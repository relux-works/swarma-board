package httpapi

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/relux-works/swarma-board/internal/contract"
	"github.com/relux-works/swarma-board/internal/testadapter"
)

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	mu          sync.Mutex
	read, write time.Time
}

func (w *deadlineRecorder) SetReadDeadline(d time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.read = d
	return nil
}
func (w *deadlineRecorder) SetWriteDeadline(d time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.write = d
	return nil
}

func TestRouteOperationAndSocketBudgets(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, token, body, media string
		budget                                 time.Duration
	}{
		{"read", "POST", queryPath, "http-viewer", "count()", "text/plain", 30 * time.Second},
		{"mutation", "POST", queryPath, "http-editor", mutation, "text/plain", 300 * time.Second},
		{"export", "GET", "/v1/boards/synthetic-board/export", "http-viewer", "", "", 300 * time.Second},
		{"import", "POST", importPath + "?dry_run=true", "http-admin", "synthetic archive", "application/gzip", 300 * time.Second},
		{"import-commit", "POST", importPath, "http-admin", "synthetic archive", "application/gzip", 300 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := newHarness(t)
			original, transfer := x.h.o.Service, x.h.o.Transfer
			var deadlines []time.Time
			observe := func(ctx context.Context) {
				d, ok := ctx.Deadline()
				if !ok {
					t.Fatal("missing operation deadline")
				}
				deadlines = append(deadlines, d)
			}
			x.h.o.Service = testadapter.Service{
				ExecuteFunc: func(ctx context.Context, c contract.Call) (contract.Reply, error) {
					observe(ctx)
					return original.Execute(ctx, c)
				},
				SnapshotFunc: func(ctx context.Context, p string) (contract.Snapshot, error) {
					observe(ctx)
					return original.Snapshot(ctx, p)
				},
				ImportFunc: func(ctx context.Context, p string, r contract.Revision, a contract.AuditContext, in contract.PreparedImport) (contract.Commit, error) {
					observe(ctx)
					return original.Import(ctx, p, r, a, in)
				},
			}
			x.h.o.Transfer = testadapter.Transfer{
				PrepareFunc: func(ctx context.Context, r io.Reader, o contract.ImportOptions) (contract.PreparedImport, error) {
					observe(ctx)
					return transfer.Prepare(ctx, r, o)
				},
				EncodeFunc: func(ctx context.Context, w io.Writer, s contract.Snapshot) error {
					observe(ctx)
					return transfer.Encode(ctx, w, s)
				},
			}
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			r.Header.Set("Authorization", "Bearer "+tc.token)
			r.Header.Set("Content-Type", tc.media)
			r.Header.Set("If-Match", `"rev-0"`)
			sum := sha256.Sum256([]byte(tc.body))
			r.Header.Set("X-Snapshot-SHA256", hex.EncodeToString(sum[:]))
			w := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
			before := time.Now()
			x.h.ServeHTTP(w, r)
			after := time.Now()
			requireStatus(t, w.ResponseRecorder, 200, "")
			if len(deadlines) == 0 {
				t.Fatal("no operation")
			}
			for _, d := range deadlines {
				if d.Before(before.Add(tc.budget)) || d.After(after.Add(tc.budget)) {
					t.Fatalf("operation budget %v, deadline %v", tc.budget, d.Sub(before))
				}
			}
			if w.write.Before(before.Add(tc.budget+5*time.Second)) || w.write.After(after.Add(tc.budget+5*time.Second)) {
				t.Fatalf("wire deadline %v", w.write.Sub(before))
			}
			if x.executes+x.snapshots+x.imports != 1 {
				t.Fatalf("unexpected attempts: execute=%d snapshot=%d import=%d", x.executes, x.snapshots, x.imports)
			}
		})
	}
}

