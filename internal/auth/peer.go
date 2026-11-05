package auth

import (
	"context"
	"reflect"

	"github.com/relux-works/swarma-board/internal/contract"
)

// ProtectedPeerResolver is supplied by the Host's protected identity/grant
// authority. Every method must consult accepted policy (including disabled
// accounts, UID mappings and grants). First deployment uses StaticPeerResolver:
// its release snapshot stays fixed and detected drift requires restart. A future
// protected publisher (A2-036) supplies live revocations. ResolvePeer must refuse
// unknown/disabled/unmapped UIDs; Require and
// Projects must recheck current account status and grants, without caching them for
// the lifetime of a connection. Implementations must support concurrent requests.
type ProtectedPeerResolver interface {
	ResolvePeer(context.Context, uint32) (contract.Principal, error)
	Require(context.Context, contract.Principal, string, string) error
	Projects(context.Context, contract.Principal) ([]contract.Project, error)
	Ready(context.Context) error
}

// PeerAuthorizer adapts a protected resolver to both the package-local HTTP peer
// seam and the unchanged contract.Authorizer role interface. Use a separate
// Authorizer/handler for remote TLS Bearer authentication.
type PeerAuthorizer struct{ resolver ProtectedPeerResolver }

func NewPeerAuthorizer(resolver ProtectedPeerResolver) (*PeerAuthorizer, error) {
	if resolver == nil {
		return nil, refuse("unavailable")
	}
	v := reflect.ValueOf(resolver)
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Func, reflect.Slice:
		if v.IsNil() {
			return nil, refuse("unavailable")
		}
	}
	return &PeerAuthorizer{resolver: resolver}, nil
}

func (a *PeerAuthorizer) Ready(ctx context.Context) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if a == nil || a.resolver == nil || a.resolver.Ready(ctx) != nil {
		return refuse("unavailable")
	}
	return ctx.Err()
}

func (a *PeerAuthorizer) AuthenticatePeer(ctx context.Context, uid uint32) (contract.Principal, error) {
	if e := a.Ready(ctx); e != nil {
		return contract.Principal{}, e
	}
	p, e := a.resolver.ResolvePeer(ctx, uid)
	if e != nil || !validAccount(p.AccountID) {
		return contract.Principal{}, refuse("unauthorized")
	}
	if e = ctx.Err(); e != nil {
		return contract.Principal{}, e
	}
	return p, nil
}

// Authenticate refuses Bearer credentials in local peer mode, even when they are
// valid in another authority. HTTP additionally rejects any Authorization header.
func (a *PeerAuthorizer) Authenticate(context.Context, string) (contract.Principal, error) {
	return contract.Principal{}, refuse("unauthorized")
}

func (a *PeerAuthorizer) Require(ctx context.Context, p contract.Principal, project, role string) error {
	if e := a.Ready(ctx); e != nil {
		return e
	}
	if !validAccount(p.AccountID) {
		return refuse("unauthorized")
	}
	if !validID(project) || rank(role) == 0 {
		return refuse("forbidden")
	}
	if e := a.resolver.Require(ctx, p, project, role); e != nil {
		// Resolver errors can contain private policy data; never reflect them.
		return refuse("forbidden")
	}
	return ctx.Err()
}

func (a *PeerAuthorizer) Projects(ctx context.Context, p contract.Principal) ([]contract.Project, error) {
	if e := a.Ready(ctx); e != nil {
		return nil, e
	}
	if !validAccount(p.AccountID) {
		return nil, refuse("unauthorized")
	}
	projects, e := a.resolver.Projects(ctx, p)
	if e != nil {
		return nil, refuse("forbidden")
	}
	seen := make(map[string]bool, len(projects))
	out := make([]contract.Project, 0, len(projects))
	for _, project := range projects {
		if !validID(project.ID) || seen[project.ID] {
			return nil, refuse("unavailable")
		}
		seen[project.ID] = true
		out = append(out, project)
	}
	if e = ctx.Err(); e != nil {
		return nil, e
	}
	sortProjects(out)
	return out, nil
}

var _ contract.Authorizer = (*PeerAuthorizer)(nil)
