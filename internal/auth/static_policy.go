package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"unicode/utf8"

	"github.com/relux-works/swarma-board/internal/contract"
)

// ErrStaticPolicyRestartRequired means drift was observed. Restoring the old
// file does not clear it: stop admission and restart with an approved release.
var ErrStaticPolicyRestartRequired = errors.New("static policy unavailable; restart required")

const maxStaticPolicyBytes = 8 << 20

type staticUIDMapping struct {
	UID       uint32 `json:"uid"`
	AccountID string `json:"account_id"`
}

type staticPolicy struct {
	SchemaVersion int                `json:"schema_version"`
	PolicyVersion string             `json:"policy_version"`
	Mode          string             `json:"mode"`
	TrustProfile  string             `json:"trust_profile"`
	RemoteTLS     bool               `json:"remote_tls"`
	Projects      []Project          `json:"projects"`
	Accounts      []Account          `json:"accounts"`
	UIDMappings   []staticUIDMapping `json:"uid_mappings"`
	Grants        []Grant            `json:"grants"`
}

// StaticPeerResolver implements first-deployment policy, not a runtime
// publisher. Only the kernel UID is an identity input. All processes sharing
// that UID are one trust unit, with one explicit board account and grant set.
// The accepted snapshot is immutable and every method checks file integrity.
// It is safe for concurrent requests. Linux and Darwin have custody paths;
// other platforms refuse construction. Darwin native qualification is separate.
// Construct once per service lifetime; never reconstruct to recover from drift.
type StaticPeerResolver struct {
	mu       sync.Mutex
	path     string
	stamp    staticFileStamp
	digest   [sha256.Size]byte
	tripped  bool
	uid      uint32
	account  Account
	projects map[string]contract.Project
	grants   map[string]string
}

// NewStaticPeerResolver accepts only a closed v1, sharedUID, single-trust-unit
// policy with remote_tls=false. expectedPolicyVersion comes from the approved
// release configuration, not from reading the policy itself. The path must be
// absolute and clean with no symlink components. The regular file must be owned
// by the effective UID, have exactly mode 0600, and have only one hard link.
// No policy bytes are returned in errors. Absence or invalid policy prevents
// startup; no synthetic or Bearer fallback is provided.
func NewStaticPeerResolver(path, expectedPolicyVersion string) (*StaticPeerResolver, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || !validID(expectedPolicyVersion) {
		return nil, refuse("unavailable")
	}
	data, stamp, e := readStaticPolicyFile(path)
	if e != nil || !utf8.Valid(data) || validateStaticPolicyJSON(data) != nil {
		return nil, refuse("unavailable")
	}
	var f staticPolicy
	if json.Unmarshal(data, &f) != nil || f.SchemaVersion != 1 || f.PolicyVersion != expectedPolicyVersion ||
		f.Mode != "sharedUID" || f.TrustProfile != "single-trust-unit" || f.RemoteTLS ||
		len(f.Accounts) != 1 || len(f.UIDMappings) != 1 {
		return nil, refuse("unavailable")
	}
	account, mapping := f.Accounts[0], f.UIDMappings[0]
	if !validAccount(account.ID) || mapping.AccountID != account.ID || mapping.UID != stamp.uid {
		return nil, refuse("unavailable")
	}
	r := &StaticPeerResolver{path: path, stamp: stamp, digest: sha256.Sum256(data), uid: mapping.UID,
		account: account, projects: map[string]contract.Project{}, grants: map[string]string{}}
	for _, p := range f.Projects {
		if !validID(p.ID) {
			return nil, refuse("unavailable")
		}
		if _, exists := r.projects[p.ID]; exists {
			return nil, refuse("unavailable")
		}
		r.projects[p.ID] = contract.Project{ID: p.ID, Name: p.Name, Description: p.Description}
	}
	for _, g := range f.Grants {
		if g.AccountID != account.ID || rank(g.Role) == 0 || r.grants[g.ProjectID] != "" {
			return nil, refuse("unavailable")
		}
		if _, exists := r.projects[g.ProjectID]; !exists {
			return nil, refuse("unavailable")
		}
		r.grants[g.ProjectID] = g.Role
	}
	return r, nil
}

func validateStaticPolicyJSON(data []byte) error {
	if e := uniqueJSON(data); e != nil {
		return e
	}
	root, e := exactObject(data, "schema_version", "policy_version", "mode", "trust_profile", "remote_tls", "projects", "accounts", "uid_mappings", "grants")
	if e != nil {
		return e
	}
	for _, v := range root {
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return errors.New("null policy field")
		}
	}
	for collection, keys := range map[string][]string{
		"projects": {"id", "name", "description"}, "accounts": {"id", "disabled"},
		"uid_mappings": {"uid", "account_id"}, "grants": {"account_id", "project_id", "role"},
	} {
		var rows []json.RawMessage
		if json.Unmarshal(root[collection], &rows) != nil || rows == nil {
			return errors.New("invalid policy array")
		}
		for _, row := range rows {
			m, e := exactObject(row, keys...)
			if e != nil {
				return e
			}
			for _, v := range m {
				if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
					return errors.New("null policy field")
				}
			}
		}
	}
	return nil
}

func (r *StaticPeerResolver) Ready(ctx context.Context) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if r == nil || r.path == "" {
		return refuse("unavailable")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tripped {
		return ErrStaticPolicyRestartRequired
	}
	data, stamp, e := readStaticPolicyFile(r.path)
	if e != nil || stamp != r.stamp || sha256.Sum256(data) != r.digest {
		r.tripped = true
		return ErrStaticPolicyRestartRequired
	}
	return ctx.Err()
}

func (r *StaticPeerResolver) ResolvePeer(ctx context.Context, uid uint32) (contract.Principal, error) {
	if e := r.Ready(ctx); e != nil {
		return contract.Principal{}, e
	}
	if uid != r.uid || r.account.Disabled {
		return contract.Principal{}, refuse("unauthorized")
	}
	return contract.Principal{AccountID: r.account.ID}, nil
}

func (r *StaticPeerResolver) Require(ctx context.Context, p contract.Principal, project, role string) error {
	if e := r.Ready(ctx); e != nil {
		return e
	}
	if p.AccountID != r.account.ID || r.account.Disabled {
		return refuse("unauthorized")
	}
	if rank(role) == 0 || rank(r.grants[project]) < rank(role) {
		return refuse("forbidden")
	}
	return nil
}

func (r *StaticPeerResolver) Projects(ctx context.Context, p contract.Principal) ([]contract.Project, error) {
	if e := r.Ready(ctx); e != nil {
		return nil, e
	}
	if p.AccountID != r.account.ID || r.account.Disabled {
		return nil, refuse("unauthorized")
	}
	out := make([]contract.Project, 0, len(r.grants))
	for id := range r.grants {
		out = append(out, r.projects[id])
	}
	sortProjects(out)
	return out, nil
}

var _ ProtectedPeerResolver = (*StaticPeerResolver)(nil)
