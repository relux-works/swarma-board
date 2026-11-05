//go:build linux || darwin

package auth

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	"github.com/relux-works/swarma-board/internal/contract"
)

const testPolicyVersion = "synthetic-release-1"

// Darwin's default temporary directory can traverse /var -> /private/var.
// Canonicalize fixture parents, never the production policy path.
func staticTestDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func staticTestPolicy() map[string]any {
	return map[string]any{
		"schema_version": 1, "policy_version": testPolicyVersion,
		"mode": "sharedUID", "trust_profile": "single-trust-unit", "remote_tls": false,
		"projects":     []any{map[string]any{"id": "alpha", "name": "Synthetic Alpha", "description": "test only"}, map[string]any{"id": "beta", "name": "Synthetic Beta", "description": "test only"}},
		"accounts":     []any{map[string]any{"id": "shared-test-unit", "disabled": false}},
		"uid_mappings": []any{map[string]any{"uid": uint32(os.Geteuid()), "account_id": "shared-test-unit"}},
		"grants":       []any{map[string]any{"account_id": "shared-test-unit", "project_id": "alpha", "role": "editor"}},
	}
}

func writeStaticTestPolicy(t *testing.T, f map[string]any) (string, []byte) {
	t.Helper()
	data := encode(t, f)
	path := filepath.Join(staticTestDir(t), "policy.json")
	if e := os.WriteFile(path, data, 0600); e != nil {
		t.Fatal(e)
	}
	return path, data
}

func TestStaticPolicySnapshotIdentityAndGrants(t *testing.T) {
	path, _ := writeStaticTestPolicy(t, staticTestPolicy())
	r, e := NewStaticPeerResolver(path, testPolicyVersion)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	peer, e := NewPeerAuthorizer(r)
	if e != nil {
		t.Fatal(e)
	}
	p, e := peer.AuthenticatePeer(ctx, uint32(os.Geteuid()))
	if e != nil || p.AccountID != "shared-test-unit" {
		t.Fatal(p, e)
	}
	if _, e = peer.AuthenticatePeer(ctx, uint32(os.Geteuid())+1); e == nil {
		t.Fatal("unknown UID accepted")
	}
	if _, e = peer.Authenticate(ctx, "any-bearer"); e == nil {
		t.Fatal("Bearer fallback")
	}
	for _, tc := range []struct {
		project, role string
		ok            bool
	}{{"alpha", "viewer", true}, {"alpha", "editor", true}, {"alpha", "admin", false}, {"beta", "viewer", false}, {"unknown", "viewer", false}, {"alpha", "owner", false}, {"alpha", "", false}} {
		if e = r.Require(ctx, p, tc.project, tc.role); (e == nil) != tc.ok {
			t.Fatal(tc, e)
		}
	}
	for _, account := range []string{"", "unknown", "shared-test-unit-other"} {
		forged := contract.Principal{AccountID: account}
		if r.Require(ctx, forged, "alpha", "viewer") == nil {
			t.Fatal("unknown account granted")
		}
		if _, e = r.Projects(ctx, forged); e == nil {
			t.Fatal("unknown account listed projects")
		}
	}
	projects, e := r.Projects(ctx, p)
	if e != nil || len(projects) != 1 || projects[0].ID != "alpha" {
		t.Fatal(projects, e)
	}
	projects[0].ID = "mutated"
	again, e := r.Projects(ctx, p)
	if e != nil || again[0].ID != "alpha" {
		t.Fatal("snapshot aliased", again, e)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if e = r.Ready(cancelled); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if _, e = r.ResolvePeer(cancelled, uint32(os.Geteuid())); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if e = r.Require(cancelled, p, "alpha", "viewer"); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if _, e = r.Projects(cancelled, p); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if e = r.Ready(ctx); e != nil {
		t.Fatal("cancellation poisoned policy", e)
	}
	var nilResolver *StaticPeerResolver
	if nilResolver.Ready(ctx) == nil || (&StaticPeerResolver{}).Ready(ctx) == nil {
		t.Fatal("uninitialized resolver ready")
	}
}

func TestStaticPolicyDisabledAndNoGrants(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		f := staticTestPolicy()
		f["accounts"].([]any)[0].(map[string]any)["disabled"] = disabled
		f["grants"] = []any{}
		path, _ := writeStaticTestPolicy(t, f)
		r, e := NewStaticPeerResolver(path, testPolicyVersion)
		if e != nil || r.Ready(context.Background()) != nil {
			t.Fatal("valid deny policy refused", e)
		}
		ctx := context.Background()
		p := contract.Principal{AccountID: "shared-test-unit"}
		if _, e = r.ResolvePeer(ctx, uint32(os.Geteuid())); (e != nil) != disabled {
			t.Fatal(disabled, e)
		}
		if r.Require(ctx, p, "alpha", "viewer") == nil {
			t.Fatal("ungranted account authorized")
		}
		projects, e := r.Projects(ctx, p)
		if disabled && e == nil || !disabled && (e != nil || len(projects) != 0) {
			t.Fatal(projects, e)
		}
	}
}

