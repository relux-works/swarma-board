package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/relux-works/swarma-board/internal/contract"
)

type observeDeadlineTransport struct {
	next  http.RoundTripper
	check func(*http.Request)
}

func (o observeDeadlineTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	o.check(r)
	return o.next.RoundTrip(r)
}

// Observe deadlines on actual public operations, including import validation
// (which is not a write) and the ordinary CAS revision preflight before a write.
func TestProviderOperationClientDeadlines(t *testing.T) {
	archive := archiveFixture(t)
	digest := archiveDigest(archive)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if preflight(w, r) {
			return
		}
		w.Header().Set("ETag", `"rev-1"`)
		switch {
		case strings.HasSuffix(r.URL.Path, "/export"):
			w.Header().Set("Content-Type", "application/gzip")
			w.Header().Set("X-Snapshot-SHA256", digest)
			_, _ = w.Write(archive)
		case strings.HasSuffix(r.URL.Path, "/import"):
			_, _ = io.WriteString(w, `{"import":{"sha256":"`+digest+`","revision":1,"element_count":2}}`)
		default:
			body, _ := io.ReadAll(r.Body)
			if string(body) == "count()" {
				_, _ = io.WriteString(w, `{"count":0}`)
			} else {
				_, _ = io.WriteString(w, `{"set_notes":{"id":"TASK-a","changed_ids":["TASK-a"]}}`)
			}
		}
	}))
	defer s.Close()
	c := newClient(t, s, nil)
	transport := c.http.Transport
	var budgets []time.Duration
	c.http.Transport = observeDeadlineTransport{next: transport, check: func(r *http.Request) {
		deadline, ok := r.Context().Deadline()
		if !ok {
			t.Fatal("outgoing operation has no deadline")
		}
		budgets = append(budgets, time.Until(deadline))
	}}
	t.Cleanup(func() { c.http.Transport = transport })
	for _, callerBudget := range []time.Duration{0, time.Hour, time.Second} {
		budgets = nil
		ctx := context.Background()
		cancel := func() {}
		if callerBudget != 0 {
			ctx, cancel = context.WithTimeout(ctx, callerBudget)
		}
		_, err := c.Query(ctx, "set_notes(TASK-a,notes=\"changed\")", true, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.Import(ctx, archive, ImportOptions{DryRun: true})
		if err != nil {
			t.Fatal(err)
		}
		rev := contract.Revision(1)
		_, err = c.Import(ctx, archive, ImportOptions{SHA256: digest, IfMatch: &rev})
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = c.Export(ctx)
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		if len(budgets) != 5 {
			t.Fatalf("unexpected requests: %v", budgets)
		}
		for n, budget := range budgets {
			if callerBudget == time.Second {
				if budget <= 0 || budget > time.Second {
					t.Fatalf("caller deadline extended: %v", budgets)
				}
			} else if n == 0 {
				if budget < 30*time.Second || budget > 35*time.Second {
					t.Fatalf("read preflight budget changed: %v", budgets)
				}
			} else if budget < 300*time.Second || budget > 305*time.Second {
				t.Fatalf("provider operation cannot use full 300s: %v", budgets)
			}
		}
	}
}

func TestProviderCallerDeadlineIncludesResponseBody(t *testing.T) {
	for _, afterHeaders := range []bool{false, true} {
		t.Run(map[bool]string{false: "waiting-for-headers", true: "streaming-body"}[afterHeaders], func(t *testing.T) {
			var attempts atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if preflight(w, r) {
					return
				}
				_, _ = io.Copy(io.Discard, r.Body)
				attempts.Add(1)
				if afterHeaders {
					w.Header().Set("ETag", `"rev-1"`)
					_, _ = io.WriteString(w, `{"set_notes":`)
					w.(http.Flusher).Flush()
				}
				select {
				case <-r.Context().Done():
				case <-time.After(3 * time.Second):
					t.Error("caller cancellation did not close the request")
				}
			}))
			defer s.Close()
			c := newClient(t, s, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			rev := contract.Revision(0)
			started := time.Now()
			_, err := c.Query(ctx, "set_notes(TASK-a,notes=\"changed\")", true, &rev)
			if err == nil || !strings.Contains(err.Error(), "outcome is unknown") || ExitCode(err) != 1 {
				t.Fatalf("missing unknown-write-outcome refusal: %v", err)
			}
			if attempts.Load() != 1 || time.Since(started) > 3*time.Second {
				t.Fatalf("caller deadline ignored or write retried: attempts=%d elapsed=%s", attempts.Load(), time.Since(started))
			}
		})
	}
}

func TestProviderResponseHeaderBudgetAcrossTransports(t *testing.T) {
	tcp := newHTTPTransport()
	unix, err := NewUnixTransport("unix:///synthetic/service.sock")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.CloseIdleConnections()
	defer unix.CloseIdleConnections()
	for _, tr := range []*http.Transport{tcp, unix.transport} {
		if tr.ResponseHeaderTimeout < 300*time.Second || tr.ResponseHeaderTimeout > 305*time.Second {
			t.Fatal("transport truncates provider operation budget", tr.ResponseHeaderTimeout)
		}
	}
}
