package maintenance

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const auditTestPrincipal = "synthetic-board-auditor"

func auditTestKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal("generate ephemeral synthetic key:", err)
	}
	t.Cleanup(func() { clear(private) })
	return public, private
}

func auditTestVerifier(t *testing.T, public ed25519.PublicKey, status string) *AuditSSHSIGVerifier {
	t.Helper()
	v, err := NewAuditSSHSIGVerifier(AuditSSHSIGTrust{
		KeyID: "unapproved-synthetic-key", Principal: auditTestPrincipal,
		Namespace: AuditSSHSIGNamespace, PublicKey: public, Status: status,
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// This encoder lives in tests only. It deliberately does not use production wire
// helpers; OpenSSH below supplies an independent interoperable implementation.
func auditTestFields(fields ...[]byte) []byte {
	var out bytes.Buffer
	for _, field := range fields {
		_ = binary.Write(&out, binary.BigEndian, uint32(len(field)))
		out.Write(field)
	}
	return out.Bytes()
}

func auditTestArmor(blob []byte) []byte {
	return []byte(auditArmorHeader + base64.StdEncoding.EncodeToString(blob) + "\n" + auditArmorFooter + "\n")
}

func auditTestSign(private ed25519.PrivateKey, manifest []byte, namespace, hashAlgorithm, reserved, algorithm string) []byte {
	var hash []byte
	if hashAlgorithm == "sha256" {
		sum := sha256.Sum256(manifest)
		hash = sum[:]
	} else {
		sum := sha512.Sum512(manifest)
		hash = sum[:]
	}
	signed := append([]byte("SSHSIG"), auditTestFields([]byte(namespace), []byte(reserved), []byte(hashAlgorithm), hash)...)
	raw := ed25519.Sign(private, signed)
	public := private.Public().(ed25519.PublicKey)
	blob := append([]byte{'S', 'S', 'H', 'S', 'I', 'G', 0, 0, 0, 1}, auditTestFields(
		auditTestFields([]byte("ssh-ed25519"), public), []byte(namespace), []byte(reserved), []byte(hashAlgorithm),
		auditTestFields([]byte(algorithm), raw))...)
	return auditTestArmor(blob)
}

func TestAuditSSHSIGAcceptancePreservesEvidence(t *testing.T) {
	_, b := exported(t)
	original := clone(b)
	public, private := auditTestKey(t)
	for _, status := range []string{AuditTestSigned, AuditTrustedSigned} {
		v := auditTestVerifier(t, public, status)
		for _, hash := range []string{"sha256", "sha512"} {
			sig := auditTestSign(private, b.Manifest, AuditSSHSIGNamespace, hash, "", "ssh-ed25519")
			originalSig := bytes.Clone(sig)
			accepted, err := VerifyAuditSignature(testContext, b, sig, auditTestPrincipal, v)
			if err != nil || accepted.Status != status || accepted.KeyID != "unapproved-synthetic-key" ||
				accepted.Principal != auditTestPrincipal || accepted.Namespace != AuditSSHSIGNamespace ||
				!strings.HasPrefix(accepted.PublicKeyFingerprint, "SHA256:") ||
				accepted.Integrity.Manifest.Status != Unsigned || accepted.Integrity.Manifest.Revision != 2 {
				t.Fatalf("acceptance metadata mismatch: %+v, %v", accepted, err)
			}
			if err = VerifyProtected(testContext, b, sig, "unapproved-synthetic-key", v); err != nil {
				t.Fatal("frozen verifier seam:", err)
			}
			if !reflect.DeepEqual(b, original) || !bytes.Equal(sig, originalSig) {
				t.Fatal("verification mutated retained evidence")
			}
		}
	}
	// A constructor copies the caller's trust key rather than retaining mutable
	// caller configuration. All trust classes here are synthetic local tests.
	v := auditTestVerifier(t, public, AuditTestSigned)
	clear(public)
	sig := auditTestSign(private, b.Manifest, AuditSSHSIGNamespace, "sha512", "", "ssh-ed25519")
	if _, err := VerifyAuditSignature(testContext, b, sig, auditTestPrincipal, v); err != nil {
		t.Fatal("mutable trust alias:", err)
	}
}

func TestAuditSSHSIGRefusesTrustAndIdentityMismatch(t *testing.T) {
	_, b := exported(t)
	public, private := auditTestKey(t)
	v := auditTestVerifier(t, public, AuditTestSigned)
	sig := auditTestSign(private, b.Manifest, AuditSSHSIGNamespace, "sha512", "", "ssh-ed25519")
	trust := v.trust
	for _, edit := range []func(*AuditSSHSIGTrust){
		func(a *AuditSSHSIGTrust) { a.KeyID = "" },
		func(a *AuditSSHSIGTrust) { a.Principal = "" },
		func(a *AuditSSHSIGTrust) { a.Principal = "bad\nprincipal" },
		func(a *AuditSSHSIGTrust) { a.Namespace = "file" },
		func(a *AuditSSHSIGTrust) { a.PublicKey = nil },
		func(a *AuditSSHSIGTrust) { a.PublicKey = make([]byte, 31) },
		func(a *AuditSSHSIGTrust) { a.Status = "" },
		func(a *AuditSSHSIGTrust) { a.Status = Unsigned },
	} {
		candidate := trust
		edit(&candidate)
		if got, err := NewAuditSSHSIGVerifier(candidate); err == nil || got != nil {
			t.Fatal("accepted invalid trust configuration")
		}
	}
	for _, verifier := range []*AuditSSHSIGVerifier{nil, {}} {
		got, err := VerifyAuditSignature(testContext, b, sig, auditTestPrincipal, verifier)
		if err == nil || got != (AuditVerification{}) {
			t.Fatal("zero/nil verifier accepted")
		}
		if verifier.VerifyManifest(testContext, trust.KeyID, b.Manifest, sig) == nil {
			t.Fatal("direct zero/nil verifier accepted")
		}
	}
	for _, principal := range []string{"", "other", auditTestPrincipal + "*", auditTestPrincipal + ",other"} {
		got, err := VerifyAuditSignature(testContext, b, sig, principal, v)
		if err == nil || got != (AuditVerification{}) {
			t.Fatal("principal mismatch accepted")
		}
	}
	otherPublic, otherPrivate := auditTestKey(t)
	for _, tc := range []struct {
		name string
		v    *AuditSSHSIGVerifier
		sig  []byte
	}{
		{"wrong-trusted-key", auditTestVerifier(t, otherPublic, AuditTestSigned), sig},
		{"artifact-key-not-trusted", v, auditTestSign(otherPrivate, b.Manifest, AuditSSHSIGNamespace, "sha512", "", "ssh-ed25519")},
		{"namespace", v, auditTestSign(private, b.Manifest, "file", "sha512", "", "ssh-ed25519")},
		{"empty-namespace", v, auditTestSign(private, b.Manifest, "", "sha512", "", "ssh-ed25519")},
		{"hash", v, auditTestSign(private, b.Manifest, AuditSSHSIGNamespace, "sha1", "", "ssh-ed25519")},
		{"reserved-extension", v, auditTestSign(private, b.Manifest, AuditSSHSIGNamespace, "sha512", "future", "ssh-ed25519")},
		{"algorithm", v, auditTestSign(private, b.Manifest, AuditSSHSIGNamespace, "sha512", "", "ssh-rsa")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := VerifyAuditSignature(testContext, b, tc.sig, auditTestPrincipal, tc.v)
			if err == nil || got != (AuditVerification{}) {
				t.Fatal("mismatch returned acceptance metadata")
			}
		})
	}
	if err := v.VerifyManifest(testContext, "another-selector", b.Manifest, sig); !errors.Is(err, ErrSignatureGate) {
		t.Fatal("wrong selector:", err)
	}
	ctx, cancel := context.WithCancel(testContext)
	cancel()
	if _, err := VerifyAuditSignature(ctx, b, sig, auditTestPrincipal, v); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestAuditSSHSIGRefusesMalformedAndOversize(t *testing.T) {
	_, b := exported(t)
	public, private := auditTestKey(t)
	v := auditTestVerifier(t, public, AuditTestSigned)
	sig := auditTestSign(private, b.Manifest, AuditSSHSIGNamespace, "sha512", "", "ssh-ed25519")
	lines := bytes.Split(sig, []byte{'\n'})
	blob, err := base64.StdEncoding.DecodeString(string(lines[1]))
	if err != nil {
		t.Fatal(err)
	}
	badVersion := bytes.Clone(blob)
	badVersion[9] = 2
	badMagic := bytes.Clone(blob)
	badMagic[0] = 'X'
	badLength := bytes.Clone(blob)
	binary.BigEndian.PutUint32(badLength[10:14], ^uint32(0))
	badSig := bytes.Clone(blob)
	badSig[len(badSig)-1] ^= 1
	cases := [][]byte{nil, {}, []byte("unsigned"), bytes.Repeat([]byte{'x'}, MaxAuditSignature+1),
		append([]byte("prefix"), sig...), append(bytes.Clone(sig), 'x'), append(bytes.Clone(sig), sig...),
		bytes.ReplaceAll(sig, []byte{'\n'}, []byte("\r\n")),
		bytes.Replace(sig, []byte{'\n'}, []byte("\n\n"), 1),
		bytes.Replace(sig, []byte{'\n'}, []byte("\nHeader: x\n"), 1),
		auditTestArmor(badMagic), auditTestArmor(badVersion), auditTestArmor(badLength), auditTestArmor(badSig),
		auditTestArmor(append(bytes.Clone(blob), 0)),
		[]byte(auditArmorHeader + "!!!!\n" + auditArmorFooter),
	}
	for i := 0; i < len(blob); i++ {
		cases = append(cases, auditTestArmor(blob[:i]))
	}
	for i, signature := range cases {
		got, err := VerifyAuditSignature(testContext, b, signature, auditTestPrincipal, v)
		if err == nil || got != (AuditVerification{}) {
			t.Fatalf("malformed case %d accepted", i)
		}
	}
	// Valid LF armor may wrap at 76 columns, and the last LF is optional.
	encoded := string(lines[1])
	var wrapped strings.Builder
	for len(encoded) > 76 {
		wrapped.WriteString(encoded[:76] + "\n")
		encoded = encoded[76:]
	}
	wrapped.WriteString(encoded)
	good := []byte(auditArmorHeader + wrapped.String() + "\n" + auditArmorFooter)
	if _, err := VerifyAuditSignature(testContext, b, good, auditTestPrincipal, v); err != nil {
		t.Fatal("wrapped armor:", err)
	}
	if err := v.VerifyManifest(testContext, v.trust.KeyID, bytes.Repeat([]byte{'x'}, MaxManifest+1), sig); !errors.Is(err, ErrLimit) {
		t.Fatal("manifest ceiling:", err)
	}
}

func TestAuditSSHSIGBindsCanonicalManifestAndCoherentArtifacts(t *testing.T) {
	_, b := exported(t)
	public, private := auditTestKey(t)
	v := auditTestVerifier(t, public, AuditTestSigned)
	sig := auditTestSign(private, b.Manifest, AuditSSHSIGNamespace, "sha512", "", "ssh-ed25519")
	for _, edit := range []func(*Bundle){
		func(x *Bundle) { x.Manifest = append(x.Manifest, ' ') },
		func(x *Bundle) { x.Snapshot[0] ^= 1 },
		func(x *Bundle) { x.Journal[0] ^= 1 },
		func(x *Bundle) { editManifest(t, x, func(m *Manifest) { m.ExporterCommit = "changed" }) },
		func(x *Bundle) { editManifest(t, x, func(m *Manifest) { m.Status = AuditTrustedSigned }) },
		func(x *Bundle) { editManifest(t, x, func(m *Manifest) { m.DeclaredSignerKeyID = "attacker" }) },
		func(x *Bundle) { editManifest(t, x, func(m *Manifest) { m.Revision++ }) },
		func(x *Bundle) {
			x.Journal = bytes.Replace(x.Journal, []byte(`"Revision":1`), []byte(`"Revision":2`), 1)
			rehash(t, x)
		},
	} {
		x := clone(b)
		edit(&x)
		if reflect.DeepEqual(x, b) {
			t.Fatal("test edit did not change evidence")
		}
		// Even a valid dedicated signature must not bless incoherent/noncanonical
		// data. Recomputed outer hashes must not bypass revision/native checks.
		for _, signature := range [][]byte{sig, auditTestSign(private, x.Manifest, AuditSSHSIGNamespace, "sha512", "", "ssh-ed25519")} {
			got, err := VerifyAuditSignature(testContext, x, signature, auditTestPrincipal, v)
			if err == nil || got != (AuditVerification{}) {
				t.Fatal("tampered or incoherent evidence accepted")
			}
		}
	}
	// Exact canonical metadata can change coherently, but a previous signature
	// cannot authorize it, even when the hashes and revision still agree.
	x := clone(b)
	editManifest(t, &x, func(m *Manifest) { m.DeclaredSignerKeyID = "different-key-id" })
	if err := v.VerifyManifest(testContext, v.trust.KeyID, x.Manifest, sig); err == nil {
		t.Fatal("key selector changed")
	}
}

type auditExternalTestSigner struct {
	private ed25519.PrivateKey
	exact   []byte
	calls   int
	refuse  bool
	mutate  bool
}

func (s *auditExternalTestSigner) SignManifest(_ context.Context, keyID string, manifest []byte) ([]byte, error) {
	s.calls++
	if s.refuse || keyID != "unapproved-synthetic-key" || !bytes.Equal(manifest, s.exact) {
		return nil, errors.New("synthetic external boundary refused")
	}
	if s.mutate {
		manifest[0] ^= 1
	}
	return auditTestSign(s.private, manifest, AuditSSHSIGNamespace, "sha512", "", "ssh-ed25519"), nil
}

func TestAuditSSHSIGExternalBoundary(t *testing.T) {
	_, b := exported(t)
	original := clone(b)
	public, private := auditTestKey(t)
	_, wrongPrivate := auditTestKey(t)
	v := auditTestVerifier(t, public, AuditTestSigned)
	for _, signer := range []ExternalSigner{nil, (*auditExternalTestSigner)(nil),
		&auditExternalTestSigner{private: private, exact: b.Manifest, refuse: true},
		&auditExternalTestSigner{private: wrongPrivate, exact: b.Manifest},
		&auditExternalTestSigner{private: private, exact: b.Manifest, mutate: true},
	} {
		sig, accepted, err := RequestVerifiedAuditSignature(testContext, b, auditTestPrincipal, signer, v)
		if err == nil || sig != nil || accepted != (AuditVerification{}) {
			t.Fatal("external refusal or invalid candidate promoted")
		}
	}
	signer := &auditExternalTestSigner{private: private, exact: b.Manifest}
	for _, principal := range []string{"", "unapproved-principal"} {
		if _, _, err := RequestVerifiedAuditSignature(testContext, b, principal, signer, v); err == nil || signer.calls != 0 {
			t.Fatal("principal mismatch reached signer")
		}
	}
	x := clone(b)
	x.Snapshot[0] ^= 1
	if _, _, err := RequestVerifiedAuditSignature(testContext, x, auditTestPrincipal, signer, v); err == nil || signer.calls != 0 {
		t.Fatal("incoherent checkpoint reached signer")
	}
	sig, accepted, err := RequestVerifiedAuditSignature(testContext, b, auditTestPrincipal, signer, v)
	if err != nil || len(sig) == 0 || accepted.Status != AuditTestSigned || signer.calls != 1 {
		t.Fatal("external synthetic acceptance:", err)
	}
	if !reflect.DeepEqual(b, original) {
		t.Fatal("external signer changed retained evidence")
	}
}

func TestAuditSSHSIGOpenSSHInteroperability(t *testing.T) {
	sshKeygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Fatal("OpenSSH interoperability prerequisite unavailable")
	}
	_, b := exported(t)
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "ephemeral-synthetic-key")
	// Private material exists only in this private temporary directory and is
	// removed by testing cleanup. No host key/agent/provider is selected.
	if out, err := exec.Command(sshKeygen, "-q", "-t", "ed25519", "-N", "", "-C", "synthetic-test-only", "-f", keyPath).CombinedOutput(); err != nil {
		t.Fatalf("ephemeral ssh-keygen: %v (%s)", err, out)
	}
	publicLine, err := os.ReadFile(keyPath + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Fields(string(publicLine))
	if len(parts) < 2 || parts[0] != "ssh-ed25519" {
		t.Fatal("unexpected synthetic public key format")
	}
	blob, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil || len(blob) != 51 || binary.BigEndian.Uint32(blob[:4]) != 11 ||
		string(blob[4:15]) != "ssh-ed25519" || binary.BigEndian.Uint32(blob[15:19]) != 32 {
		t.Fatal("unexpected synthetic public key encoding")
	}
	v := auditTestVerifier(t, ed25519.PublicKey(blob[19:]), AuditTestSigned)
	for _, hash := range []string{"sha256", "sha512"} {
		cmd := exec.Command(sshKeygen, "-Y", "sign", "-f", keyPath, "-n", AuditSSHSIGNamespace, "-O", "hashalg="+hash)
		cmd.Stdin = bytes.NewReader(b.Manifest)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		sig, err := cmd.Output()
		if err != nil {
			t.Fatalf("synthetic OpenSSH sign: %v (%s)", err, stderr.Bytes())
		}
		if _, err = VerifyAuditSignature(testContext, b, sig, auditTestPrincipal, v); err != nil {
			t.Fatal("OpenSSH signature not accepted:", err)
		}
	}
	// Independently check the in-memory test encoder against OpenSSH as well.
	public, private := auditTestKey(t)
	publicBlob := auditTestFields([]byte("ssh-ed25519"), public)
	allowed := auditTestPrincipal + " namespaces=\"" + AuditSSHSIGNamespace + "\" ssh-ed25519 " + base64.StdEncoding.EncodeToString(publicBlob) + "\n"
	allowedPath := filepath.Join(dir, "synthetic-allowed-signers")
	sigPath := filepath.Join(dir, "synthetic.sig")
	if err = os.WriteFile(allowedPath, []byte(allowed), 0600); err != nil {
		t.Fatal(err)
	}
	for _, hash := range []string{"sha256", "sha512"} {
		if err = os.WriteFile(sigPath, auditTestSign(private, b.Manifest, AuditSSHSIGNamespace, hash, "", "ssh-ed25519"), 0600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(sshKeygen, "-Y", "verify", "-f", allowedPath, "-I", auditTestPrincipal, "-n", AuditSSHSIGNamespace, "-s", sigPath)
		cmd.Stdin = bytes.NewReader(b.Manifest)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("synthetic signature rejected by OpenSSH: %v (%s)", err, out)
		}
	}
}
