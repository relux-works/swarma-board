package maintenance

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"fmt"
)

const (
	AuditSSHSIGNamespace = "swarma-board-audit-v1"
	MaxAuditSignature    = MaxManifest
	AuditTestSigned      = "test-signed"
	AuditTrustedSigned   = "trusted-signed"
)

// AuditSSHSIGTrust is supplied by protected caller configuration, never loaded
// from an export. PublicKey is a raw dedicated Ed25519 public key, not an SSH
// certificate. Principal is an exact local identity binding: SSHSIG contains no
// principal. KeyID is an opaque custody selector, not a path or lookup command.
// Status must explicitly be AuditTestSigned or AuditTrustedSigned. Selecting
// trusted-signed asserts caller trust; it does not establish custody approval.
type AuditSSHSIGTrust struct {
	KeyID, Principal, Namespace string
	PublicKey                   ed25519.PublicKey
	Status                      string
}

// AuditSSHSIGVerifier holds one immutable, explicit trust binding. Its zero value
// refuses everything. It performs verification only and has no key discovery,
// subprocess, private-key, agent, network or platform signing implementation.
type AuditSSHSIGVerifier struct{ trust AuditSSHSIGTrust }

var _ ProtectedVerifier = (*AuditSSHSIGVerifier)(nil)

func NewAuditSSHSIGVerifier(trust AuditSSHSIGTrust) (*AuditSSHSIGVerifier, error) {
	if !label(trust.KeyID, false) || !label(trust.Principal, false) ||
		trust.Namespace != AuditSSHSIGNamespace || len(trust.PublicKey) != ed25519.PublicKeySize ||
		(trust.Status != AuditTestSigned && trust.Status != AuditTrustedSigned) {
		return nil, ErrSignatureGate
	}
	trust.PublicKey = bytes.Clone(trust.PublicKey)
	return &AuditSSHSIGVerifier{trust: trust}, nil
}

func (v *AuditSSHSIGVerifier) binding(principal string) error {
	if v == nil || v.trust.Namespace != AuditSSHSIGNamespace ||
		len(v.trust.PublicKey) != ed25519.PublicKeySize ||
		(v.trust.Status != AuditTestSigned && v.trust.Status != AuditTrustedSigned) ||
		principal == "" || principal != v.trust.Principal {
		return ErrSignatureGate
	}
	return nil
}

// VerifyManifest implements the frozen ProtectedVerifier seam. The principal is
// pinned at construction; use VerifyAuditSignature to also compare a caller's
// expected principal and validate the entire snapshot/journal checkpoint.
func (v *AuditSSHSIGVerifier) VerifyManifest(ctx context.Context, keyID string, manifest, signature []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if v == nil || v.binding(v.trust.Principal) != nil || keyID == "" || keyID != v.trust.KeyID {
		return ErrSignatureGate
	}
	if len(manifest) > MaxManifest || len(signature) > MaxAuditSignature {
		return ErrLimit
	}
	var m Manifest
	if canonical(manifest, &m) != nil || m.Format != Format || m.Version != 1 ||
		m.Status != Unsigned || m.DeclaredSignerKeyID != keyID {
		return ErrSignatureGate
	}
	if err := v.verifySSHSIG(manifest, signature); err != nil {
		return err
	}
	return ctx.Err()
}

// AuditVerification is derived acceptance metadata, never a replacement manifest.
// Integrity alone is unsigned; only this separate result may say test-signed or
// trusted-signed. Neither status is a production/custody admission witness.
type AuditVerification struct {
	Integrity                       Integrity
	Status, KeyID, Principal        string
	Namespace, PublicKeyFingerprint string
}

// VerifyAuditSignature first checks frozen canonical encoding, artifact hashes,
// native snapshot validation and revision coherence, then the dedicated SSHSIG.
// Any failure returns zero metadata. Artifact key material never selects trust.
func VerifyAuditSignature(ctx context.Context, b Bundle, signature []byte, expectedPrincipal string, verifier *AuditSSHSIGVerifier) (AuditVerification, error) {
	if err := verifier.binding(expectedPrincipal); err != nil {
		return AuditVerification{}, err
	}
	if len(signature) == 0 {
		return AuditVerification{}, ErrSignatureGate
	}
	if len(signature) > MaxAuditSignature {
		return AuditVerification{}, ErrLimit
	}
	integrity, err := Verify(ctx, bytes.NewReader(b.Manifest), bytes.NewReader(b.Snapshot), bytes.NewReader(b.Journal), Limits{})
	if err != nil {
		return AuditVerification{}, err
	}
	if err = verifier.VerifyManifest(ctx, verifier.trust.KeyID, b.Manifest, signature); err != nil {
		return AuditVerification{}, err
	}
	fingerprint := sha256.Sum256(auditPublicKeyBlob(verifier.trust.PublicKey))
	return AuditVerification{Integrity: integrity, Status: verifier.trust.Status, KeyID: verifier.trust.KeyID,
		Principal: verifier.trust.Principal, Namespace: verifier.trust.Namespace,
		PublicKeyFingerprint: "SHA256:" + base64.RawStdEncoding.EncodeToString(fingerprint[:])}, nil
}