func TestExportConsumptionUsesCallerContext(t *testing.T) {
	x := newHarness(t)
	caller, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	want, _ := caller.Deadline()
	attempts := 0
	var consumed context.Context
	x.h.o.Transfer = testadapter.Transfer{EncodeFunc: func(ctx context.Context, w io.Writer, s contract.Snapshot) error {
		attempts++
		consumed = ctx
		if d, _ := ctx.Deadline(); !d.Equal(want) {
			t.Fatalf("stream deadline extended: %v", d)
		}
		if _, err := io.WriteString(w, "first archive chunk"); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}}
	r := httptest.NewRequest("GET", "/v1/boards/synthetic-board/export", nil).WithContext(caller)
	r.Header.Set("Authorization", "Bearer http-viewer")
	w := httptest.NewRecorder()
	x.h.ServeHTTP(w, r)
	requireStatus(t, w, 503, "unavailable")
	if attempts != 1 || x.snapshots != 1 || consumed.Err() == nil || strings.Contains(w.Body.String(), "first archive chunk") {
		t.Fatal("transfer cancellation or single-attempt boundary lost")
	}
}

func TestCallerDeadlineAndUnknownWriteAreNotRetried(t *testing.T) {
	x := newHarness(t)
	caller, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	want, _ := caller.Deadline()
	attempts := 0
	x.h.o.Service = testadapter.Service{ExecuteFunc: func(ctx context.Context, c contract.Call) (contract.Reply, error) {
		attempts++
		if d, _ := ctx.Deadline(); !d.Equal(want) {
			t.Fatalf("caller deadline extended: %v", d)
		}
		<-ctx.Done()
		// An unavailable result may follow a decision; the handler must not replay.
		return contract.Reply{}, ctx.Err()
	}}
	r := httptest.NewRequest("POST", queryPath, strings.NewReader(mutation)).WithContext(caller)
	r.Header.Set("Authorization", "Bearer http-editor")
	r.Header.Set("Content-Type", "text/plain")
	r.Header.Set("If-Match", `"rev-0"`)
	w := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	x.h.ServeHTTP(w, r)
	requireStatus(t, w.ResponseRecorder, 503, "unavailable")
	if attempts != 1 || !w.write.Equal(want) {
		t.Fatalf("attempts=%d write deadline=%v", attempts, w.write)
	}
}

func TestParsingDoesNotRestartBudget(t *testing.T) {
	x := newHarness(t)
	parser := x.h.o.Parser
	x.h.o.Parser = testadapter.ParserFunc(func(s string) (contract.Operation, error) {
		<-time.After(100 * time.Millisecond)
		return parser.Parse(s)
	})
	before := time.Now()
	x.h.o.Service = testadapter.Service{ExecuteFunc: func(ctx context.Context, c contract.Call) (contract.Reply, error) {
		d, _ := ctx.Deadline()
		if delta := d.Sub(before.Add(300 * time.Second)); delta < 0 || delta > 50*time.Millisecond {
			t.Fatalf("budget restarted or truncated: delta=%v", delta)
		}
		return contract.Reply{JSON: []byte(`{"set_notes":{}}`)}, nil
	}}
	w := x.request("POST", queryPath, "http-editor", mutation, map[string]string{"Content-Type": "text/plain", "If-Match": `"rev-0"`})
	requireStatus(t, w, 200, "")
}

// This control uses a real net/http body: context cancellation must interrupt
// its blocked Read even when the caller has no deadline to copy to the socket.
func TestSlowBodyCancellation(t *testing.T) {
	x := newHarness(t)
	caller, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		x.h.ServeHTTP(w, r.WithContext(caller))
	}))
	srv.Start()
	defer srv.Close()
	conn, err := net.DialTimeout("tcp", srv.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	_, err = io.WriteString(conn, "POST "+queryPath+" HTTP/1.1\r\nHost: synthetic\r\nAuthorization: Bearer http-editor\r\nContent-Type: text/plain\r\nContent-Length: 100\r\n\r\nx")
	if err != nil {
		t.Fatal(err)
	}
	// Allow the local server to enter body consumption, then cancel without a
	// deadline. The overall control has a separate one-second failure bound.
	timer := time.AfterFunc(50*time.Millisecond, cancel)
	defer timer.Stop()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt slow body")
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 503 {
		t.Fatalf("slow body status %d", response.StatusCode)
	}
	if x.executes != 0 || x.imports != 0 {
		t.Fatal("incomplete body reached mutation")
	}
}