func TestStaticPolicyClosedSchema(t *testing.T) {
	mutations := map[string]func(map[string]any){
		"new-schema":        func(f map[string]any) { f["schema_version"] = 2 },
		"version-mismatch":  func(f map[string]any) { f["policy_version"] = "other-release" },
		"dynamic-mode":      func(f map[string]any) { f["mode"] = "publisher" },
		"isolation-claim":   func(f map[string]any) { f["trust_profile"] = "per-agent" },
		"remote-tls":        func(f map[string]any) { f["remote_tls"] = true },
		"missing-tls":       func(f map[string]any) { delete(f, "remote_tls") },
		"null-tls":          func(f map[string]any) { f["remote_tls"] = nil },
		"unknown-field":     func(f map[string]any) { f["publisher"] = "invented" },
		"null-projects":     func(f map[string]any) { f["projects"] = nil },
		"duplicate-project": func(f map[string]any) { f["projects"] = append(f["projects"].([]any), f["projects"].([]any)[0]) },
		"bad-project":       func(f map[string]any) { f["projects"].([]any)[0].(map[string]any)["id"] = "../escape" },
		"case-folded-key":   func(f map[string]any) { f["Remote_TLS"] = f["remote_tls"]; delete(f, "remote_tls") },
		"multiple-accounts": func(f map[string]any) { f["accounts"] = append(f["accounts"].([]any), f["accounts"].([]any)[0]) },
		"no-account":        func(f map[string]any) { f["accounts"] = []any{} },
		"bad-account":       func(f map[string]any) { f["accounts"].([]any)[0].(map[string]any)["id"] = "bad account" },
		"missing-disabled":  func(f map[string]any) { delete(f["accounts"].([]any)[0].(map[string]any), "disabled") },
		"null-disabled":     func(f map[string]any) { f["accounts"].([]any)[0].(map[string]any)["disabled"] = nil },
		"nested-extra":      func(f map[string]any) { f["accounts"].([]any)[0].(map[string]any)["agent"] = "claimed-agent" },
		"missing-mapping":   func(f map[string]any) { f["uid_mappings"] = []any{} },
		"duplicate-mapping": func(f map[string]any) {
			f["uid_mappings"] = append(f["uid_mappings"].([]any), f["uid_mappings"].([]any)[0])
		},
		"different-uid": func(f map[string]any) {
			f["uid_mappings"].([]any)[0].(map[string]any)["uid"] = uint32(os.Geteuid()) + 1
		},
		"negative-uid":           func(f map[string]any) { f["uid_mappings"].([]any)[0].(map[string]any)["uid"] = -1 },
		"overflow-uid":           func(f map[string]any) { f["uid_mappings"].([]any)[0].(map[string]any)["uid"] = uint64(1) << 32 },
		"fractional-uid":         func(f map[string]any) { f["uid_mappings"].([]any)[0].(map[string]any)["uid"] = 1.5 },
		"null-uid":               func(f map[string]any) { f["uid_mappings"].([]any)[0].(map[string]any)["uid"] = nil },
		"unmapped-account":       func(f map[string]any) { f["uid_mappings"].([]any)[0].(map[string]any)["account_id"] = "unknown" },
		"dangling-grant-account": func(f map[string]any) { f["grants"].([]any)[0].(map[string]any)["account_id"] = "unknown" },
		"dangling-grant-project": func(f map[string]any) { f["grants"].([]any)[0].(map[string]any)["project_id"] = "unknown" },
		"duplicate-grant":        func(f map[string]any) { f["grants"] = append(f["grants"].([]any), f["grants"].([]any)[0]) },
		"bad-role":               func(f map[string]any) { f["grants"].([]any)[0].(map[string]any)["role"] = "owner" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			f := staticTestPolicy()
			mutate(f)
			path, _ := writeStaticTestPolicy(t, f)
			if r, e := NewStaticPeerResolver(path, testPolicyVersion); e == nil || r != nil {
				t.Fatal("unsafe policy accepted")
			}
		})
	}
	base := encode(t, staticTestPolicy())
	for _, data := range [][]byte{
		{}, []byte("null"), []byte("[]"), {0xff}, append(append([]byte{}, base...), []byte(" {}")...),
		bytes.Replace(base, []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":1`), 1),
		bytes.Replace(base, []byte(`"remote_tls":false`), []byte(`"remote_tls":false,"remote_\u0074ls":false`), 1),
		bytes.Repeat([]byte(" "), maxStaticPolicyBytes+1),
	} {
		path := filepath.Join(staticTestDir(t), "policy.json")
		if e := os.WriteFile(path, data, 0600); e != nil {
			t.Fatal(e)
		}
		if _, e := NewStaticPeerResolver(path, testPolicyVersion); e == nil {
			t.Fatal("malformed policy accepted")
		}
	}
}

