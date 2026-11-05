package app

import (
	"context"
	"github.com/relux-works/swarma-board/internal/auth"
	"github.com/relux-works/swarma-board/internal/contract"
)

// SyntheticPeerResolver is an immutable fake, never a Host authority. It maps
// exactly one real kernel UID to an explicitly selected synthetic account and
// grants admin on exactly one synthetic project. It never consults bearer
// fixtures, headers, ambient configuration or credential files for local grants.
// All agents sharing that UID receive the same identity.
type SyntheticPeerResolver struct {
	UID     uint32
	Account string
	Project contract.Project
}

func (s *SyntheticPeerResolver) Ready(ctx context.Context) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if s == nil || s.Account == "" || s.Project.ID == "" {
		return unavailable()
	}
	return nil
}
func (s *SyntheticPeerResolver) ResolvePeer(ctx context.Context, uid uint32) (contract.Principal, error) {
	if e := s.Ready(ctx); e != nil {
		return contract.Principal{}, e
	}
	if uid != s.UID {
		return contract.Principal{}, unavailable()
	}
	return contract.Principal{AccountID: s.Account}, nil
}
func (s *SyntheticPeerResolver) Require(ctx context.Context, p contract.Principal, project, role string) error {
	if e := s.Ready(ctx); e != nil {
		return e
	}
	if p.AccountID != s.Account || project != s.Project.ID || (role != "viewer" && role != "editor" && role != "admin") {
		return unavailable()
	}
	return nil
}
func (s *SyntheticPeerResolver) Projects(ctx context.Context, p contract.Principal) ([]contract.Project, error) {
	if e := s.Require(ctx, p, s.Project.ID, "viewer"); e != nil {
		return nil, e
	}
	return []contract.Project{s.Project}, nil
}

var _ auth.ProtectedPeerResolver = (*SyntheticPeerResolver)(nil)
