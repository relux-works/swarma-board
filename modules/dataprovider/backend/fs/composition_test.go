//go:build linux

package fs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	dp "github.com/relux-works/swarma-board/dataprovider"
)

// Use the actual filesystem provider and returned common handle throughout.
// A real caller deadline complements the frozen C41 live/cancellation controls.
func TestCheckpointCloseLifetimeOutcomes(t *testing.T) {
	for _, mode := range []string{"live", "cancel", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			p, err := Open(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := p.Close(); err != nil {
					t.Error(err)
				}
			})
			b := dp.Board{Scope: dp.Scope{ProductID: "alpha", BoardID: "project"}, Name: "Example"}
			if err := p.EnsureEmpty(t.Context(), b); err != nil {
				t.Fatal(err)
			}
			in := generated('d', 3)
			a := dp.Actor{AccountID: "example", RequestID: "close-lifetime", Operation: "put_resource", At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
			if _, err := p.Update(t.Context(), b.Scope, dp.Revision{Scope: b.Scope}, a, func(dp.Snapshot) (dp.ChangeSet, error) {
				return dp.ChangeSet{PutResources: []dp.ResourceInput{in}}, nil
			}); err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			parent, cancel := context.WithCancel(t.Context())
			if mode == "deadline" {
				cancel()
				parent, cancel = context.WithTimeout(t.Context(), 100*time.Millisecond)
			}
			defer cancel()
			cp, err := p.Checkpoint(parent, b.Scope)
			if err != nil {
				t.Fatal(err)
			}
			defer cp.Close()
			snapshot, events := cp.Snapshot(), cp.Events()
			child, err := cp.OpenResource(in.Info.SHA256)
			if err != nil {
				t.Fatal(err)
			}
			defer child.Close()
			var outcome error
			if mode == "cancel" {
				cancel()
				outcome = context.Canceled
			} else if mode == "deadline" {
				<-parent.Done()
				outcome = context.DeadlineExceeded
			}
			if outcome != nil {
				if _, err := child.Read(make([]byte, 1)); !errors.Is(err, outcome) {
					t.Fatal("child lost admission outcome", err)
				}
				later, err := cp.OpenResource(in.Info.SHA256)
				if later != nil {
					_ = later.Close()
					t.Fatal("expired admission exposed a child")
				}
				if !errors.Is(err, outcome) {
					t.Fatal("later child open lost admission outcome", err)
				}
			}
			first, second := cp.Close(), cp.Close()
			if first != second {
				t.Fatal("Close outcome changed on repeat")
			}
			if outcome == nil {
				if first != nil {
					t.Fatal("live Close failed", first)
				}
				_, err := child.Read(make([]byte, 1))
				if code, _ := dp.CodeOf(err); code != dp.Unavailable {
					t.Fatal("closed live child did not refuse as unavailable", err)
				}
			} else {
				code, _ := dp.CodeOf(first)
				if _, closed := first.(*dp.Error); !closed || code != dp.Unavailable || !errors.Is(first, outcome) {
					t.Fatal("Close lost closed admission outcome", first)
				}
			}
			if err := child.Close(); err != nil {
				t.Fatal("child cleanup failed", err)
			}
			before, _ := json.Marshal([]any{snapshot, events})
			after, _ := json.Marshal([]any{cp.Snapshot(), cp.Events()})
			if !bytes.Equal(before, after) {
				t.Fatal("Close changed retained metadata/history")
			}
			cancel()
			if cp.Close() != first {
				t.Fatal("later caller cancellation replaced completed Close outcome")
			}
			if err := p.Close(); err != nil {
				t.Fatal("released checkpoint retained provider backing", err)
			}
			t.Logf("actual %s elapsed=%s; repeated Close stable; child/provider cleanup complete", mode, time.Since(started))
		})
	}
}

