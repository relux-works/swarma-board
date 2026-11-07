package providerdb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	dp "github.com/relux-works/swarma-board/dataprovider"
)

func provisionActor(request string) dp.Actor {
	return dp.Actor{AccountID: "sample", RequestID: request, Operation: "set_notes", At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

// Keep facts, payload membership/bytes and an event in the existing scope so a
// provisioning failure cannot pass by preserving only empty board metadata.
func provisionFixture(t *testing.T) (*DB, dp.Board) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	d, err := Open(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	board := dp.Board{Scope: dp.Scope{ProductID: "sample", BoardID: "shared"}, Name: "Existing"}
	if err = d.EnsureEmpty(t.Context(), board); err != nil {
		t.Fatal(err)
	}
	data := []byte("retained facts")
	digest := sha256.Sum256(data)
	info := dp.ResourceInfo{SHA256: hex.EncodeToString(digest[:]), Size: int64(len(data))}
	_, err = d.Update(t.Context(), board.Scope, dp.Revision{Scope: board.Scope}, provisionActor("seed"), func(dp.Snapshot) (dp.ChangeSet, error) {
		return dp.ChangeSet{
			Provenance:   []dp.ProvenanceFile{{Path: "provenance/source.txt", SHA256: info.SHA256, Data: data}},
			PutResources: []dp.ResourceInput{{Info: info, Reader: bytes.NewReader(data)}},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return d, board
}

func provisionCode(t *testing.T, err error, want dp.Code) {
	t.Helper()
	if code, ok := dp.CodeOf(err); !ok || code != want {
		t.Fatalf("error = %v, want %s", err, want)
	}
}

func waitProvisionWriters(t *testing.T, d *DB, count int) {
	t.Helper()
	until := time.Now().Add(time.Second)
	for time.Now().Before(until) {
		d.mu.Lock()
		active := d.active
		d.mu.Unlock()
		if active == count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("queued writer was not admitted within one second")
}

func provisionResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("writer did not finish within three seconds")
		return nil
	}
}

func queueProvisionOperation(ctx context.Context, d *DB, board dp.Board, operation string, callback *bool) <-chan error {
	result := make(chan error, 1)
	go func() {
		if operation == "update-control" {
			_, err := d.Update(ctx, board.Scope, dp.Revision{Scope: board.Scope, Counter: 1}, provisionActor("queued"), func(dp.Snapshot) (dp.ChangeSet, error) {
				*callback = true
				return dp.ChangeSet{}, nil
			})
			result <- err
			return
		}
		if operation == "absent-provision" {
			board.Scope.BoardID = "queued"
		}
		result <- d.EnsureEmpty(ctx, board)
	}()
	return result
}

// A separate real engine writer is held after Update's successful Commit. The
// queued adapter call must return unavailable without attempting even Begin,
// including the identical-metadata no-op, which would otherwise block here.
func TestQueuedProvisioningRecoveryBarrier(t *testing.T) {
	for _, operation := range []string{"identical-provision", "absent-provision", "update-control"} {
		t.Run(operation, func(t *testing.T) {
			d, board := provisionFixture(t)
			reached, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unpause := func() { once.Do(func() { close(release) }) }
			defer unpause()
			d.hook = func(_ context.Context, point string) error {
				if point == "after-commit" {
					close(reached)
					<-release
					return dp.NewError(dp.StorageError)
				}
				return nil
			}
			first := make(chan error, 1)
			go func() {
				_, err := d.Update(t.Context(), board.Scope, dp.Revision{Scope: board.Scope, Counter: 1}, provisionActor("first"), func(dp.Snapshot) (dp.ChangeSet, error) { return dp.ChangeSet{}, nil })
				first <- err
			}()
			select {
			case <-reached:
			case <-time.After(3 * time.Second):
				t.Fatal("first writer did not reach acknowledgement point")
			}
			blocker, err := d.db.Bolt.Begin(true)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback()
			callback := false
			queued := queueProvisionOperation(t.Context(), d, board, operation, &callback)
			waitProvisionWriters(t, d, 2)
			unpause()
			provisionCode(t, provisionResult(t, first), dp.CommitOutcomeUnknown)
			select {
			case err = <-queued:
				provisionCode(t, err, dp.Unavailable)
			case <-time.After(time.Second):
				// Release the engine lock and drain before reporting the failure.
				_ = blocker.Rollback()
				_ = provisionResult(t, queued)
				t.Fatal("queued operation attempted an engine transaction past the latch")
			}
			if callback {
				t.Fatal("queued Update invoked its callback")
			}
			provisionCode(t, d.Ready(t.Context()), dp.Unavailable)
			if err = blocker.Rollback(); err != nil {
				t.Fatal(err)
			}
			if err = d.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(t.Context(), d.root)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			s, err := reopened.View(t.Context(), board.Scope)
			if err != nil || s.Revision.Counter != 2 {
				t.Fatalf("committed writer was not recovered: %+v %v", s.Revision, err)
			}
			absent := board.Scope
			absent.BoardID = "queued"
			_, err = reopened.View(t.Context(), absent)
			provisionCode(t, err, dp.NotFound)
			t.Logf("queued=%s callback=%t refused before engine Begin; recovered first revision=%d", operation, callback, s.Revision.Counter)
		})
	}
}

// Instrument only the pinned engine's existing in-memory write callback. No
// adapter/engine source is patched. In lost-ack mode the real metadata page is
// written and synced before EIO is returned. The other mode refuses that write,
// after dirty-page sync. These causal old/new controls test classification, not
// physical storage failure or power loss. The caller restores after all writers
// have returned, so neither slot restoration nor observed state races a writer.
func provisionCommitFault(t *testing.T, d *DB, mode string, reached, release chan struct{}) func() {
	t.Helper()
	field := reflect.ValueOf(d.db.Bolt).Elem().FieldByName("ops").FieldByName("writeAt")
	slot := (*func([]byte, int64) (int, error))(unsafe.Pointer(field.UnsafeAddr()))
	original := *slot
	f, err := os.OpenFile(d.db.Bolt.Path(), os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	page := int64(d.db.Bolt.Info().PageSize)
	fired := false
	*slot = func(b []byte, off int64) (int, error) {
		if off >= 2*page || fired {
			return original(b, off)
		}
		fired = true
		n := 0
		if mode == "lost-ack" {
			var err error
			n, err = original(b, off)
			if err != nil {
				return n, err
			}
			if err = f.Sync(); err != nil {
				return n, err
			}
		}
		close(reached)
		<-release
		return n, syscall.EIO
	}
	return func() {
		*slot = original
		if err := f.Close(); err != nil {
			t.Error(err)
		}
		if !fired {
			t.Error("engine never attempted the metadata write")
		}
	}
}

func retainProvisionFile(t *testing.T, root, label string) {
	t.Helper()
	dir := os.Getenv("PROVISION_EVIDENCE")
	if dir == "" {
		return
	}
	if !filepath.IsAbs(dir) {
		t.Fatal("evidence directory must be absolute")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "provider.db")
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() > 2<<20 {
		t.Fatalf("invalid synthetic evidence file: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, label+".db"), data, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	t.Logf("retained %s.db bytes=%d sha256=%x", label, len(data), sum)
}

func TestProvisioningCommitAmbiguity(t *testing.T) {
	for _, firstOperation := range []string{"provision", "update-control"} {
		t.Run(firstOperation, func(t *testing.T) {
			// A successful independent fixture supplies the exact new checkpoint,
			// including contiguous history and all resource bytes, for comparison.
			healthy, board := provisionFixture(t)
			oldBytes, _ := checkpointBytes(t, healthy, board.Scope)
			target := board
			target.Scope.ProductID = "other" // same BoardID, different full Scope
			target.Name = "New"
			apply := func(d *DB) error {
				if firstOperation == "provision" {
					return d.EnsureEmpty(t.Context(), target)
				}
				_, err := d.Update(t.Context(), board.Scope, dp.Revision{Scope: board.Scope, Counter: 1}, provisionActor("first"), func(dp.Snapshot) (dp.ChangeSet, error) { return dp.ChangeSet{}, nil })
				return err
			}
			if err := apply(healthy); err != nil {
				t.Fatal(err)
			}
			newScope := target.Scope
			if firstOperation == "update-control" {
				newScope = board.Scope
			}
			newBytes, _ := checkpointBytes(t, healthy, newScope)
			for _, mode := range []string{"before-metadata", "lost-ack"} {
				for _, queuedOperation := range []string{"identical-provision", "absent-provision", "update-control"} {
					t.Run(mode+"/"+queuedOperation, func(t *testing.T) {
						d, _ := provisionFixture(t)
						before, _ := checkpointBytes(t, d, board.Scope)
						if !bytes.Equal(before, oldBytes) {
							t.Fatal("control fixture did not start at the causal old state")
						}
						_, err := d.View(t.Context(), target.Scope)
						provisionCode(t, err, dp.NotFound)
						retain := firstOperation == "provision" && queuedOperation == "absent-provision"
						if retain {
							retainProvisionFile(t, d.root, mode+"-before")
						}
						reached, release := make(chan struct{}), make(chan struct{})
						var once sync.Once
						unpause := func() { once.Do(func() { close(release) }) }
						defer unpause()
						restore := provisionCommitFault(t, d, mode, reached, release)
						first := make(chan error, 1)
						go func() { first <- apply(d) }()
						select {
						case <-reached:
						case <-time.After(3 * time.Second):
							t.Fatal("first writer did not reach metadata fault")
						}
						callback := false
						queued := queueProvisionOperation(t.Context(), d, board, queuedOperation, &callback)
						waitProvisionWriters(t, d, 2)
						unpause()
						firstErr, queuedErr := provisionResult(t, first), provisionResult(t, queued)
						restore()
						provisionCode(t, firstErr, dp.CommitOutcomeUnknown)
						provisionCode(t, queuedErr, dp.Unavailable)
						if callback {
							t.Fatal("queued Update invoked its callback")
						}
						provisionCode(t, d.Ready(t.Context()), dp.Unavailable)
						_, err = d.View(t.Context(), board.Scope)
						provisionCode(t, err, dp.Unavailable)
						if err = d.Close(); err != nil {
							t.Fatal(err)
						}
						if retain {
							retainProvisionFile(t, d.root, mode+"-after")
						}
						for reopen := 0; reopen < 2; reopen++ {
							recovered, err := Open(t.Context(), d.root)
							if err != nil {
								t.Fatal(err)
							}
							want := oldBytes
							if firstOperation == "update-control" && mode == "lost-ack" {
								want = newBytes
							}
							got, _ := checkpointBytes(t, recovered, board.Scope)
							if !bytes.Equal(got, want) {
								t.Error("existing scope's coherent checkpoint differs from the causal control")
							}
							if firstOperation == "provision" && mode == "lost-ack" {
								got, _ = checkpointBytes(t, recovered, target.Scope)
								if !bytes.Equal(got, newBytes) {
									t.Error("durable provision differs from the successful empty-scope control")
								}
							} else {
								_, err = recovered.View(t.Context(), target.Scope)
								provisionCode(t, err, dp.NotFound)
							}
							absent := board.Scope
							absent.BoardID = "queued"
							_, err = recovered.View(t.Context(), absent)
							provisionCode(t, err, dp.NotFound)
							if err = recovered.Ready(t.Context()); err != nil {
								t.Error(err)
							}
							if err = recovered.Close(); err != nil {
								t.Fatal(err)
							}
						}
						t.Logf("first=%s fault=%s queued=%s callback=%t unknown+latch; two coherent reopens match causal control", firstOperation, mode, queuedOperation, callback)
					})
				}
			}
		})
	}
}

func TestProvisioningDefiniteRefusals(t *testing.T) {
	for _, operation := range []string{"identical", "metadata-conflict", "invalid-board", "invalid-scope", "canceled", "callback", "validation", "precommit-storage"} {
		t.Run(operation, func(t *testing.T) {
			d, board := provisionFixture(t)
			before, _ := checkpointBytes(t, d, board.Scope)
			// Every refusal (and the existing-scope no-op) must avoid engine
			// writes altogether, rather than reaching Commit and being relabeled.
			field := reflect.ValueOf(d.db.Bolt).Elem().FieldByName("ops").FieldByName("writeAt")
			slot := (*func([]byte, int64) (int, error))(unsafe.Pointer(field.UnsafeAddr()))
			original, writes := *slot, 0
			*slot = func(b []byte, off int64) (int, error) { writes++; return original(b, off) }
			var err error
			want := dp.Code("")
			callbackError := errors.New("callback refused")
			switch operation {
			case "identical":
				err = d.EnsureEmpty(t.Context(), board)
			case "metadata-conflict":
				board.Name = "Different"
				err = d.EnsureEmpty(t.Context(), board)
				want = dp.ScopeConflict
			case "invalid-board":
				board.Scope.BoardID = "absent"
				board.Name = "Invalid\xff"
				err = d.EnsureEmpty(t.Context(), board)
				want = dp.InvalidRequest
			case "invalid-scope":
				board.Scope.ProductID = ""
				err = d.EnsureEmpty(t.Context(), board)
				want = dp.InvalidRequest
			case "canceled":
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				err = d.EnsureEmpty(ctx, board)
				want = dp.Unavailable
				if !errors.Is(err, context.Canceled) {
					t.Errorf("cancellation sentinel lost: %v", err)
				}
			case "callback", "validation", "precommit-storage":
				d.hook = func(_ context.Context, point string) error {
					if operation == "precommit-storage" && point == "before-commit" {
						return syscall.EIO
					}
					return nil
				}
				errResult := func(dp.Snapshot) (dp.ChangeSet, error) {
					if operation == "callback" {
						return dp.ChangeSet{}, callbackError
					}
					if operation == "validation" {
						return dp.ChangeSet{Provenance: []dp.ProvenanceFile{{Path: "provenance/invalid", SHA256: "bad"}}}, nil
					}
					return dp.ChangeSet{}, nil
				}
				_, err = d.Update(t.Context(), board.Scope, dp.Revision{Scope: board.Scope, Counter: 1}, provisionActor("refused"), errResult)
				if operation == "callback" {
					if err != callbackError {
						t.Errorf("callback error changed: %v", err)
					}
				} else if operation == "validation" {
					want = dp.InvalidSnapshot
				} else {
					want = dp.StorageError
				}
			}
			*slot = original
			if want != "" {
				provisionCode(t, err, want)
			} else if operation == "identical" && err != nil {
				t.Fatal(err)
			}
			if writes != 0 {
				t.Fatalf("precommit refusal/no-op attempted %d engine writes", writes)
			}
			if err = d.Ready(t.Context()); err != nil {
				t.Fatalf("definite refusal latched a usable provider: %v", err)
			}
			originalScope := dp.Scope{ProductID: "sample", BoardID: "shared"}
			after, _ := checkpointBytes(t, d, originalScope)
			if !bytes.Equal(before, after) {
				t.Fatal("definite refusal/no-op changed the existing scope")
			}
			if err = d.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(t.Context(), d.root)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			after, _ = checkpointBytes(t, reopened, originalScope)
			if !bytes.Equal(before, after) {
				t.Fatal("definite refusal/no-op changed durable state")
			}
			absent := originalScope
			absent.BoardID = "absent"
			_, err = reopened.View(t.Context(), absent)
			provisionCode(t, err, dp.NotFound)
			t.Logf("operation=%s engine-writes=%d Ready usable; complete checkpoint unchanged after reopen", operation, writes)
		})
	}
}
