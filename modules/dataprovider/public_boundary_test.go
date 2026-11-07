package dataprovider_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	dp "github.com/relux-works/swarma-board/dataprovider"
)

// Embedding only the frozen public interface forwards its methods unchanged.
// In particular, this external decorator cannot forward package-private hooks.
type publicCheckpoint struct{ dp.Checkpoint }

type boundaryWriter struct {
	bytes.Buffer
	writes int
	after  func(int)
}

func (w *boundaryWriter) Write(p []byte) (int, error) {
	w.writes++
	n, err := w.Buffer.Write(p)
	if w.after != nil {
		w.after(w.writes)
	}
	return n, err
}

func TestCheckpointAdmissionAcrossPublicBoundary(t *testing.T) {
	for _, history := range []bool{false, true} {
		scope := dp.Scope{ProductID: "alpha", BoardID: "work"}
		snap := dp.Snapshot{Board: dp.Board{Scope: scope}, Revision: dp.Revision{Scope: scope}}
		var events []dp.Event
		label, final, fullBytes := "empty", 5, 246
		if history {
			label, final, fullBytes = "history", 6, 537
			snap.Revision.Counter = 1
			events = []dp.Event{{FormatVersion: 1, Scope: scope, Revision: snap.Revision, Actor: dp.Actor{AccountID: "control", RequestID: "history", Operation: "import", At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}}}
		}
		for _, wrapped := range []bool{false, true} {
			route := "direct"
			if wrapped {
				route = "public-decorator"
			}
			for _, phase := range []string{"live", "before", "first-write", "last-write", "release", "deadline-before", "deadline-write", "deadline-release"} {
				t.Run(label+"/"+route+"/"+phase, func(t *testing.T) {
					parent := t.Context()
					admission, cancel := context.WithCancel(parent)
					deadline := strings.HasPrefix(phase, "deadline-")
					if deadline {
						cancel()
						admission, cancel = context.WithTimeout(parent, 25*time.Millisecond)
					}
					defer cancel()
					var releases atomic.Int32
					cp, err := dp.OpenCheckpoint(admission, snap, events, dp.ResourceBacking{Release: func() error {
						releases.Add(1)
						if phase == "release" {
							cancel()
						}
						if phase == "deadline-release" {
							<-admission.Done()
						}
						return nil
					}})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() {
						_ = cp.Close()
						if releases.Load() != 1 {
							t.Errorf("release count=%d", releases.Load())
						}
					})
					var input dp.Checkpoint = cp
					if wrapped {
						input = publicCheckpoint{cp}
					}
					if phase == "before" {
						cancel()
					}
					if phase == "deadline-before" {
						<-admission.Done()
					}
					w := &boundaryWriter{after: func(n int) {
						if phase == "first-write" && n == 1 || phase == "last-write" && n == final {
							cancel()
						}
						// A cooperative write boundary waits for the real retained deadline.
						// Nothing here claims interruption of an arbitrary blocked Writer.
						if phase == "deadline-write" && n == 1 {
							<-admission.Done()
						}
					}}
					n, err := dp.EncodeCheckpoint(parent, w, input)
					t.Logf("bytes=%d writes=%d release=%d admission=%v encoder=%v", n, w.writes, releases.Load(), admission.Err(), err)
					if parent.Err() != nil || n != int64(w.Len()) {
						t.Fatal("live parent or byte accounting changed")
					}
					expectedBytes, expectedWrites := fullBytes, final
					if !wrapped {
						switch phase {
						case "before", "deadline-before":
							expectedBytes, expectedWrites = 0, 0
						case "first-write", "deadline-write":
							expectedBytes, expectedWrites = 12, 1
						}
					}
					if n != int64(expectedBytes) || w.writes != expectedWrites {
						t.Fatalf("output boundary: bytes=%d writes=%d want %d/%d", n, w.writes, expectedBytes, expectedWrites)
					}
					first, second := input.Close(), input.Close()
					if first != second || releases.Load() != 1 {
						t.Fatal("Close outcome unstable or backing released twice")
					}
					if phase == "live" {
						if err != nil || first != nil || !json.Valid(w.Bytes()) {
							t.Fatalf("live output or Close failed: %v / %v", err, first)
						}
						cancel()
						if later := input.Close(); later != nil {
							t.Fatalf("later cancellation changed completed live Close: %v", later)
						}
					} else {
						want := error(context.Canceled)
						if deadline {
							want = context.DeadlineExceeded
						}
						for _, got := range []error{err, first, second} {
							code, ok := dp.CodeOf(got)
							if !ok || code != dp.Unavailable || !errors.Is(got, want) {
								t.Fatalf("public admission outcome lost: %v", got)
							}
							if _, ok := got.(*dp.Error); !ok {
								t.Fatalf("nonclosed error: %T", got)
							}
						}
					}
				})
			}
		}
	}
}

type stagedCloseReader struct {
	io.Reader
	err    error
	closes int
}

func (r *stagedCloseReader) Close() error { r.closes++; return r.err }

