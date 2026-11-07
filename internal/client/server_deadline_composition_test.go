package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/relux-works/swarma-board/internal/contract"
	"github.com/relux-works/swarma-board/internal/httpapi"
	"github.com/relux-works/swarma-board/internal/testadapter"
)

// Exercise the real client and HTTP handler together; only the admitted service,
// archive and authorization seams are synthetic. No provider workload is implied.
func deadlineComposition(t *testing.T, observe func(context.Context, string) error, configure ...func(*http.Server)) (*Client, *atomic.Int32) {
	t.Helper()
	var attempts atomic.Int32
	archive := []byte("synthetic bounded archive")
	sum := sha256.Sum256(archive)
	digest := hex.EncodeToString(sum[:])
	service := testadapter.Service{
		ExecuteFunc: func(ctx context.Context, call contract.Call) (contract.Reply, error) {
			attempts.Add(1)
			if err := observe(ctx, call.Operation.Name); err != nil {
				return contract.Reply{}, err
			}
			payload := `{"count":0}`
			if call.Operation.Name == "set_notes" {
				payload = `{"set_notes":{"id":"TASK-a","changed_ids":["TASK-a"]}}`
			}
			return contract.Reply{Revision: 1, JSON: []byte(payload)}, nil
		},
		SnapshotFunc: func(ctx context.Context, project string) (contract.Snapshot, error) {
			attempts.Add(1)
			if err := observe(ctx, "snapshot"); err != nil {
				return contract.Snapshot{}, err
			}
			return contract.Snapshot{Project: contract.Project{ID: project}, Revision: 1}, nil
		},
		ImportFunc: func(ctx context.Context, _ string, _ contract.Revision, _ contract.AuditContext, _ contract.PreparedImport) (contract.Commit, error) {
			attempts.Add(1)
			if err := observe(ctx, "import"); err != nil {
				return contract.Commit{}, err
			}
			return contract.Commit{Revision: 1}, nil
		},
	}
	h, err := httpapi.New(httpapi.Options{
		Service: service,
		Parser: testadapter.ParserFunc(func(q string) (contract.Operation, error) {
			return contract.Operation{Name: strings.SplitN(q, "(", 2)[0]}, nil
		}),
		Transfer: testadapter.Transfer{
			PrepareFunc: func(ctx context.Context, r io.Reader, opts contract.ImportOptions) (contract.PreparedImport, error) {
				if _, err := io.ReadAll(r); err != nil {
					return contract.PreparedImport{}, err
				}
				if err := observe(ctx, "prepare"); err != nil {
					return contract.PreparedImport{}, err
				}
				return contract.PreparedImport{SHA256: digest, Snapshot: contract.Snapshot{Project: contract.Project{ID: opts.ProjectID}}, Report: contract.ImportReport{SHA256: digest, ElementCount: 1}}, nil
			},
			EncodeFunc: func(ctx context.Context, w io.Writer, _ contract.Snapshot) error {
				if err := observe(ctx, "encode"); err != nil {
					return err
				}
				_, err := w.Write(archive)
				return err
			},
		},
		Authorizer: testadapter.Authorizer{
			ReadyFunc: func(context.Context) error { return nil },
			AuthenticateFunc: func(context.Context, string) (contract.Principal, error) {
				return contract.Principal{AccountID: "synthetic-account"}, nil
			},
			RequireFunc: func(context.Context, contract.Principal, string, string) error { return nil },
		},
		StorageReady: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	config, err := h.Server("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	for _, apply := range configure {
		apply(config)
	}
	s := httptest.NewUnstartedServer(h)
	s.Config = config
	s.Start()
	t.Cleanup(s.Close)
	return newClient(t, s, nil), &attempts
}

func TestClientServerDeadlineComposition(t *testing.T) {
	for _, operation := range []string{"read", "mutation", "export", "validate-import", "commit-import"} {
		t.Run(operation, func(t *testing.T) {
			var observed []time.Duration
			var mu sync.Mutex
			c, attempts := deadlineComposition(t, func(ctx context.Context, _ string) error {
				mu.Lock()
				defer mu.Unlock()
				d, ok := ctx.Deadline()
				if !ok {
					t.Error("server operation lacks deadline")
				}
				observed = append(observed, time.Until(d))
				return nil
			})
			var outgoing []time.Duration
			original := c.http.Transport
			c.http.Transport = observeDeadlineTransport{next: original, check: func(r *http.Request) {
				d, ok := r.Context().Deadline()
				if !ok {
					t.Error("client request lacks deadline")
				}
				outgoing = append(outgoing, time.Until(d))
			}}
			defer func() { c.http.Transport = original }()
			rev := contract.Revision(1)
			var err error
			switch operation {
			case "read":
				_, err = c.Query(context.Background(), "count()", false, nil)
			case "mutation":
				_, err = c.Query(context.Background(), "set_notes()", true, &rev)
			case "export":
				_, _, err = c.Export(context.Background())
			default:
				sum := sha256.Sum256([]byte("synthetic bounded archive"))
				_, err = c.Import(context.Background(), []byte("synthetic bounded archive"), ImportOptions{DryRun: operation == "validate-import", SHA256: hex.EncodeToString(sum[:]), IfMatch: &rev})
			}
			if err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			clientBudget, serverBudget := 305*time.Second, 300*time.Second
			if operation == "read" {
				clientBudget, serverBudget = 35*time.Second, 30*time.Second
			}
			if len(outgoing) != 1 || len(observed) == 0 {
				t.Fatalf("missing observations: client=%v server=%v", outgoing, observed)
			}
			for _, d := range outgoing {
				if d < clientBudget-time.Second || d > clientBudget {
					t.Errorf("client budget %v, want %v", d, clientBudget)
				}
			}
			for _, d := range observed {
				if d < serverBudget-time.Second || d > serverBudget {
					t.Errorf("server budget %v, want %v", d, serverBudget)
				}
			}
			wantAttempts := int32(1)
			if attempts.Load() != wantAttempts {
				t.Fatalf("unexpected service attempts: %d", attempts.Load())
			}
		})
	}
}

func TestClientServerCancellationComposition(t *testing.T) {
	for _, operation := range []string{"mutation", "export", "import"} {
		t.Run(operation, func(t *testing.T) {
			stopped := make(chan struct{})
			var calls atomic.Int32
			c, _ := deadlineComposition(t, func(ctx context.Context, phase string) error {
				if phase == "snapshot" {
					return nil
				}
				calls.Add(1)
				defer close(stopped)
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(2 * time.Second):
					t.Error("caller cancellation did not reach handler work")
					return context.DeadlineExceeded
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			start := time.Now()
			var err error
			rev := contract.Revision(1)
			switch operation {
			case "mutation":
				_, err = c.Query(ctx, "set_notes()", true, &rev)
			case "export":
				_, _, err = c.Export(ctx)
			case "import":
				sum := sha256.Sum256([]byte("synthetic bounded archive"))
				_, err = c.Import(ctx, []byte("synthetic bounded archive"), ImportOptions{SHA256: hex.EncodeToString(sum[:]), IfMatch: &rev})
			}
			if err == nil || ExitCode(err) != 1 {
				t.Fatalf("missing bounded failure: %v", err)
			}
			if operation != "export" && !strings.Contains(err.Error(), "outcome is unknown") {
				t.Fatalf("unknown-write outcome lost: %v", err)
			}
			select {
			case <-stopped:
			case <-time.After(3 * time.Second):
				t.Fatal("handler work did not stop")
			}
			if calls.Load() != 1 || time.Since(start) > 3*time.Second {
				t.Fatalf("unexpected attempts or elapsed: %d %v", calls.Load(), time.Since(start))
			}
		})
	}
}
