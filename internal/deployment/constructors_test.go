package deployment_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/relux-works/swarma-board/internal/app"
	"github.com/relux-works/swarma-board/internal/client"
	"github.com/relux-works/swarma-board/internal/contract"
	"github.com/relux-works/swarma-board/internal/deployment"
	"github.com/relux-works/swarma-board/internal/testadapter"
)

// This is constructor qualification with a FAKE shared-UID resolver. It proves
// composition compatibility only, never static policy custody or binary wiring.
type credentialFunc func(context.Context, contract.Projection) (string, error)

func (f credentialFunc) Bearer(ctx context.Context, p contract.Projection) (string, error) {
	return f(ctx, p)
}

type controlledResolver struct {
	app.SyntheticPeerResolver
	ready atomic.Bool
}

func (r *controlledResolver) Ready(ctx context.Context) error {
	if !r.ready.Load() {
		return deployment.ErrConfig
	}
	return r.SyntheticPeerResolver.Ready(ctx)
}

func TestExistingUnixConstructorsExposeDescriptorAndPreserveReadiness(t *testing.T) {
	c := config(t)
	p, err := deployment.Validate(c)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open("../testadapter/testdata/server.json")
	if err != nil {
		t.Fatal(err)
	}
	s, err := app.ConcreteSynthetic(context.Background(), app.SyntheticOptions{Fixture: f, DataDir: c.DataDir, Listen: "127.0.0.1:0", Commit: "dev"})
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := &controlledResolver{SyntheticPeerResolver: app.SyntheticPeerResolver{UID: uint32(os.Geteuid()), Account: "shared-agent", Project: contract.Project{ID: "synthetic-board"}}}
	r.ready.Store(true)
	server, err := app.NewUnixServer(s, r, "dev")
	if err != nil {
		t.Fatal(err)
	}
	server.Handler, err = p.WithStatusHeaders(server.Handler)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := app.ListenUnix(strings.TrimPrefix(c.Endpoint, "unix://"))
	if err != nil {
		t.Fatal(err)
	}
	// Client validation works with an existing service socket. A second listener
	// remains refused by the lifecycle constructor; no unlink/recovery happens.
	if _, err := deployment.Validate(c); err != nil {
		t.Fatal(err)
	}
	if second, err := app.ListenUnix(strings.TrimPrefix(c.Endpoint, "unix://")); err == nil {
		second.Close()
		t.Fatal("occupied socket replaced")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.ServeUntilCanceled(ctx, server, listener) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("shutdown stuck")
		}
	}()
	tr, err := p.NewUnixTransport()
	if err != nil {
		t.Fatal(err)
	}
	defer tr.CloseIdleConnections()
	hc := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	get := func(route string, want int) {
		t.Helper()
		resp, err := hc.Get("http://swarma-board.local" + route)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		var d deployment.Descriptor
		if err := json.Unmarshal([]byte(resp.Header.Get(deployment.DescriptorHeader)), &d); err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != want || d.Profile != deployment.SingleTrustUnit || d.ProductionAdmitted || d.PerAgentIsolation {
			t.Fatalf("%s status=%d descriptor=%+v body=%s", route, resp.StatusCode, d, body)
		}
	}
	get("/ready", 200)
	get("/v1/version", 200)
	projection := contract.Projection{Endpoint: c.Endpoint, ProjectID: "synthetic-board", AccountID: "shared-agent"}
	if err := p.CheckProjection(projection); err != nil {
		t.Fatal(err)
	}
	credentialCalls := 0
	credentials := credentialFunc(func(context.Context, contract.Projection) (string, error) {
		credentialCalls++
		return "must-not-read", nil
	})
	cl, err := client.New(ctx, testadapter.Projection{Value: projection}, credentials, client.Options{UnixTransport: tr})
	if err != nil {
		t.Fatal(err)
	}
	cl.Close()
	if credentialCalls != 0 {
		t.Fatal("Unix client read credentials")
	}
	// Same persistent client still sees current authority readiness, not a cached
	// green result. The existing httpapi handler supplies the refusal.
	r.ready.Store(false)
	get("/ready", 503)
	r.ready.Store(true)
	get("/ready", 200)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	get("/ready", 503)
}
