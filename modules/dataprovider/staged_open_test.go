package dataprovider_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	dp "github.com/relux-works/swarma-board/dataprovider"
)

type unusedStagedInput struct{}

func (unusedStagedInput) Read([]byte) (int, error) { panic("one-shot input reused by staging encoder") }

func stagedOpenFixture(t *testing.T) dp.ChangeSet {
	t.Helper()
	c := dp.ChangeSet{PutResources: []dp.ResourceInput{{
		Info:   dp.ResourceInfo{SHA256: "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", Size: 3},
		Reader: unusedStagedInput{},
	}}}
	if err := dp.ValidateChangeSet(c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestStagedSourceOpenIsPrepublication(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		sentinel error
	}{
		{"ordinary", errors.New("private acquisition diagnostic"), nil},
		{"typed-unknown", dp.NewError(dp.CommitOutcomeUnknown), nil},
		{"typed-not-found", dp.NewError(dp.NotFound), nil},
		{"typed-budget", dp.NewError(dp.PayloadTooLarge), nil},
		{"typed-invalid", dp.NewError(dp.InvalidSnapshot), nil},
		{"typed-unavailable", dp.NewError(dp.Unavailable), nil},
		{"canceled", context.Canceled, context.Canceled},
		{"deadline", context.DeadlineExceeded, context.DeadlineExceeded},
		{"typed-context", dp.ContextError(context.Canceled), context.Canceled},
	}
	for _, x := range cases {
		for _, wrapped := range []bool{false, true} {
			for _, partial := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/wrapped=%v/handle=%v", x.name, wrapped, partial), func(t *testing.T) {
					original := x.err
					if wrapped {
						original = fmt.Errorf("private acquisition diagnostic: %w", original)
					}
					// A contradictory cleanup error must never replace acquisition.
					cleanupErr := error(context.Canceled)
					if x.sentinel != nil {
						cleanupErr = dp.NewError(dp.CommitOutcomeUnknown)
					}
					r := &stagedCloseReader{Reader: strings.NewReader("abc"), err: cleanupErr}
					calls := 0
					var out bytes.Buffer
					n, err := dp.EncodeChangeSet(t.Context(), &out, stagedOpenFixture(t), func(i int) (io.ReadCloser, error) {
						calls++
						if i != 0 {
							t.Fatalf("staging index=%d", i)
						}
						if partial {
							return r, original
						}
						return nil, original
					})
					t.Logf("bytes=%d actual=%d opens=%d closes=%d error=%v; no publisher", n, out.Len(), calls, r.closes, err)
					if n != 62 || n != int64(out.Len()) || calls != 1 {
						t.Fatal("partial accounting/acquisition changed")
					}
					wantCloses := 0
					if partial {
						wantCloses = 1
					}
					if r.closes != wantCloses {
						t.Fatal("returned staging handle not closed exactly once")
					}
					want := dp.StorageError
					if x.sentinel != nil {
						want = dp.Unavailable
					}
					code, ok := dp.CodeOf(err)
					if !ok || code != want {
						t.Fatalf("acquisition code=%s want=%s: %v", code, want, err)
					}
					if _, ok := err.(*dp.Error); !ok || strings.Contains(err.Error(), "private acquisition diagnostic") {
						t.Fatalf("nonclosed error: %v", err)
					}
					if x.sentinel != nil {
						if !errors.Is(err, x.sentinel) {
							t.Fatalf("original context lost to cleanup: %v", err)
						}
					} else if errors.Unwrap(err) != nil {
						t.Fatal("cleanup context or private cause replaced acquisition")
					}
				})
			}
		}
	}
}

func TestStagedSourceOpenPositiveAndNilControls(t *testing.T) {
	for _, nilHandle := range []bool{false, true} {
		t.Run(fmt.Sprintf("nil-handle=%v", nilHandle), func(t *testing.T) {
			var out bytes.Buffer
			calls := 0
			r := &stagedCloseReader{Reader: strings.NewReader("abc")}
			n, err := dp.EncodeChangeSet(t.Context(), &out, stagedOpenFixture(t), func(int) (io.ReadCloser, error) {
				calls++
				if nilHandle {
					return nil, nil
				}
				return r, nil
			})
			if n != int64(out.Len()) || calls != 1 {
				t.Fatal("byte accounting/acquisition changed")
			}
			if nilHandle {
				code, ok := dp.CodeOf(err)
				if !ok || code != dp.StorageError || n != 62 || r.closes != 0 {
					t.Fatalf("nil acquisition: n=%d closes=%d err=%v", n, r.closes, err)
				}
			} else if err != nil || !json.Valid(out.Bytes()) || !bytes.Contains(out.Bytes(), []byte(`"data":"YWJj"`)) || r.closes != 1 {
				t.Fatalf("valid private encoding: n=%d closes=%d err=%v", n, r.closes, err)
			}
			t.Logf("nil=%v bytes=%d closes=%d err=%v", nilHandle, n, r.closes, err)
		})
	}
	for _, phase := range []string{"canceled-before-open", "destination-before-open"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var out bytes.Buffer
			var dst io.Writer = &out
			if phase == "canceled-before-open" {
				cancel()
			} else {
				dst = stagedDestinationRefusal{}
			}
			calls := 0
			n, err := dp.EncodeChangeSet(ctx, dst, stagedOpenFixture(t), func(int) (io.ReadCloser, error) { calls++; return nil, dp.NewError(dp.CommitOutcomeUnknown) })
			if n != 0 || calls != 0 {
				t.Fatalf("opened staging after earlier refusal: bytes=%d calls=%d", n, calls)
			}
			want := dp.PayloadTooLarge
			if phase == "canceled-before-open" {
				want = dp.Unavailable
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			}
			code, ok := dp.CodeOf(err)
			if !ok || code != want {
				t.Fatalf("earlier refusal replaced: %v", err)
			}
		})
	}
}

type stagedDestinationRefusal struct{}

func (stagedDestinationRefusal) Write([]byte) (int, error) { return 0, dp.NewError(dp.PayloadTooLarge) }