func TestCheckpointExportAdmission(t *testing.T) {
	for _, state := range []string{"empty", "history", "resource"} {
		t.Run(state, func(t *testing.T) {
			ctx, _, i, b, a := fixture(t)
			if state != "empty" {
				_, err := i.Provider.Update(ctx, b.Scope, dp.Revision{Scope: b.Scope}, a, func(dp.Snapshot) (dp.ChangeSet, error) {
					if state == "resource" {
						return dp.ChangeSet{PutResources: []dp.ResourceInput{generated('x', 3)}}, nil
					}
					return dp.ChangeSet{}, nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			for _, cancelAtWrite := range []bool{false, true} {
				parent, cancel := context.WithCancel(ctx)
				cp, err := i.Provider.Checkpoint(parent, b.Scope)
				if err != nil {
					cancel()
					t.Fatal(err)
				}
				var dst bytes.Buffer
				var writer io.Writer = &dst
				if cancelAtWrite {
					writer = cancelWriter{Writer: &dst, cancel: cancel}
				}
				n, err := dp.EncodeCheckpoint(ctx, writer, cp)
				cancel()
				if cancelAtWrite {
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("canceled export: n=%d err=%v", n, err)
					}
				} else if err != nil || n != int64(dst.Len()) || n == 0 {
					t.Fatalf("live export: n=%d bytes=%d err=%v", n, dst.Len(), err)
				}
				first, second := cp.Close(), cp.Close()
				if first != second {
					t.Fatal("export Close outcome changed on repeat")
				}
				if cancelAtWrite {
					code, _ := dp.CodeOf(first)
					if _, closed := first.(*dp.Error); !closed || code != dp.Unavailable || !errors.Is(first, context.Canceled) {
						t.Fatalf("canceled export Close lost closed cancellation outcome: %v", first)
					}
				} else if first != nil {
					t.Fatal("successful export Close failed", first)
				}
			}
		})
	}
}

type cancelWriter struct {
	io.Writer
	cancel context.CancelFunc
}

func (w cancelWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	w.cancel()
	return n, err
}

type errorInput struct{ err error }

func (r errorInput) Read([]byte) (int, error) { return 0, r.err }
func TestClosedInputErrorsAndRollback(t *testing.T) {
	for _, inputErr := range []error{errors.New("synthetic input error"), dp.NewError(dp.NotFound), fmt.Errorf("wrapped: %w", dp.NewError(dp.CommitOutcomeUnknown)), context.Canceled, context.DeadlineExceeded} {
		t.Run(fmt.Sprintf("%T-%s", inputErr, strings.Split(inputErr.Error(), ":")[0]), func(t *testing.T) {
			ctx, _, i, b, a := fixture(t)
			in := generated('x', 1)
			in.Reader = errorInput{inputErr}
			commit, err := i.Provider.Update(ctx, b.Scope, dp.Revision{Scope: b.Scope}, a, func(dp.Snapshot) (dp.ChangeSet, error) {
				return dp.ChangeSet{PutResources: []dp.ResourceInput{generated('a', 2), in}}, nil
			})
			expected := dp.StorageError
			if errors.Is(inputErr, context.Canceled) || errors.Is(inputErr, context.DeadlineExceeded) {
				expected = dp.Unavailable
				if !errors.Is(err, inputErr) {
					t.Fatalf("lost context sentinel: %v", err)
				}
			}
			if code, _ := dp.CodeOf(err); code != expected || commit.Revision.Counter != 0 {
				t.Fatalf("code/result: %v %+v", err, commit)
			}
			for n := 0; n < 2; n++ {
				p, _, err := i.Restart(ctx)
				if err != nil {
					t.Fatal(err)
				}
				s, err := p.View(ctx, b.Scope)
				if err != nil || s.Revision.Counter != 0 || len(s.Resources) != 0 {
					t.Fatalf("rollback: %+v %v", s, err)
				}
			}
		})
	}
}

func TestStreamAdmissionCleanup(t *testing.T) {
	for _, mode := range []string{"live", "cancel", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			parent, cancel := context.WithCancel(t.Context())
			defer cancel()
			cap := time.Hour
			if mode == "deadline" {
				cap = 20 * time.Millisecond
			}
			ctx, finish := admitStream(parent, cap)
			child, childCancel := context.WithCancel(ctx)
			if mode == "cancel" {
				cancel()
			}
			if mode == "deadline" {
				select {
				case <-child.Done():
				case <-time.After(time.Second):
					t.Fatal("deadline did not cancel owned child")
				}
			}
			if mode == "cancel" {
				select {
				case <-child.Done():
				case <-time.After(time.Second):
					t.Fatal("caller did not cancel owned child")
				}
			}
			childCancel()
			finish()
			finish()
			switch mode {
			case "live":
				if ctx.Err() != nil {
					t.Fatal("cleanup canceled admission", ctx.Err())
				}
				cancel()
				if !errors.Is(ctx.Err(), context.Canceled) {
					t.Fatal("cleanup hid original caller cancellation")
				}
			case "cancel":
				if !errors.Is(ctx.Err(), context.Canceled) {
					t.Fatal(ctx.Err())
				}
			case "deadline":
				if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
					t.Fatal(ctx.Err())
				}
			}
		})
	}
}

