package providerdb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dp "github.com/relux-works/swarma-board/dataprovider"
)

// This source-only diagnostic adds no product behavior and uses only a new,
// empty disposable scope. It binds the public encoder to the actual DB handle.
func TestCompositionCheckpointFinalization(t *testing.T) {
	parent := context.Background()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := Open(parent, root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	board := dp.Board{Scope: dp.Scope{ProductID: "sample", BoardID: "empty"}, Name: "Empty"}
	if err = db.EnsureEmpty(parent, board); err != nil {
		t.Fatal(err)
	}
	cp, err := db.Checkpoint(parent, board.Scope)
	if err != nil {
		t.Fatal(err)
	}
	n, err := dp.EncodeCheckpoint(parent, io.Discard, cp)
	code, _ := dp.CodeOf(err)
	t.Logf("encoded_bytes=%d caller_context_error=%v code=%s canceled=%t deadline=%t", n, parent.Err(), code, errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded))
	if err != nil {
		t.Fatalf("ordinary live empty checkpoint finalization failed: %v", err)
	}
	if err = cp.Close(); err != nil {
		t.Fatal(err)
	}
	if err = db.Ready(parent); err != nil {
		t.Fatal(err)
	}
}

func admissionFixture(t *testing.T, state string) (*DB, dp.Board, dp.ResourceInfo) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	board := dp.Board{Scope: dp.Scope{ProductID: "sample", BoardID: "admission"}, Name: "Admission"}
	if err = db.EnsureEmpty(t.Context(), board); err != nil {
		t.Fatal(err)
	}
	payload := strings.Repeat("abc", 30000)
	hash := sha256.Sum256([]byte(payload))
	info := dp.ResourceInfo{SHA256: hex.EncodeToString(hash[:]), Size: int64(len(payload))}
	if state != "empty" {
		actor := dp.Actor{AccountID: "sample", RequestID: "seed", Operation: "put_resource", At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
		_, err = db.Update(t.Context(), board.Scope, dp.Revision{Scope: board.Scope}, actor, func(dp.Snapshot) (dp.ChangeSet, error) {
			c := dp.ChangeSet{}
			if state == "resource" {
				c.PutResources = []dp.ResourceInput{{Info: info, Reader: strings.NewReader(payload)}}
			}
			return c, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return db, board, info
}

func assertNoStreamLease(t *testing.T, db *DB) {
	t.Helper()
	db.mu.Lock()
	active := db.active
	db.mu.Unlock()
	if active != 0 {
		t.Fatalf("retained leases: %d", active)
	}
	pins, err := filepath.Glob(filepath.Join(db.root, "pin-*"))
	if err != nil || len(pins) != 0 {
		t.Fatalf("retained pins: %v %v", pins, err)
	}
}

type admissionWriter struct {
	bytes.Buffer
	writes int
	at     int
	action func()
}

func (w *admissionWriter) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	w.writes++
	if w.action != nil && w.writes == w.at {
		w.action()
	}
	return n, err
}

func checkpointBytes(t *testing.T, db *DB, scope dp.Scope) ([]byte, int) {
	t.Helper()
	cp, err := db.Checkpoint(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	var w admissionWriter
	n, err := dp.EncodeCheckpoint(t.Context(), &w, cp)
	if err != nil || n != int64(w.Len()) || n == 0 {
		t.Fatalf("encode: %d %d %v", n, w.Len(), err)
	}
	if err = cp.Close(); err != nil {
		t.Fatal(err)
	}
	return append([]byte(nil), w.Bytes()...), w.writes
}

// Every case uses the actual DB acquisition and public encoder, with an encoder
// caller that stays live while the separate retained admission fails.
func TestCheckpointRetainedAdmission(t *testing.T) {
	for _, state := range []string{"empty", "history", "resource"} {
		t.Run(state, func(t *testing.T) {
			db, board, _ := admissionFixture(t, state)
			expected, writes := checkpointBytes(t, db, board.Scope)
			for _, cause := range []string{"cancel", "deadline"} {
				for _, phase := range []string{"before", "during", "middle", "final-write"} {
					t.Run(cause+"-"+phase, func(t *testing.T) {
						var parent context.Context
						var cancel context.CancelFunc
						sentinel := context.Canceled
						if cause == "deadline" {
							parent, cancel = context.WithTimeout(t.Context(), 100*time.Millisecond)
							sentinel = context.DeadlineExceeded
						} else {
							parent, cancel = context.WithCancel(t.Context())
						}
						defer cancel()
						cp, err := db.Checkpoint(parent, board.Scope)
						if err != nil {
							t.Fatal(err)
						}
						action := func() {
							if cause == "deadline" {
								<-parent.Done()
							} else {
								cancel()
							}
						}
						w := admissionWriter{at: 1, action: action}
						if phase == "before" {
							action()
							w.action = nil
						}
						if phase == "final-write" {
							w.at = writes
						}
						if phase == "middle" {
							w.at = writes / 2
						}
						n, err := dp.EncodeCheckpoint(t.Context(), &w, cp)
						if !errors.Is(err, sentinel) || n != int64(w.Len()) {
							t.Fatalf("lost outcome: n=%d bytes=%d err=%v", n, w.Len(), err)
						}
						if phase == "final-write" && !bytes.Equal(w.Bytes(), expected) {
							t.Fatal("final-write did not encode complete bytes")
						}
						first, second := cp.Close(), cp.Close()
						if first != second || !errors.Is(first, sentinel) {
							t.Fatalf("unstable finalization: %v %v", first, second)
						}
						assertNoStreamLease(t, db)
						got, _ := checkpointBytes(t, db, board.Scope)
						if !bytes.Equal(got, expected) {
							t.Fatal("failed export changed durable envelope")
						}
						t.Logf("n=%d writes=%d retained=%v caller=%v", n, w.writes, sentinel, t.Context().Err())
					})
				}
			}
			root := db.root
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			for n := 0; n < 2; n++ {
				reopened, err := Open(t.Context(), root)
				if err != nil {
					t.Fatal(err)
				}
				got, _ := checkpointBytes(t, reopened, board.Scope)
				if !bytes.Equal(got, expected) {
					t.Fatal("reopen changed durable envelope")
				}
				assertNoStreamLease(t, reopened)
				if err = reopened.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestStreamAdmissionRefusalAndResourceLifetime(t *testing.T) {
	db, board, info := admissionFixture(t, "resource")
	for _, cause := range []string{"cancel", "deadline"} {
		for _, phase := range []string{"before-acquire", "after-acquire"} {
			t.Run(cause+"-"+phase, func(t *testing.T) {
				var parent context.Context
				var cancel context.CancelFunc
				sentinel := context.Canceled
				if cause == "deadline" {
					parent, cancel = context.WithTimeout(t.Context(), 100*time.Millisecond)
					sentinel = context.DeadlineExceeded
				} else {
					parent, cancel = context.WithCancel(t.Context())
				}
				defer cancel()
				action := func() {
					if cause == "deadline" {
						<-parent.Done()
					} else {
						cancel()
					}
				}
				if phase == "before-acquire" {
					action()
					cp, err := db.Checkpoint(parent, board.Scope)
					if cp != nil || !errors.Is(err, sentinel) {
						t.Fatalf("checkpoint refusal: %v %v", cp, err)
					}
					rr, err := db.ReadResource(parent, board.Scope, info.SHA256)
					if rr.Reader != nil || !errors.Is(err, sentinel) {
						t.Fatalf("reader refusal: %v", err)
					}
				} else {
					rr, err := db.ReadResource(parent, board.Scope, info.SHA256)
					if err != nil {
						t.Fatal(err)
					}
					action()
					if _, err = rr.Reader.Read(make([]byte, 1)); !errors.Is(err, sentinel) {
						t.Fatalf("reader lost outcome: %v", err)
					}
					if err = rr.Reader.Close(); err != nil {
						t.Fatal(err)
					}
					if err = rr.Reader.Close(); err != nil {
						t.Fatal(err)
					}
				}
				assertNoStreamLease(t, db)
			})
		}
	}
	rr, err := db.ReadResource(t.Context(), board.Scope, info.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := io.ReadAll(rr.Reader)
	if err != nil || int64(len(payload)) != info.Size {
		t.Fatalf("reader: %d %v", len(payload), err)
	}
	if err = rr.Reader.Close(); err != nil {
		t.Fatal(err)
	}
	assertNoStreamLease(t, db)
}

func TestCheckpointCloseCancelsOwnedChild(t *testing.T) {
	db, board, info := admissionFixture(t, "resource")
	cp, err := db.Checkpoint(t.Context(), board.Scope)
	if err != nil {
		t.Fatal(err)
	}
	child, err := cp.OpenResource(info.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if err = cp.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = child.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("owned child remains live: %v", err)
	}
	if err = child.Close(); err != nil {
		t.Fatal(err)
	}
	if err = cp.Close(); err != nil {
		t.Fatal(err)
	}
	assertNoStreamLease(t, db)
}

func TestCheckpointSameTurnAdmissionCancellation(t *testing.T) {
	db, board, info := admissionFixture(t, "resource")
	for n := 0; n < 32; n++ {
		parent, cancel := context.WithCancel(t.Context())
		cp, err := db.Checkpoint(parent, board.Scope)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		cancel()
		child, err := cp.OpenResource(info.SHA256)
		if child != nil {
			child.Close()
			cp.Close()
			t.Fatal("canceled admission opened child")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if err = cp.Close(); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		assertNoStreamLease(t, db)
	}
}

// Exercise a cancellation occurring inside finalization of real pinned DB
// backing. This wraps only the backing release seam, never the checkpoint's
// public/private method set, and leaves common unchanged.
func TestCheckpointAdmissionDuringBackingRelease(t *testing.T) {
	for _, cause := range []string{"live", "cancel", "deadline"} {
		t.Run(cause, func(t *testing.T) {
			db, board, _ := admissionFixture(t, "resource")
			var parent context.Context
			var cancel context.CancelFunc
			if cause == "deadline" {
				parent, cancel = context.WithTimeout(t.Context(), 100*time.Millisecond)
			} else {
				parent, cancel = context.WithCancel(t.Context())
			}
			defer cancel()
			admission, finish := admitStream(parent, dp.CheckpointTimeout)
			defer finish()
			if err := db.admit(); err != nil {
				t.Fatal(err)
			}
			snapshot, events, backing, err := db.pin(admission, board.Scope, "")
			if err != nil {
				db.release()
				t.Fatal(err)
			}
			var releases atomic.Int32
			release := backing.Release
			backing.Release = func() error {
				defer finish()
				releases.Add(1)
				err := release()
				if cause == "cancel" {
					cancel()
				}
				if cause == "deadline" {
					<-parent.Done()
				}
				return err
			}
			cp, err := dp.OpenCheckpoint(admission, snapshot, events, backing)
			if err != nil {
				t.Fatal(err)
			}
			var dst bytes.Buffer
			n, err := dp.EncodeCheckpoint(t.Context(), &dst, cp)
			sentinel := parent.Err()
			if cause == "live" && err != nil || cause != "live" && !errors.Is(err, sentinel) {
				t.Fatalf("finalization: %v parent=%v", err, sentinel)
			}
			if n == 0 || n != int64(dst.Len()) {
				t.Fatal("missing complete export")
			}
			first, second := cp.Close(), cp.Close()
			if first != second || cause == "live" && first != nil || cause != "live" && !errors.Is(first, sentinel) {
				t.Fatalf("unstable Close: %v %v", first, second)
			}
			if releases.Load() != 1 {
				t.Fatalf("release count: %d", releases.Load())
			}
			assertNoStreamLease(t, db)
		})
	}
}

func TestPinnedBackingReleaseExactlyOnce(t *testing.T) {
	db, board, _ := admissionFixture(t, "resource")
	if err := db.admit(); err != nil {
		t.Fatal(err)
	}
	_, _, backing, err := db.pin(t.Context(), board.Scope, "")
	if err != nil {
		db.release()
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for n := 0; n < 32; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := backing.Release(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	assertNoStreamLease(t, db)
}

func TestCheckpointConcurrentClose(t *testing.T) {
	db, board, info := admissionFixture(t, "resource")
	for n := 0; n < 16; n++ {
		parent, cancel := context.WithCancel(t.Context())
		cp, err := db.Checkpoint(parent, board.Scope)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		child, err := cp.OpenResource(info.SHA256)
		if err != nil {
			cancel()
			cp.Close()
			t.Fatal(err)
		}
		start := make(chan struct{})
		results := make(chan error, 2)
		var wg sync.WaitGroup
		for j := 0; j < 2; j++ {
			wg.Add(1)
			go func() { defer wg.Done(); <-start; results <- cp.Close() }()
		}
		wg.Add(2)
		go func() { defer wg.Done(); <-start; cancel() }()
		go func() { defer wg.Done(); <-start; _, _ = child.Read(make([]byte, 1024)) }()
		close(start)
		wg.Wait()
		first, second := <-results, <-results
		if first != second || first != nil && !errors.Is(first, context.Canceled) {
			t.Fatalf("unstable concurrent Close: %v %v", first, second)
		}
		if _, err := child.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
			t.Fatalf("closed child: %v", err)
		}
		if err := child.Close(); err != nil {
			t.Fatal(err)
		}
		assertNoStreamLease(t, db)
	}
}

func TestStreamAdmissionCleanupAndBudget(t *testing.T) {
	for _, cause := range []string{"live", "cancel", "deadline"} {
		t.Run(cause, func(t *testing.T) {
			parent, cancel := context.WithCancel(t.Context())
			defer cancel()
			cap := time.Hour
			if cause == "deadline" {
				cap = 20 * time.Millisecond
			}
			ctx, finish := admitStream(parent, cap)
			defer finish()
			child, childCancel := context.WithCancel(ctx)
			defer childCancel()
			if cause == "cancel" {
				cancel()
			}
			if cause != "live" {
				select {
				case <-child.Done():
				case <-time.After(time.Second):
					t.Fatal("owned child not canceled")
				}
			}
			finish()
			finish()
			if cause == "live" {
				if ctx.Err() != nil {
					t.Fatal("cleanup invented admission cancellation")
				}
				select {
				case <-ctx.Done():
					t.Fatal("cleanup closed admission")
				default:
				}
				cancel()
				if !errors.Is(ctx.Err(), context.Canceled) {
					t.Fatal("cleanup hid original caller")
				}
			} else if cause == "cancel" && !errors.Is(ctx.Err(), context.Canceled) || cause == "deadline" && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				t.Fatal(ctx.Err())
			}
		})
	}
	parent, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	start := time.Now()
	ctx, finish := admitStream(parent, dp.CheckpointTimeout)
	defer finish()
	want, _ := parent.Deadline()
	got, _ := ctx.Deadline()
	if got != want {
		t.Fatal("extended original caller deadline")
	}
	ctx2, finish2 := admitStream(t.Context(), dp.ReadTimeout)
	defer finish2()
	got, _ = ctx2.Deadline()
	if got.Before(start.Add(dp.ReadTimeout)) || got.After(time.Now().Add(dp.ReadTimeout)) {
		t.Fatal("read cap changed")
	}
}

// These are real protocol lifetimes, not shortened timers. Independent private
// fixtures run in parallel so the original missing/long caps need one elapsed
// checkpoint lifetime rather than serial repetitions of the same wait.
func TestStreamAdmissionActualCaps(t *testing.T) {
	for _, checkpoint := range []bool{false, true} {
		for _, caller := range []string{"missing", "long"} {
			name := "resource-" + caller
			cap := dp.ReadTimeout
			if checkpoint {
				name = "checkpoint-" + caller
				cap = dp.CheckpointTimeout
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				db, board, info := admissionFixture(t, "resource")
				expected, _ := checkpointBytes(t, db, board.Scope)
				parent := t.Context()
				if caller == "long" {
					var cancel context.CancelFunc
					parent, cancel = context.WithTimeout(parent, 2*cap)
					defer cancel()
				}
				started := time.Now()
				var cp dp.Checkpoint
				var reader io.ReadCloser
				var err error
				if checkpoint {
					cp, err = db.Checkpoint(parent, board.Scope)
				} else {
					var rr dp.ResourceReader
					rr, err = db.ReadResource(parent, board.Scope, info.SHA256)
					reader = rr.Reader
				}
				if err != nil {
					t.Fatal(err)
				}
				if cp != nil {
					defer cp.Close()
				}
				if reader != nil {
					defer reader.Close()
				}
				<-time.After(time.Until(started.Add(cap + 50*time.Millisecond)))
				if checkpoint {
					var child io.ReadCloser
					child, err = cp.OpenResource(info.SHA256)
					if child != nil {
						child.Close()
						t.Fatal("expired checkpoint opened child")
					}
				} else {
					_, err = reader.Read(make([]byte, 1))
				}
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("actual cap lost: %v", err)
				}
				// Expiry must release unclosed handles; Close is tested afterward.
				closed := make(chan error, 1)
				go func() { closed <- db.Close() }()
				select {
				case err := <-closed:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(dp.CloseTimeout + time.Second):
					t.Fatal("expired handle retained lease")
				}
				if cp != nil {
					first, second := cp.Close(), cp.Close()
					if first != second || !errors.Is(first, context.DeadlineExceeded) {
						t.Fatalf("expiry Close: %v %v", first, second)
					}
				} else if err = reader.Close(); err != nil {
					t.Fatal(err)
				}
				assertNoStreamLease(t, db)
				t.Logf("actual lifetime elapsed=%s cap=%s caller=%s", time.Since(started), cap, caller)
				for n := 0; n < 2; n++ {
					reopened, err := Open(t.Context(), db.root)
					if err != nil {
						t.Fatal(err)
					}
					got, _ := checkpointBytes(t, reopened, board.Scope)
					if !bytes.Equal(got, expected) {
						t.Fatal("expiry changed durable envelope")
					}
					if err = reopened.Close(); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}