func TestStaticPolicyFileCustody(t *testing.T) {
	for _, mode := range []os.FileMode{0000, 0400, 0640, 0644, 0700, 0600 | os.ModeSetuid, 0600 | os.ModeSetgid, 0600 | os.ModeSticky} {
		path, _ := writeStaticTestPolicy(t, staticTestPolicy())
		// A parent-inherited group need not be one this user belongs to.
		// Establish our own effective group on this disposable file before
		// requesting setgid; some kernels silently clear an unowned group bit.
		if mode&os.ModeSetgid != 0 {
			if e := os.Chown(path, -1, os.Getegid()); e != nil {
				t.Fatal(e)
			}
			var st syscall.Stat_t
			if e := syscall.Lstat(path, &st); e != nil {
				t.Fatal(e)
			}
			if st.Uid != uint32(os.Geteuid()) || st.Gid != uint32(os.Getegid()) {
				t.Fatalf("setgid fixture owner/group: uid=%d gid=%d, want uid=%d gid=%d", st.Uid, st.Gid, os.Geteuid(), os.Getegid())
			}
		}
		if e := os.Chmod(path, mode); e != nil {
			t.Fatal(e)
		}
		st, e := os.Lstat(path)
		if e != nil {
			t.Fatal(e)
		}
		if st.Mode() != mode {
			t.Fatalf("unsafe-mode fixture precondition: got %v, want %v", st.Mode(), mode)
		}
		t.Logf("unsafe-mode fixture established: %v", st.Mode())
		if _, e := NewStaticPeerResolver(path, testPolicyVersion); e == nil {
			t.Fatalf("unsafe mode %v accepted", mode)
		}
	}
	path, _ := writeStaticTestPolicy(t, staticTestPolicy())
	for _, version := range []string{"", "Bad Version", "../version"} {
		if _, e := NewStaticPeerResolver(path, version); e == nil {
			t.Fatal("unpinned version accepted")
		}
	}
	for _, p := range []string{"policy.json", filepath.Dir(path) + "/./policy.json", filepath.Dir(path) + "/missing", filepath.Dir(path)} {
		if _, e := NewStaticPeerResolver(p, testPolicyVersion); e == nil {
			t.Fatal("unsafe path accepted", p)
		}
	}
	alias := filepath.Join(staticTestDir(t), "alias")
	if e := os.Symlink(path, alias); e != nil {
		t.Fatal(e)
	}
	if _, e := NewStaticPeerResolver(alias, testPolicyVersion); e == nil {
		t.Fatal("file symlink accepted")
	}
	parent := filepath.Join(staticTestDir(t), "parent")
	if e := os.Symlink(filepath.Dir(path), parent); e != nil {
		t.Fatal(e)
	}
	if _, e := NewStaticPeerResolver(filepath.Join(parent, "policy.json"), testPolicyVersion); e == nil {
		t.Fatal("parent symlink accepted")
	}
	link := filepath.Join(filepath.Dir(path), "hardlink")
	if e := os.Link(path, link); e != nil {
		t.Fatal(e)
	}
	if _, e := NewStaticPeerResolver(path, testPolicyVersion); e == nil {
		t.Fatal("multiple hard links accepted")
	}
	fifo := filepath.Join(staticTestDir(t), "fifo")
	if e := syscall.Mkfifo(fifo, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := NewStaticPeerResolver(fifo, testPolicyVersion); e == nil {
		t.Fatal("FIFO accepted")
	}
}

func TestStaticPolicyObservedDriftLatchesAndNeverReloads(t *testing.T) {
	for _, change := range []string{"content", "replace-identical", "chmod", "remove", "symlink", "parent-symlink", "hardlink", "touch"} {
		t.Run(change, func(t *testing.T) {
			path, original := writeStaticTestPolicy(t, staticTestPolicy())
			r, e := NewStaticPeerResolver(path, testPolicyVersion)
			if e != nil {
				t.Fatal(e)
			}
			ctx := context.Background()
			p, e := r.ResolvePeer(ctx, uint32(os.Geteuid()))
			if e != nil {
				t.Fatal(e)
			}
			switch change {
			case "content":
				f := staticTestPolicy()
				f["grants"].([]any)[0].(map[string]any)["role"] = "admin"
				e = os.WriteFile(path, encode(t, f), 0600)
			case "replace-identical":
				tmp := path + ".replacement"
				if e = os.WriteFile(tmp, original, 0600); e == nil {
					e = os.Rename(tmp, path)
				}
			case "chmod":
				e = os.Chmod(path, 0644)
			case "remove":
				e = os.Remove(path)
			case "symlink":
				if e = os.Rename(path, path+".saved"); e == nil {
					e = os.Symlink(path+".saved", path)
				}
			case "parent-symlink":
				dir := filepath.Dir(path)
				if e = os.Rename(dir, dir+".saved"); e == nil {
					t.Cleanup(func() { os.RemoveAll(dir + ".saved") })
					e = os.Symlink(dir+".saved", dir)
				}
			case "hardlink":
				e = os.Link(path, path+".hardlink")
			case "touch":
				st, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				e = os.Chtimes(path, st.ModTime().Add(-1), st.ModTime().Add(-1))
			}
			if e != nil {
				t.Fatal(e)
			}
			// A grant check itself must notice drift, without a readiness probe.
			if e = r.Require(ctx, p, "alpha", "viewer"); !errors.Is(e, ErrStaticPolicyRestartRequired) {
				t.Fatal("stale grant accepted", e)
			}
			if e = r.Ready(ctx); !errors.Is(e, ErrStaticPolicyRestartRequired) {
				t.Fatal(e)
			}
			if _, e = r.ResolvePeer(ctx, uint32(os.Geteuid())); !errors.Is(e, ErrStaticPolicyRestartRequired) {
				t.Fatal(e)
			}
			if _, e = r.Projects(ctx, p); !errors.Is(e, ErrStaticPolicyRestartRequired) {
				t.Fatal(e)
			}
			// Restore approved bytes at a safe path: this instance stays closed.
			if change == "parent-symlink" {
				dir := filepath.Dir(path)
				if e = os.Remove(dir); e != nil {
					t.Fatal(e)
				}
				if e = os.Rename(dir+".saved", dir); e != nil {
					t.Fatal(e)
				}
			}
			if e = os.Remove(path); e != nil && !os.IsNotExist(e) {
				t.Fatal(e)
			}
			if change == "hardlink" {
				if e = os.Remove(path + ".hardlink"); e != nil {
					t.Fatal(e)
				}
			}
			if e = os.WriteFile(path, original, 0600); e != nil {
				t.Fatal(e)
			}
			if e = r.Ready(ctx); !errors.Is(e, ErrStaticPolicyRestartRequired) {
				t.Fatal("restoration cleared latch", e)
			}
			fresh, e := NewStaticPeerResolver(path, testPolicyVersion)
			if e != nil || fresh.Require(ctx, p, "alpha", "viewer") != nil {
				t.Fatal("approved restart failed", e)
			}
			if fresh.Require(ctx, p, "alpha", "admin") == nil {
				t.Fatal("drift published an admin grant")
			}
		})
	}
}

func TestStaticPolicyConcurrentChecksAndDrift(t *testing.T) {
	path, _ := writeStaticTestPolicy(t, staticTestPolicy())
	r, e := NewStaticPeerResolver(path, testPolicyVersion)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	p := contract.Principal{AccountID: "shared-test-unit"}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				for _, e := range []error{r.Ready(ctx), r.Require(ctx, p, "alpha", "viewer")} {
					if e != nil && !errors.Is(e, ErrStaticPolicyRestartRequired) {
						t.Error(e)
					}
				}
				if _, e := r.ResolvePeer(ctx, uint32(os.Geteuid())); e != nil && !errors.Is(e, ErrStaticPolicyRestartRequired) {
					t.Error(e)
				}
				if _, e := r.Projects(ctx, p); e != nil && !errors.Is(e, ErrStaticPolicyRestartRequired) {
					t.Error(e)
				}
			}
		}()
	}
	if e := os.Chmod(path, 0644); e != nil {
		t.Fatal(e)
	}
	wg.Wait()
	if !errors.Is(r.Ready(ctx), ErrStaticPolicyRestartRequired) {
		t.Fatal("drift not latched")
	}
}
