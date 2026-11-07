package dataprovider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type lifetimeWriter struct {
	bytes.Buffer
	writes int
	after  func(int)
}

func (w *lifetimeWriter) Write(p []byte) (int, error) {
	w.writes++
	n, err := w.Buffer.Write(p)
	if w.after != nil {
		w.after(w.writes)
	}
	return n, err
}

func TestCheckpointRetainedAdmissionWithLiveEncoder(t *testing.T) {
	scope := Scope{ProductID: "alpha", BoardID: "work"}
	for _, history := range []bool{false, true} {
		name := "empty"
		final := 5
		snap := Snapshot{Board: Board{Scope: scope}, Revision: Revision{Scope: scope}}
		var events []Event
		if history {
			name = "history"
			final = 6
			snap.Revision.Counter = 1
			events = []Event{{FormatVersion: 1, Scope: scope, Revision: snap.Revision, Actor: Actor{AccountID: "control", RequestID: "history", Operation: "import", At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}}}
		}
		for _, phase := range []string{"live", "before", "first-write", "last-write", "release", "deadline"} {
			t.Run(name+"/"+phase, func(t *testing.T) {
				// The encoder's parent remains live throughout every control. Only
				// the provider-retained admission ends; all inputs are tiny metadata.
				parent := t.Context()
				admission, cancel := context.WithCancel(parent)
				if phase == "deadline" {
					cancel()
					admission, cancel = context.WithTimeout(parent, 100*time.Millisecond)
				}
				defer cancel()
				var releases atomic.Int32
				cp, err := OpenCheckpoint(admission, snap, events, ResourceBacking{Release: func() error {
					releases.Add(1)
					if phase == "release" {
						cancel()
					}
					return nil
				}})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					closed := cp.Close()
					if phase == "live" && closed != nil || phase != "live" && !errors.Is(closed, admission.Err()) {
						t.Errorf("cleanup lost admission outcome: %v", closed)
					}
				})
				if phase == "before" {
					cancel()
				}
				w := &lifetimeWriter{after: func(n int) {
					if phase == "first-write" && n == 1 || phase == "last-write" && n == final {
						cancel()
					}
					// This bounded, cooperative writer waits for the real short deadline.
					// It is not a claim of interrupting an arbitrary blocked Write call.
					if phase == "deadline" && n == 1 {
						<-admission.Done()
					}
				}}
				n, err := EncodeCheckpoint(parent, w, cp)
				t.Logf("bytes=%d writes=%d admission=%v parent=%v encoder=%v", n, w.writes, admission.Err(), parent.Err(), err)
				if parent.Err() != nil || n != int64(w.Len()) {
					t.Fatal("parent or byte accounting changed")
				}
				if phase == "live" {
					if err != nil || w.writes != final {
						t.Fatalf("live encoding failed: %v", err)
					}
				} else {
					want := context.Canceled
					if phase == "deadline" {
						want = context.DeadlineExceeded
					}
					if !errors.Is(err, want) {
						t.Fatalf("retained admission lost: %v", err)
					}
					if _, ok := err.(*Error); !ok {
						t.Fatalf("non-closed context error: %T", err)
					}
					if phase == "before" && w.writes != 0 || (phase == "first-write" || phase == "deadline") && w.writes != 1 || (phase == "last-write" || phase == "release") && w.writes != final {
						t.Fatal("output continued past failed write boundary")
					}
				}
				if releases.Load() != 1 {
					t.Fatalf("backing releases=%d", releases.Load())
				}
				// The public outcome stays stable, including after cancellation.
				// Live Close must never turn its own cleanup into admission failure.
				first, second := cp.Close(), cp.Close()
				if first != second || phase == "live" && first != nil || phase != "live" && !errors.Is(first, admission.Err()) {
					t.Fatalf("unstable or incorrect Close outcome: %v / %v", first, second)
				}
				if releases.Load() != 1 {
					t.Fatal("backing released more than once")
				}
			})
		}
	}
}

type inputFailureReader struct {
	data string
	err  error
}

type cancellationCloseReader struct {
	io.Reader
	ctx context.Context
}

func (r cancellationCloseReader) Close() error {
	if r.ctx.Err() == nil {
		return errors.New("child operation was not canceled before Close")
	}
	return nil
}

