package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/relux-works/swarma-board/internal/contract"
)

type peerPolicy struct {
	principal                  contract.Principal
	err, readiness             error
	grant                      bool
	projects                   []contract.Project
	resolved, required, listed int
}

func (p *peerPolicy) Ready(context.Context) error { return p.readiness }
func (p *peerPolicy) ResolvePeer(_ context.Context, uid uint32) (contract.Principal, error) {
	p.resolved++
	if uid != 42 {
		return contract.Principal{}, errors.New("private unknown UID")
	}
	return p.principal, p.err
}
func (p *peerPolicy) Require(context.Context, contract.Principal, string, string) error {
	p.required++
	if !p.grant {
		return errors.New("private grant data")
	}
	return nil
}
func (p *peerPolicy) Projects(context.Context, contract.Principal) ([]contract.Project, error) {
	p.listed++
	return p.projects, p.err
}
func TestPeerAdapterCurrentPolicyAndFailClosed(t *testing.T) {
	ctx := context.Background()
	var typedNil *peerPolicy
	for _, r := range []ProtectedPeerResolver{nil, typedNil} {
		if _, e := NewPeerAuthorizer(r); e == nil {
			t.Fatal("missing protected authority accepted")
		}
	}
	p := &peerPolicy{principal: contract.Principal{AccountID: "host-account"}, grant: true,
		projects: []contract.Project{{ID: "beta"}, {ID: "alpha"}}}
	a, e := NewPeerAuthorizer(p)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.Authenticate(ctx, "remote-token"); e == nil {
		t.Fatal("peer adapter accepted Bearer")
	}
	for _, uid := range []uint32{0, 43} {
		if _, e = a.AuthenticatePeer(ctx, uid); e == nil {
			t.Fatal("unknown UID accepted")
		}
	}
	principal, e := a.AuthenticatePeer(ctx, 42)
	if e != nil || principal.AccountID != "host-account" {
		t.Fatal(principal, e)
	}
	if e = a.Require(ctx, principal, "alpha", "viewer"); e != nil {
		t.Fatal(e)
	}
	projects, e := a.Projects(ctx, principal)
	if e != nil || projects[0].ID != "alpha" {
		t.Fatal(projects, e)
	}
	projects[0].ID = "mutated"
	if p.projects[1].ID != "alpha" {
		t.Fatal("policy result aliased")
	}
	p.principal.AccountID = "new-mapping"
	principal, e = a.AuthenticatePeer(ctx, 42)
	if e != nil || principal.AccountID != "new-mapping" {
		t.Fatal("stale mapping", principal, e)
	}
	p.grant = false
	if e = a.Require(ctx, principal, "alpha", "viewer"); e == nil || e.Error() != "forbidden" {
		t.Fatal(e)
	}
	if p.required != 2 || p.listed != 1 {
		t.Fatal("grants not delegated on each call")
	}
	p.err = errors.New("private disabled account")
	if _, e = a.AuthenticatePeer(ctx, 42); e == nil || e.Error() != "unauthorized" {
		t.Fatal(e)
	}
	if _, e = a.Projects(ctx, principal); e == nil || e.Error() != "forbidden" {
		t.Fatal(e)
	}
	p.err = nil
	for _, account := range []string{"", "bad account", "bad\naccount"} {
		p.principal.AccountID = account
		if _, e = a.AuthenticatePeer(ctx, 42); e == nil {
			t.Fatal("invalid account accepted")
		}
	}
	p.readiness = errors.New("private policy unavailable")
	if e = a.Ready(ctx); e == nil || e.Error() != "unavailable" {
		t.Fatal(e)
	}
	if _, e = a.AuthenticatePeer(ctx, 42); e == nil {
		t.Fatal("unready mapping accepted")
	}
	if e = a.Require(ctx, principal, "alpha", "viewer"); e == nil {
		t.Fatal("unready grants accepted")
	}
	if _, e = a.Projects(ctx, principal); e == nil {
		t.Fatal("unready projects accepted")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e = a.AuthenticatePeer(cancelled, 42); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}
