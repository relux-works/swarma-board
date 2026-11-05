package maintenance

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

func FuzzAuditSSHSIGParser(f *testing.F) {
	public, private := auditTestKeyForFuzz(f)
	verifier, err := NewAuditSSHSIGVerifier(AuditSSHSIGTrust{
		KeyID: "synthetic-fuzz-key", Principal: "synthetic-fuzz-principal", Namespace: AuditSSHSIGNamespace,
		PublicKey: public, Status: AuditTestSigned,
	})
	if err != nil {
		f.Fatal(err)
	}
	manifest, err := marshal(Manifest{Format: Format, Version: 1, Status: Unsigned, DeclaredSignerKeyID: "synthetic-fuzz-key"})
	if err != nil {
		f.Fatal(err)
	}
	signature := auditTestSign(private, manifest, AuditSSHSIGNamespace, "sha512", "", "ssh-ed25519")
	f.Add(signature)
	f.Add([]byte{})
	f.Add([]byte(auditArmorHeader + auditArmorFooter))
	f.Add(auditTestArmor([]byte{'S', 'S', 'H', 'S', 'I', 'G', 0, 0, 0, 1, 255, 255, 255, 255}))
	f.Fuzz(func(t *testing.T, candidate []byte) {
		before := bytes.Clone(candidate)
		err := verifier.VerifyManifest(testContext, "synthetic-fuzz-key", manifest, candidate)
		if !bytes.Equal(before, candidate) {
			t.Fatal("parser mutated input")
		}
		if err == nil {
			changed := bytes.Replace(manifest, []byte("synthetic-fuzz-key"), []byte("synthetic-fuzz-kez"), 1)
			if verifier.verifySSHSIG(changed, candidate) == nil {
				t.Fatal("accepted signature did not bind exact manifest bytes")
			}
		}
	})
}

// Fuzz keys are random in-memory test material, cleared after the fuzz worker.
func auditTestKeyForFuzz(f *testing.F) (ed25519.PublicKey, ed25519.PrivateKey) {
	f.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { clear(private) })
	return public, private
}