func TestCheckpointCloseCancelsChildBeforeBackingFinalization(t *testing.T) {
	snapshot, events := ownedFixture()
	var cp Checkpoint
	opens, releases := 0, 0
	var err error
	cp, err = OpenCheckpoint(t.Context(), snapshot, events, ResourceBacking{
		Open: func(string) (io.ReadCloser, error) {
			opens++
			if opens == 1 {
				return io.NopCloser(strings.NewReader("abc")), nil
			}
			return cancellationCloseReader{Reader: strings.NewReader("abc"), ctx: cp.(*ownedCheckpoint).ctx}, nil
		},
		Release: func() error { releases++; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cp.Close() })
	child, err := cp.OpenResource(abcSHA)
	if err != nil {
		t.Fatal(err)
	}
	if err := cp.Close(); err != nil {
		t.Fatal(err)
	}
	if err := child.Close(); err != nil {
		t.Fatal(err)
	}
	if releases != 1 {
		t.Fatalf("releases=%d", releases)
	}
}

func (r *inputFailureReader) Read(p []byte) (int, error) {
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, r.err
}

func TestPreparedReaderFailuresAreDefiniteAndClosed(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		want     Code
		sentinel error
	}{
		{"ordinary", errors.New("private input diagnostic"), StorageError, nil},
		{"unknown", NewError(CommitOutcomeUnknown), StorageError, nil},
		{"not-found", NewError(NotFound), StorageError, nil},
		{"wrapped-unknown", fmt.Errorf("private input diagnostic: %w", NewError(CommitOutcomeUnknown)), StorageError, nil},
		{"wrapped-not-found", fmt.Errorf("private input diagnostic: %w", NewError(NotFound)), StorageError, nil},
		{"typed-overflow", NewError(PayloadTooLarge), StorageError, nil},
		{"canceled", context.Canceled, Unavailable, context.Canceled},
		{"wrapped-canceled", fmt.Errorf("private input diagnostic: %w", context.Canceled), Unavailable, context.Canceled},
		{"deadline", context.DeadlineExceeded, Unavailable, context.DeadlineExceeded},
		{"typed-context", ContextError(context.DeadlineExceeded), Unavailable, context.DeadlineExceeded},
	}
	for _, x := range cases {
		for _, data := range []string{"", "a"} {
			t.Run(x.name+fmt.Sprint(len(data)), func(t *testing.T) {
				n, err := CopyResource(t.Context(), io.Discard, ResourceInput{Info: ResourceInfo{SHA256: abcSHA, Size: 3}, Reader: &inputFailureReader{data: data, err: x.err}})
				requireCode(t, err, x.want)
				if _, ok := err.(*Error); !ok || strings.Contains(err.Error(), "private input diagnostic") {
					t.Fatalf("input diagnostic escaped: %v", err)
				}
				if n != int64(len(data)) {
					t.Fatalf("read count=%d", n)
				}
				if x.sentinel != nil && !errors.Is(err, x.sentinel) {
					t.Fatalf("context sentinel lost: %v", err)
				}
				if x.sentinel == nil && errors.Unwrap(err) != nil {
					t.Fatal("raw cause retained")
				}
			})
		}
	}
}

func TestPreparedReaderClassificationPreservesDestinationAndIntegrity(t *testing.T) {
	valid := func() ResourceInput {
		return ResourceInput{Info: ResourceInfo{SHA256: abcSHA, Size: 3}, Reader: strings.NewReader("abc")}
	}
	t.Run("destination-budget", func(t *testing.T) {
		b := &budgetWriter{w: io.Discard, cap: 2}
		_, err := CopyResource(t.Context(), b, valid())
		requireCode(t, err, PayloadTooLarge)
		if b.n != 0 {
			t.Fatal("overflow chunk was written")
		}
	})
	t.Run("encoder-budget", func(t *testing.T) {
		b := &budgetWriter{w: io.Discard, cap: 100}
		err := encodePayload(t.Context(), b, valid().Info, io.NopCloser(strings.NewReader("abc")))
		requireCode(t, err, PayloadTooLarge)
	})
	for _, data := range []string{"ab", "abcd", "abd"} {
		t.Run("integrity-"+data, func(t *testing.T) {
			in := valid()
			in.Reader = strings.NewReader(data)
			_, err := CopyResource(t.Context(), io.Discard, in)
			requireCode(t, err, InvalidSnapshot)
		})
	}
	var out bytes.Buffer
	n, err := CopyResource(t.Context(), &out, valid())
	if err != nil || n != 3 || out.String() != "abc" {
		t.Fatalf("valid input changed: %d %v", n, err)
	}
}