// This injection is after atomic visibility and BEFORE the parent fsync. It is
// deliberately separate from the shared driver's AfterDecision durable points.
func TestPreSyncAmbiguityWholeEnvelope(t *testing.T) {
	ctx, _, i, b, a := fixture(t)
	c := i.Provider.(*controlled)
	p, _ := c.current()
	resource := generated('z', 3)
	p.hook = func(_ context.Context, id string) error {
		if id == "renamed" {
			return errors.New("synthetic parent-sync seam")
		}
		return nil
	}
	commit, err := p.Update(ctx, b.Scope, dp.Revision{Scope: b.Scope}, a, func(dp.Snapshot) (dp.ChangeSet, error) {
		return dp.ChangeSet{Upserts: []dp.Element{{ID: "TASK-1", Type: "task", Name: "Example", Notes: "## Literal heading\n", Status: "backlog"}}, PutResources: []dp.ResourceInput{resource}}, nil
	})
	if code, _ := dp.CodeOf(err); code != dp.CommitOutcomeUnknown || commit.Revision != (dp.Revision{}) || commit.ChangedIDs != nil || commit.ChangedResourceIDs != nil {
		t.Fatalf("unknown outcome/result: %+v %v", commit, err)
	}
	evidence, err := fingerprintTree(c.root)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := p.View(ctx, b.Scope)
	if code, _ := dp.CodeOf(err); code != dp.Unavailable || snapshot.Board.Scope != (dp.Scope{}) {
		t.Fatal("barrier exposed snapshot", err)
	}
	cp, err := p.Checkpoint(ctx, b.Scope)
	if cp != nil {
		_ = cp.Close()
		t.Fatal("barrier exposed checkpoint")
	}
	if code, _ := dp.CodeOf(err); code != dp.Unavailable {
		t.Fatal(err)
	}
	rr, err := p.ReadResource(ctx, b.Scope, resource.Info.SHA256)
	if rr.Reader != nil {
		_ = rr.Reader.Close()
		t.Fatal("barrier exposed reader")
	}
	if code, _ := dp.CodeOf(err); code != dp.Unavailable {
		t.Fatal(err)
	}
	calls := 0
	_, err = p.Update(ctx, b.Scope, dp.Revision{Scope: b.Scope}, a, func(dp.Snapshot) (dp.ChangeSet, error) { calls++; return dp.ChangeSet{}, nil })
	if code, _ := dp.CodeOf(err); code != dp.Unavailable || calls != 0 {
		t.Fatal("barrier invoked callback", err)
	}
	after, err := fingerprintTree(c.root)
	if err != nil || after != evidence {
		t.Fatal("refusal changed evidence", err)
	}
	p.hook = nil
	var canonical []byte
	for n := 0; n < 2; n++ {
		recovered, _, err := i.Restart(ctx)
		if err != nil {
			t.Fatal(err)
		}
		cp, err := recovered.Checkpoint(ctx, b.Scope)
		if err != nil {
			t.Fatal(err)
		}
		snapshot := cp.Snapshot()
		events := cp.Events()
		if snapshot.Revision.Counter != 1 || len(snapshot.Elements) != 1 || snapshot.Elements[0].Notes != "## Literal heading\n" || len(snapshot.Resources) != 1 || len(events) != 1 || events[0].Revision != snapshot.Revision {
			_ = cp.Close()
			t.Fatal("recovered incomplete envelope")
		}
		var dst bytes.Buffer
		if _, err := dp.EncodeCheckpoint(ctx, &dst, cp); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			canonical = append([]byte(nil), dst.Bytes()...)
		} else if !bytes.Equal(canonical, dst.Bytes()) {
			t.Fatal("second recovery changed complete envelope")
		}
	}
}

func TestSameTurnCancellationCannotOpenChild(t *testing.T) {
	ctx, _, i, b, a := fixture(t)
	in := generated('c', 3)
	_, err := i.Provider.Update(ctx, b.Scope, dp.Revision{Scope: b.Scope}, a, func(dp.Snapshot) (dp.ChangeSet, error) {
		return dp.ChangeSet{PutResources: []dp.ResourceInput{in}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 32; n++ {
		parent, cancel := context.WithCancel(ctx)
		cp, err := i.Provider.Checkpoint(parent, b.Scope)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		cancel()
		child, err := cp.OpenResource(in.Info.SHA256)
		if child != nil {
			_ = child.Close()
			_ = cp.Close()
			t.Fatalf("same-turn cancellation opened child at iteration %d", n)
		}
		if !errors.Is(err, context.Canceled) {
			_ = cp.Close()
			t.Fatal("lost cancellation", err)
		}
		_ = cp.Close()
	}
}