func TestStagedSourceCloseIsPrepublication(t *testing.T) {
	const digest = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	cases := []struct {
		name     string
		err      error
		sentinel error
	}{
		{"live", nil, nil},
		{"ordinary", errors.New("private staging diagnostic"), nil},
		{"typed-unknown", dp.NewError(dp.CommitOutcomeUnknown), nil},
		{"wrapped-unknown", fmt.Errorf("private staging diagnostic: %w", dp.NewError(dp.CommitOutcomeUnknown)), nil},
		{"typed-not-found", dp.NewError(dp.NotFound), nil},
		{"wrapped-not-found", fmt.Errorf("private staging diagnostic: %w", dp.NewError(dp.NotFound)), nil},
		{"typed-budget", dp.NewError(dp.PayloadTooLarge), nil},
		{"typed-invalid", dp.NewError(dp.InvalidSnapshot), nil},
		{"canceled", context.Canceled, context.Canceled},
		{"wrapped-canceled", fmt.Errorf("private staging diagnostic: %w", context.Canceled), context.Canceled},
		{"deadline", context.DeadlineExceeded, context.DeadlineExceeded},
		{"wrapped-deadline", fmt.Errorf("private staging diagnostic: %w", context.DeadlineExceeded), context.DeadlineExceeded},
		{"typed-context", dp.ContextError(context.DeadlineExceeded), context.DeadlineExceeded},
	}
	for _, x := range cases {
		t.Run(x.name, func(t *testing.T) {
			r := &stagedCloseReader{Reader: strings.NewReader("abc"), err: x.err}
			in := dp.ResourceInput{Info: dp.ResourceInfo{SHA256: digest, Size: 3}, Reader: strings.NewReader("unused one-shot input")}
			var out bytes.Buffer
			// Encoding into private bytes performs no provider transaction/publication.
			n, err := dp.EncodeChangeSet(t.Context(), &out, dp.ChangeSet{PutResources: []dp.ResourceInput{in}}, func(i int) (io.ReadCloser, error) {
				if i != 0 {
					t.Fatalf("unexpected staging index %d", i)
				}
				return r, nil
			})
			t.Logf("bytes=%d closes=%d encode=%v; no publication", n, r.closes, err)
			if n != int64(out.Len()) || r.closes != 1 {
				t.Fatal("byte accounting or source ownership changed")
			}
			if x.err == nil {
				if err != nil || !json.Valid(out.Bytes()) {
					t.Fatalf("live encoding failed: %v", err)
				}
				return
			}
			if n != 171 {
				t.Fatalf("unexpected partial byte count %d", n)
			}
			want := dp.StorageError
			if x.sentinel != nil {
				want = dp.Unavailable
			}
			code, ok := dp.CodeOf(err)
			if !ok || code != want {
				t.Fatalf("prepublication Close code %s want %s: %v", code, want, err)
			}
			if _, ok := err.(*dp.Error); !ok || strings.Contains(err.Error(), "private staging diagnostic") {
				t.Fatalf("unclosed diagnostic: %v", err)
			}
			if x.sentinel != nil {
				if !errors.Is(err, x.sentinel) {
					t.Fatalf("context sentinel lost: %v", err)
				}
			} else if errors.Unwrap(err) != nil {
				t.Fatal("raw private cause retained")
			}
		})
	}
	// Source Close must not replace a prior destination budget or integrity error.
	for _, kind := range []string{"destination-budget", "short", "long", "digest"} {
		t.Run("prior-"+kind, func(t *testing.T) {
			data := "abc"
			want := dp.InvalidSnapshot
			switch kind {
			case "short":
				data = "ab"
			case "long":
				data = "abcd"
			case "digest":
				data = "abd"
			case "destination-budget":
				want = dp.PayloadTooLarge
			}
			r := &stagedCloseReader{Reader: strings.NewReader(data), err: dp.NewError(dp.CommitOutcomeUnknown)}
			var out bytes.Buffer
			w := &payloadRefusalWriter{}
			var dst io.Writer = &out
			if kind == "destination-budget" {
				// Refuse only the payload chunk so staging ownership has been acquired.
				dst = w
			}
			n, err := dp.EncodeChangeSet(t.Context(), dst, dp.ChangeSet{PutResources: []dp.ResourceInput{{Info: dp.ResourceInfo{SHA256: digest, Size: 3}}}}, func(int) (io.ReadCloser, error) { return r, nil })
			code, ok := dp.CodeOf(err)
			if !ok || code != want || r.closes != 1 {
				t.Fatalf("earlier error lost: bytes=%d closes=%d code=%s err=%v", n, r.closes, code, err)
			}
			actual := out.Len()
			if kind == "destination-budget" {
				actual = w.Len()
			}
			if n != int64(actual) {
				t.Fatal("partial byte accounting changed")
			}
		})
	}
}

// The base64 payload for abc is YWJj; metadata chunks are accepted unchanged.
type payloadRefusalWriter struct{ bytes.Buffer }

func (w *payloadRefusalWriter) Write(p []byte) (int, error) {
	if bytes.Equal(p, []byte("YWJj")) {
		return 0, dp.NewError(dp.PayloadTooLarge)
	}
	return w.Buffer.Write(p)
}
