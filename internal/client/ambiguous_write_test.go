package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/relux-works/swarma-board/internal/contract"
)

// A service may report expiry after a decision. Exercise a delivered error
// through the real handler with the client and parent operation still live.
// This seam does not claim a durable commit.
func TestClientServerDeliveredWriteExpiry(t *testing.T) {
	for _, kind := range []string{"mutation", "import"} {
		t.Run(kind, func(t *testing.T) {
			var parentLive atomic.Bool
			c, attempts := deadlineComposition(t, func(ctx context.Context, phase string) error {
				if phase != "set_notes" && phase != "import" {
					return nil
				}
				child, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
				defer cancel()
				<-child.Done()
				parentLive.Store(ctx.Err() == nil)
				return child.Err()
			})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			started := time.Now()
			err := ambiguousWriteCall(ctx, c, kind)
			e, ok := err.(*Error)
			if !ok || e.Code != "unavailable" || e.Status != 503 || ExitCode(err) != 1 || !strings.Contains(e.Message, "outcome is unknown") || !strings.Contains(e.Message, "read and reconcile") {
				t.Fatalf("delivered expiry lost typed uncertainty: %#v", err)
			}
			if !parentLive.Load() || ctx.Err() != nil || attempts.Load() != 1 {
				t.Fatalf("expiry must precede caller cancellation with one admission: parentLive=%v caller=%v attempts=%d", parentLive.Load(), ctx.Err(), attempts.Load())
			}
			t.Logf("elapsed=%s code=%s status=%d admissions=%d caller_live=true parent_live=true", time.Since(started), e.Code, e.Status, attempts.Load())
		})
	}
}

func ambiguousWriteCall(ctx context.Context, c *Client, kind string) error {
	rev := contract.Revision(1)
	if kind == "mutation" {
		_, err := c.Query(ctx, "set_notes()", true, &rev)
		return err
	}
	data := []byte("synthetic bounded archive")
	digest := sha256.Sum256(data)
	_, err := c.Import(ctx, data, ImportOptions{SHA256: hex.EncodeToString(digest[:]), IfMatch: &rev})
	return err
}

func TestClientServerWriteRefusal(t *testing.T) {
	for _, kind := range []string{"mutation", "import"} {
		t.Run(kind, func(t *testing.T) {
			c, attempts := deadlineComposition(t, func(_ context.Context, phase string) error {
				if phase == "set_notes" || phase == "import" {
					return &contract.Error{Code: "revision_conflict", Details: map[string]any{"current_revision": uint64(7)}}
				}
				return nil
			})
			err := ambiguousWriteCall(context.Background(), c, kind)
			e, ok := err.(*Error)
			if !ok || e.Code != "revision_conflict" || e.Status != 412 || ExitCode(err) != 4 || e.Message != safeMessage(e.Code) || e.Details["current_revision"] != uint64(7) || attempts.Load() != 1 {
				t.Fatalf("unambiguous refusal changed: %#v admissions=%d", err, attempts.Load())
			}
		})
	}
}

func TestDeliveredErrorClassification(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		code      string
		exit      int
		ambiguous bool
	}{
		{"unavailable", 503, "unavailable", 1, true},
		{"in-band-unavailable", 200, "unavailable", 1, true},
		{"storage", 500, "storage_error", 1, true},
		{"request-timeout", 408, "", 1, true},
		{"gateway-timeout", 504, "", 1, true},
		{"unauthorized", 401, "unauthorized", 3, false},
		{"forbidden", 403, "forbidden", 3, false},
		{"revision-conflict", 412, "revision_conflict", 4, false},
		{"invalid-snapshot", 422, "invalid_snapshot", 1, false},
	} {
		for _, write := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/read", true: "/write"}[write], func(t *testing.T) {
				var attempts atomic.Int32
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if preflight(w, r) {
						return
					}
					attempts.Add(1)
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("X-Request-ID", "synthetic-expiry")
					w.WriteHeader(tc.status)
					_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": tc.code, "message": "untrusted text", "details": map[string]any{"current_revision": 7, "private": "untrusted text"}}})
				}))
				defer s.Close()
				c := newClient(t, s, nil)
				rev := contract.Revision(1)
				_, err := c.Query(context.Background(), "count()", write, &rev)
				e, ok := err.(*Error)
				wantCode := tc.code
				if wantCode == "" {
					wantCode = statusCode(tc.status)
				}
				if !ok || e.Code != wantCode || e.Status != tc.status || e.RequestID != "synthetic-expiry" || ExitCode(err) != tc.exit || e.Local || attempts.Load() != 1 {
					t.Fatalf("typed error/one-attempt behavior changed: %#v attempts=%d", err, attempts.Load())
				}
				wantMessage := safeMessage(wantCode)
				if write && tc.ambiguous {
					wantMessage = "write outcome is unknown; read and reconcile before another write"
				}
				if e.Message != wantMessage || e.Details["current_revision"] != uint64(7) || len(e.Details) != 1 {
					t.Fatalf("message or safe detail boundary changed: %#v", e)
				}
			})
		}
	}
}
