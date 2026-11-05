package maintenance

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

type syntheticSigner struct {
	calls int
	exact []byte
	err   error
	size  int
}

func (s *syntheticSigner) SignManifest(_ context.Context, key string, b []byte) ([]byte, error) {
	s.calls++
	if key != "unapproved-synthetic-key" || !bytes.Equal(b, s.exact) {
		return nil, errors.New("synthetic exact-byte mismatch")
	}
	b[0] = 'X' // Must not mutate retained manifest evidence.
	n := s.size
	if n == 0 {
		n = 1
	}
	return bytes.Repeat([]byte{'x'}, n), s.err
}

type syntheticPolicy struct {
	calls int
	exact []byte
	err   error
}

func (p *syntheticPolicy) VerifyManifest(_ context.Context, key string, b, sig []byte) error {
	p.calls++
	if key != "unapproved-synthetic-key" || !bytes.Equal(b, p.exact) || !bytes.Equal(sig, []byte("x")) {
		return errors.New("synthetic policy refused")
	}
	b[0] = 'X'
	sig[0] = 'z'
	return p.err
}
func TestInjectedSignatureGatesNeverSelectAuthority(t *testing.T) {
	_, b := exported(t)
	original := bytes.Clone(b.Manifest)
	unavailable := errors.New("synthetic external custodian unavailable")
	for _, s := range []ExternalSigner{nil, (*syntheticSigner)(nil), &syntheticSigner{exact: original, err: unavailable}, &syntheticSigner{exact: original, size: MaxManifest + 1}} {
		sig, e := RequestSignature(testContext, b, "unapproved-synthetic-key", s)
		if e == nil || sig != nil {
			t.Fatal("missing/refusing signer returned candidate", e)
		}
	}
	signer := &syntheticSigner{exact: original}
	if _, e := RequestSignature(testContext, b, "wrong-key", signer); !errors.Is(e, ErrSignatureGate) || signer.calls != 0 {
		t.Fatal(e)
	}
	signature, e := RequestSignature(testContext, b, "unapproved-synthetic-key", signer)
	if e != nil || signer.calls != 1 {
		t.Fatal(e)
	}
	if !bytes.Equal(b.Manifest, original) {
		t.Fatal("signer mutated manifest")
	}
	var m Manifest
	if e = canonical(b.Manifest, &m); e != nil || m.Status != Unsigned {
		t.Fatal("signature candidate promoted status", e)
	}
	for _, v := range []ProtectedVerifier{nil, (*syntheticPolicy)(nil), &syntheticPolicy{exact: original, err: unavailable}} {
		if e = VerifyProtected(testContext, b, signature, "unapproved-synthetic-key", v); e == nil {
			t.Fatal("unavailable protected policy accepted")
		}
	}
	policy := &syntheticPolicy{exact: original}
	for _, key := range []string{"", "wrong-key"} {
		if e = VerifyProtected(testContext, b, signature, key, policy); !errors.Is(e, ErrSignatureGate) || policy.calls != 0 {
			t.Fatal(e)
		}
	}
	if e = VerifyProtected(testContext, b, nil, "unapproved-synthetic-key", policy); !errors.Is(e, ErrSignatureGate) {
		t.Fatal(e)
	}
	if e = VerifyProtected(testContext, b, signature, "unapproved-synthetic-key", policy); e != nil || policy.calls != 1 {
		t.Fatal(e)
	}
	if !bytes.Equal(b.Manifest, original) || string(signature) != "x" {
		t.Fatal("policy mutated evidence")
	}
	tampered := clone(b)
	tampered.Journal[0] ^= 1
	if e = VerifyProtected(testContext, tampered, signature, "unapproved-synthetic-key", policy); e == nil || policy.calls != 1 {
		t.Fatal("tamper reached protected policy", e)
	}
	if _, e = RequestSignature(testContext, tampered, "unapproved-synthetic-key", signer); e == nil || signer.calls != 1 {
		t.Fatal("tamper reached custodian", e)
	}
	editManifest(t, &b, func(m *Manifest) { m.DeclaredSignerKeyID = "" })
	if _, e = RequestSignature(testContext, b, "unapproved-synthetic-key", signer); !errors.Is(e, ErrSignatureGate) {
		t.Fatal(e)
	}
	// These doubles test injection/control flow only. No cryptographic signature
	// or approved-key verification witness is produced by this test.
}
