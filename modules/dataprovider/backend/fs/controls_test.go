//go:build linux

package fs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	dp "github.com/relux-works/swarma-board/dataprovider"
	cf "github.com/relux-works/swarma-board/dataprovider/conformance"
)

func fixture(t *testing.T) (context.Context, *driver, *cf.Instance, dp.Board, dp.Actor) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(cancel)
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	d := newDriver()
	i, err := d.factory().Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := i.Cleanup(); err != nil {
			t.Error(err)
		}
	})
	b := dp.Board{Scope: dp.Scope{ProductID: "alpha", BoardID: "project"}, Name: "Example"}
	a := dp.Actor{AccountID: "example", RequestID: "seed", Operation: "create", At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	if err := i.Provisioner.EnsureEmpty(ctx, b); err != nil {
		t.Fatal(err)
	}
	return ctx, d, i, b, a
}
func TestPhysicalPredecisionKills(t *testing.T) {
	for _, point := range physicalPoints {
		if point.Stage != cf.BeforeDecision {
			continue
		}
		t.Run(point.ID, func(t *testing.T) {
			ctx, d, i, b, a := fixture(t)
			c := i.Provider.(*controlled)
			ticket, err := d.Arm(ctx, c.root, point, cf.Kill)
			if err != nil {
				t.Fatal(err)
			}
			defer ticket.Close()
			done := make(chan error, 1)
			go func() {
				_, err := c.Update(ctx, b.Scope, dp.Revision{Scope: b.Scope}, a, func(dp.Snapshot) (dp.ChangeSet, error) {
					return dp.ChangeSet{Upserts: []dp.Element{{ID: "TASK-1", Type: "task", Name: "Example", Status: "backlog"}}}, nil
				})
				done <- err
			}()
			if err := ticket.WaitReached(ctx); err != nil {
				t.Fatal(err)
			}
			receipt, err := ticket.Trigger(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if receipt.ProcessExit == nil || *receipt.ProcessExit == 0 {
				t.Fatal("missing real process death")
			}
			raw, _ := json.Marshal(receipt)
			t.Log(string(raw))
			if err := <-done; err == nil {
				t.Fatal("killed command succeeded")
			}
			p, _, err := i.Restart(ctx)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := p.View(ctx, b.Scope)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Revision.Counter != 0 || len(snapshot.Elements) != 0 {
				t.Fatal("uncommitted state exposed after kill")
			}
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
			p, _, err = i.Restart(ctx)
			if err != nil {
				t.Fatal(err)
			}
			cp, err := p.Checkpoint(ctx, b.Scope)
			if err != nil {
				t.Fatal(err)
			}
			if err := cp.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func fingerprintTree(root string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(root, path)
		fmt.Fprintf(h, "%s:%s:", relative, info.Mode())
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			io.WriteString(h, target)
		} else if info.Mode().IsRegular() {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			h.Write(b)
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil)), err
}
func TestStrictEvidenceRetained(t *testing.T) {
	for _, kind := range []string{"schema", "unknown-scope-file", "orphan-generation", "orphan-staging", "duplicate-intent", "unknown-canonical-field"} {
		t.Run(kind, func(t *testing.T) {
			ctx, _, i, b, _ := fixture(t)
			c := i.Provider.(*controlled)
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "schema":
				if err := os.Remove(filepath.Join(c.root, "schema.json")); err != nil {
					t.Fatal(err)
				}
			case "unknown-scope-file":
				if err := os.WriteFile(filepath.Join(c.root, scopePath(b.Scope), "unknown.md"), []byte("opaque"), 0400); err != nil {
					t.Fatal(err)
				}
			case "orphan-generation":
				if err := os.Mkdir(filepath.Join(c.root, scopePath(b.Scope), "gen-"+strings.Repeat("a", 32)), 0700); err != nil {
					t.Fatal(err)
				}
			case "orphan-staging":
				if err := os.Mkdir(filepath.Join(c.root, "staging", strings.Repeat("b", 32)), 0700); err != nil {
					t.Fatal(err)
				}
			case "duplicate-intent":
				if err := os.WriteFile(filepath.Join(c.root, "transactions", strings.Repeat("c", 32)+".json"), []byte(`{"scope":{},"scope":{},"old":null,"new":"gen-cccccccccccccccccccccccccccccccc","provision":true}`), 0400); err != nil {
					t.Fatal(err)
				}
			case "unknown-canonical-field":
				gen, err := fixtureGeneration(c.root, b.Scope)
				if err != nil {
					t.Fatal(err)
				}
				name := filepath.Join(gen, "snapshot.json")
				data, err := os.ReadFile(name)
				if err != nil {
					t.Fatal(err)
				}
				data = append([]byte(`{"extra":0,`), data[1:]...)
				if err := replaceFixture(name, data); err != nil {
					t.Fatal(err)
				}
			}
			before, err := fingerprintTree(c.root)
			if err != nil {
				t.Fatal(err)
			}
			p, err := Open(ctx, c.root)
			if p != nil {
				_ = p.Close()
			}
			if err == nil {
				t.Fatal("unsafe tree admitted")
			}
			after, err := fingerprintTree(c.root)
			if err != nil {
				t.Fatal(err)
			}
			if before != after {
				t.Fatal("refusal modified retained evidence")
			}
		})
	}
}

type repeatByte byte

func (b repeatByte) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(b)
	}
	return len(p), nil
}
func generated(b byte, n int64) dp.ResourceInput {
	h := sha256.New()
	_, _ = io.CopyN(h, repeatByte(b), n)
	return dp.ResourceInput{Info: dp.ResourceInfo{SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}, Reader: io.LimitReader(repeatByte(b), n)}
}
func TestResourceFileBoundary(t *testing.T) {
	ctx, _, i, b, a := fixture(t)
	input := generated('x', dp.MaxResourceBytes)
	commit, err := i.Provider.Update(ctx, b.Scope, dp.Revision{Scope: b.Scope}, a, func(dp.Snapshot) (dp.ChangeSet, error) {
		return dp.ChangeSet{PutResources: []dp.ResourceInput{input}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	rr, err := i.Provider.ReadResource(ctx, b.Scope, input.Info.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	n, err := io.Copy(io.Discard, rr.Reader)
	ce := rr.Reader.Close()
	if err != nil || ce != nil || n != dp.MaxResourceBytes {
		t.Fatal("at-cap read failed", n, err, ce)
	}
	oversized := generated('y', dp.MaxResourceBytes+1)
	_, err = i.Provider.Update(ctx, b.Scope, commit.Revision, a, func(dp.Snapshot) (dp.ChangeSet, error) {
		return dp.ChangeSet{PutResources: []dp.ResourceInput{oversized}}, nil
	})
	if code, ok := dp.CodeOf(err); !ok || code != dp.PayloadTooLarge {
		t.Fatal(err)
	}
	snapshot, err := i.Provider.View(ctx, b.Scope)
	if err != nil || snapshot.Revision != commit.Revision {
		t.Fatal("one-over refusal changed state", err)
	}
}
func TestUncertainRenameBarrier(t *testing.T) {
	ctx, _, i, b, a := fixture(t)
	c := i.Provider.(*controlled)
	p, _ := c.current()
	p.hook = func(_ context.Context, id string) error {
		if id == "renamed" {
			return dp.NewError(dp.StorageError)
		}
		return nil
	}
	commit, err := p.Update(ctx, b.Scope, dp.Revision{Scope: b.Scope}, a, func(dp.Snapshot) (dp.ChangeSet, error) { return dp.ChangeSet{}, nil })
	if code, ok := dp.CodeOf(err); !ok || code != dp.CommitOutcomeUnknown || commit.Revision != (dp.Revision{}) || commit.ChangedIDs != nil || commit.ChangedResourceIDs != nil {
		t.Fatal("rename ambiguity lost", err)
	}
	if code, _ := dp.CodeOf(p.Ready(ctx)); code != dp.Unavailable {
		t.Fatal("ambiguity not latched")
	}
	p.hook = nil
	recovered, _, err := i.Restart(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := recovered.View(ctx, b.Scope)
	if err != nil || snapshot.Revision.Counter != 1 {
		t.Fatal("uncertain publication not reconciled", err)
	}
}

func TestLosslessTextAdmission(t *testing.T) {
	ctx, _, i, b, a := fixture(t)
	bad := b
	bad.Scope.BoardID = "invalid-text"
	bad.Name = string([]byte{0xc3})
	bad.Description = string([]byte{0xa9})
	if code, _ := dp.CodeOf(i.Provisioner.EnsureEmpty(ctx, bad)); code != dp.InvalidSnapshot {
		t.Fatal("split invalid UTF-8 board accepted")
	}
	_, err := i.Provider.Update(ctx, b.Scope, dp.Revision{Scope: b.Scope}, a, func(dp.Snapshot) (dp.ChangeSet, error) {
		return dp.ChangeSet{Upserts: []dp.Element{{ID: "TASK-1", Type: "task", Name: "Example", Status: "backlog", ResourceRefs: []dp.ResourceRef{{Name: string([]byte{0xc3}), URI: string([]byte{0xa9}) + "urn:example:value", Type: "outcome"}}}}}, nil
	})
	if code, _ := dp.CodeOf(err); code != dp.InvalidSnapshot {
		t.Fatal("split invalid UTF-8 reference accepted", err)
	}
	snapshot, err := i.Provider.View(ctx, b.Scope)
	if err != nil || snapshot.Revision.Counter != 0 || len(snapshot.Elements) != 0 {
		t.Fatal("text refusal changed state", err)
	}
}

func TestScopedAmbiguityBarrier(t *testing.T) {
	ctx, _, i, b, a := fixture(t)
	other := b
	other.Scope.ProductID = "beta"
	if err := i.Provisioner.EnsureEmpty(ctx, other); err != nil {
		t.Fatal(err)
	}
	c := i.Provider.(*controlled)
	p, _ := c.current()
	p.hook = func(_ context.Context, id string) error {
		if id == "parent_sync" {
			return dp.NewError(dp.StorageError)
		}
		return nil
	}
	_, err := p.Update(ctx, b.Scope, dp.Revision{Scope: b.Scope}, a, func(dp.Snapshot) (dp.ChangeSet, error) { return dp.ChangeSet{}, nil })
	if code, _ := dp.CodeOf(err); code != dp.CommitOutcomeUnknown {
		t.Fatal(err)
	}
	p.hook = nil
	snapshot, err := p.View(ctx, b.Scope)
	if code, _ := dp.CodeOf(err); code != dp.Unavailable || snapshot.Board.Scope != (dp.Scope{}) {
		t.Fatal("nonzero ambiguous View", err)
	}
	rr, err := p.ReadResource(ctx, b.Scope, strings.Repeat("a", 64))
	if code, _ := dp.CodeOf(err); code != dp.Unavailable || rr.Reader != nil || rr.Info != (dp.ResourceInfo{}) {
		t.Fatal("nonzero ambiguous ResourceReader", err)
	}
	cp, err := p.Checkpoint(ctx, b.Scope)
	if code, _ := dp.CodeOf(err); code != dp.Unavailable || cp != nil {
		t.Fatal("ambiguous checkpoint exposed", err)
	}
	foreign, err := p.View(ctx, other.Scope)
	if err != nil || foreign.Revision.Counter != 0 {
		t.Fatal("independent scope unavailable or modified", err)
	}
	if _, err := p.Update(ctx, other.Scope, foreign.Revision, a, func(dp.Snapshot) (dp.ChangeSet, error) { return dp.ChangeSet{}, nil }); err != nil {
		t.Fatal("independent scope update refused", err)
	}
	if code, _ := dp.CodeOf(p.Ready(ctx)); code != dp.Unavailable {
		t.Fatal("provider readiness ignored a latched scope")
	}
}

func TestPhysicalDiskFull(t *testing.T) {
	root := os.Getenv("FS_ENOSPC_ROOT")
	if root == "" {
		t.Log("NOT_RUN: requires separately bounded disposable tmpfs")
		return
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	p, err := Open(ctx, state)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	b := dp.Board{Scope: dp.Scope{ProductID: "alpha", BoardID: "project"}, Name: "Example"}
	if err := p.EnsureEmpty(ctx, b); err != nil {
		t.Fatal(err)
	}
	filler, err := os.OpenFile(filepath.Join(root, "filler"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	buf := make([]byte, 32768)
	for {
		n, e := filler.Write(buf)
		total += int64(n)
		if e != nil {
			if !errors.Is(e, syscall.ENOSPC) {
				t.Fatal(e)
			}
			break
		}
		if total > 20<<20 {
			t.Fatal("disposable filesystem exceeded its physical allowance")
		}
	}
	_ = filler.Close()
	t.Logf("actual ENOSPC after %d filler bytes", total)
	a := dp.Actor{AccountID: "example", RequestID: "full", Operation: "set_notes", At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	commit, err := p.Update(ctx, b.Scope, dp.Revision{Scope: b.Scope}, a, func(dp.Snapshot) (dp.ChangeSet, error) { return dp.ChangeSet{}, nil })
	if code, _ := dp.CodeOf(err); code != dp.StorageError || commit.Revision != (dp.Revision{}) || commit.ChangedIDs != nil {
		t.Fatal("disk-full outcome", commit, err)
	}
	if err := os.Remove(filepath.Join(root, "filler")); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := Open(ctx, state)
	if err != nil {
		t.Fatal("reopen after providing space", err)
	}
	defer recovered.Close()
	snapshot, err := recovered.View(ctx, b.Scope)
	if err != nil || snapshot.Revision.Counter != 0 || len(snapshot.Elements) != 0 {
		t.Fatal("disk-full rollback changed old state", err)
	}
	cp, err := recovered.Checkpoint(ctx, b.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dp.EncodeCheckpoint(ctx, io.Discard, cp); err != nil {
		t.Fatal(err)
	}
}

func TestLongCallerDeadline(t *testing.T) {
	ctx, _, i, b, a := fixture(t)
	data := []byte("abc")
	sum := sha256.Sum256(data)
	info := dp.ResourceInfo{SHA256: hex.EncodeToString(sum[:]), Size: 3}
	_, err := i.Provider.Update(ctx, b.Scope, dp.Revision{Scope: b.Scope}, a, func(dp.Snapshot) (dp.ChangeSet, error) {
		return dp.ChangeSet{PutResources: []dp.ResourceInput{{Info: info, Reader: strings.NewReader("abc")}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	c := i.Provider.(*controlled)
	p, _ := c.current()
	p.hook = func(ctx context.Context, id string) error {
		if id == "resource_read" {
			deadline, ok := ctx.Deadline()
			t.Logf("actual backing deadline=%s present=%v remaining=%s error=%v", deadline.Format(time.RFC3339Nano), ok, time.Until(deadline), ctx.Err())
		}
		return nil
	}
	parent, cancel := context.WithTimeout(ctx, 2*dp.ReadTimeout)
	defer cancel()
	started := time.Now()
	rr, err := i.Provider.ReadResource(parent, b.Scope, info.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	defer rr.Reader.Close()
	if _, err := rr.Reader.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-time.After(dp.ReadTimeout + 100*time.Millisecond):
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	_, err = rr.Reader.Read(make([]byte, 1))
	t.Logf("actual elapsed=%s read_error=%v", time.Since(started), err)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("long caller extended the resource lifetime", err)
	}
}

func TestDetectedCorruptionStopsReadiness(t *testing.T) {
	ctx, _, i, b, a := fixture(t)
	c := i.Provider.(*controlled)
	gen, err := fixtureGeneration(c.root, b.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := replaceFixture(filepath.Join(gen, "snapshot.json"), []byte("corrupt")); err != nil {
		t.Fatal(err)
	}
	before, err := fingerprintTree(c.root)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := i.Provider.View(ctx, b.Scope)
	if code, _ := dp.CodeOf(err); code != dp.StorageError || snapshot.Board.Scope != (dp.Scope{}) {
		t.Fatal("corruption exposed nonzero state", err)
	}
	if code, _ := dp.CodeOf(i.Provider.Ready(ctx)); code != dp.Unavailable {
		t.Fatal("detected corruption still ready")
	}
	calls := 0
	_, err = i.Provider.Update(ctx, b.Scope, dp.Revision{Scope: b.Scope}, a, func(dp.Snapshot) (dp.ChangeSet, error) { calls++; return dp.ChangeSet{}, nil })
	if code, _ := dp.CodeOf(err); code != dp.Unavailable || calls != 0 {
		t.Fatal("corrupt scope admitted mutation", err)
	}
	after, err := fingerprintTree(c.root)
	if err != nil || before != after {
		t.Fatal("detected corruption evidence changed", err)
	}
}