// RequestVerifiedAuditSignature wraps the existing injected signer seam. The
// external adapter must already be configured for this dedicated key/namespace;
// this package does not choose or activate Keymaker/keyvault or any other API.
// Returned candidate bytes are accepted only after full independent verification.
func RequestVerifiedAuditSignature(ctx context.Context, b Bundle, expectedPrincipal string, signer ExternalSigner, verifier *AuditSSHSIGVerifier) ([]byte, AuditVerification, error) {
	if err := verifier.binding(expectedPrincipal); err != nil {
		return nil, AuditVerification{}, err
	}
	signature, err := RequestSignature(ctx, b, verifier.trust.KeyID, signer)
	if err != nil {
		return nil, AuditVerification{}, err
	}
	accepted, err := VerifyAuditSignature(ctx, b, signature, expectedPrincipal, verifier)
	if err != nil {
		return nil, AuditVerification{}, err
	}
	return signature, accepted, nil
}

const auditArmorHeader = "-----BEGIN SSH SIGNATURE-----\n"
const auditArmorFooter = "-----END SSH SIGNATURE-----"

func (v *AuditSSHSIGVerifier) verifySSHSIG(manifest, armored []byte) error {
	// Strict single LF-armored block; reject prefixes, headers, trailing blocks,
	// whitespace and noncanonical base64. One final LF is optional (OpenSSH).
	armored = bytes.TrimSuffix(armored, []byte{'\n'})
	if !bytes.HasPrefix(armored, []byte(auditArmorHeader)) || !bytes.HasSuffix(armored, []byte("\n"+auditArmorFooter)) {
		return ErrSignatureGate
	}
	body := armored[len(auditArmorHeader) : len(armored)-len(auditArmorFooter)-1]
	for _, line := range bytes.Split(body, []byte{'\n'}) {
		if len(line) == 0 {
			return ErrSignatureGate
		}
	}
	encoded := bytes.ReplaceAll(body, []byte{'\n'}, nil)
	blob, err := base64.StdEncoding.Strict().DecodeString(string(encoded))
	if err != nil || base64.StdEncoding.EncodeToString(blob) != string(encoded) || len(blob) < 10 ||
		string(blob[:6]) != "SSHSIG" || binary.BigEndian.Uint32(blob[6:10]) != 1 {
		return ErrSignatureGate
	}
	wire := auditWire(blob[10:])
	key, ok1 := wire.field()
	namespace, ok2 := wire.field()
	reserved, ok3 := wire.field()
	hashAlgorithm, ok4 := wire.field()
	signature, ok5 := wire.field()
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || len(wire) != 0 ||
		!bytes.Equal(key, auditPublicKeyBlob(v.trust.PublicKey)) || string(namespace) != v.trust.Namespace || len(reserved) != 0 {
		return ErrSignatureGate
	}
	// D4 deliberately narrows the protocol: Ed25519 only, no certificates,
	// version extensions or reserved data. Both specified hashes are supported.
	var hash []byte
	switch string(hashAlgorithm) {
	case "sha256":
		sum := sha256.Sum256(manifest)
		hash = sum[:]
	case "sha512":
		sum := sha512.Sum512(manifest)
		hash = sum[:]
	default:
		return ErrSignatureGate
	}
	sigWire := auditWire(signature)
	algorithm, ok1 := sigWire.field()
	raw, ok2 := sigWire.field()
	if !ok1 || !ok2 || len(sigWire) != 0 || string(algorithm) != "ssh-ed25519" || len(raw) != ed25519.SignatureSize {
		return ErrSignatureGate
	}
	signed := []byte("SSHSIG")
	for _, field := range [][]byte{namespace, reserved, hashAlgorithm, hash} {
		signed = auditAppendField(signed, field)
	}
	if !ed25519.Verify(v.trust.PublicKey, signed, raw) {
		return fmt.Errorf("%w: audit SSHSIG mismatch", ErrSignatureGate)
	}
	return nil
}

// SSH length fields are checked against remaining bytes before slicing, including
// on 32-bit systems; no allocation is driven by an untrusted length field.
type auditWire []byte

func (w *auditWire) field() ([]byte, bool) {
	if len(*w) < 4 {
		return nil, false
	}
	n := binary.BigEndian.Uint32((*w)[:4])
	*w = (*w)[4:]
	if uint64(n) > uint64(len(*w)) {
		return nil, false
	}
	field := (*w)[:int(n)]
	*w = (*w)[int(n):]
	return field, true
}

func auditAppendField(dst, field []byte) []byte {
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(field)))
	return append(dst, field...)
}

func auditPublicKeyBlob(key ed25519.PublicKey) []byte {
	return auditAppendField(auditAppendField(nil, []byte("ssh-ed25519")), key)
}
