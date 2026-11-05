//go:build linux || darwin

package httpapi

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/relux-works/swarma-board/internal/auth"
	"github.com/relux-works/swarma-board/internal/contract"
)

type livePeerPolicy struct {
	mu                    sync.Mutex
	account               string
	grant, disabled, down bool
	seen                  []uint32
}

func (p *livePeerPolicy) Ready(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.down {
		return errors.New("private authority down")
	}
	return nil
}
func (p *livePeerPolicy) ResolvePeer(_ context.Context, uid uint32) (contract.Principal, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen = append(p.seen, uid)
	if uid != uint32(os.Getuid()) || p.disabled || p.account == "" {
		return contract.Principal{}, errors.New("unmapped/disabled")
	}
	return contract.Principal{AccountID: p.account}, nil
}
func (p *livePeerPolicy) Require(_ context.Context, principal contract.Principal, _, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if principal.AccountID != p.account || p.disabled || !p.grant {
		return errors.New("revoked")
	}
	return nil
}
func (p *livePeerPolicy) Projects(_ context.Context, principal contract.Principal) ([]contract.Project, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if principal.AccountID != p.account || p.disabled {
		return nil, errors.New("disabled")
	}
	if !p.grant {
		return []contract.Project{}, nil
	}
	return []contract.Project{{ID: "synthetic-board"}}, nil
}

// This real socket uses the same OS UID on both ends. It proves SO_PEERCRED
// extraction and per-request policy refresh, NOT distinct-UID agent isolation.
func TestRealUnixSameUIDKernelCredentialsAndKeepAliveRevocation(t *testing.T) {
	x := newHarness(t)
	policy := &livePeerPolicy{account: "host-test-account", grant: true}
	adapter, e := auth.NewPeerAuthorizer(policy)
	if e != nil {
		t.Fatal(e)
	}
	opts := x.h.o
	opts.Authorizer, opts.PeerAuthenticator, opts.Log = adapter, adapter, nil
	h, e := New(opts)
	if e != nil {
		t.Fatal(e)
	}
	// /tmp keeps sockaddr_un below the kernel path-length bound in long checkouts.
	dir, e := os.MkdirTemp("", "board-peer-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "server.sock")
	listener, e := net.Listen("unix", path)
	if e != nil {
		t.Fatal(e)
	}
	var accepted atomic.Int32
	server := &http.Server{Handler: h, ConnContext: UnixConnContext,
		ReadHeaderTimeout: time.Second, IdleTimeout: time.Second,
		ConnState: func(_ net.Conn, s http.ConnState) {
			if s == http.StateNew {
				accepted.Add(1)
			}
		}}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Close()
		if e := <-serveDone; !errors.Is(e, http.ErrServerClosed) {
			t.Error(e)
		}
	})
	transport := &http.Transport{MaxConnsPerHost: 1, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	request := func(path string, headers map[string]string, want int) string {
		t.Helper()
		r, e := http.NewRequest("GET", "http://unix"+path, nil)
		if e != nil {
			t.Fatal(e)
		}
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		response, e := client.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		body, e := io.ReadAll(response.Body)
		response.Body.Close()
		if e != nil || response.StatusCode != want {
			t.Fatalf("%s status=%d want=%d body=%s err=%v", path, response.StatusCode, want, body, e)
		}
		return string(body)
	}
	if body := request("/v1/me", nil, 200); !strings.Contains(body, "host-test-account") {
		t.Fatal(body)
	}
	request("/v1/boards/synthetic-board/export", nil, 200)
	request("/health", map[string]string{"Authorization": "Bearer ignored"}, 401)
	for _, header := range []string{"Authorization", "X-Account-ID", "X-User-ID", "X-Remote-User", "X-Forwarded-User", "Remote-User", "X-Authenticated-User", "X-Peer-UID", "X-UID", "X-Forwarded-Uid"} {
		request("/v1/me", map[string]string{header: "forged"}, 401)
	}
	policy.mu.Lock()
	policy.account = "remapped-test-account"
	policy.mu.Unlock()
	if body := request("/v1/me", nil, 200); !strings.Contains(body, "remapped-test-account") {
		t.Fatal("cached principal", body)
	}
	policy.mu.Lock()
	policy.grant = false
	policy.mu.Unlock()
	request("/v1/boards/synthetic-board/export", nil, 403)
	if body := request("/v1/boards", nil, 200); !strings.Contains(body, `"boards":[]`) {
		t.Fatal("cached grants", body)
	}
	policy.mu.Lock()
	policy.disabled = true
	policy.mu.Unlock()
	request("/v1/me", nil, 401)
	policy.mu.Lock()
	policy.disabled = false
	policy.account = ""
	policy.mu.Unlock()
	request("/v1/me", nil, 401)
	policy.mu.Lock()
	policy.down = true
	policy.mu.Unlock()
	request("/ready", nil, 503)
	request("/v1/me", nil, 503)
	request("/health", nil, 200)
	if _, e = New(opts); e == nil {
		t.Fatal("startup accepted unavailable peer authority")
	}
	if accepted.Load() != 1 {
		t.Fatalf("not a single keep-alive connection: %d", accepted.Load())
	}
	policy.mu.Lock()
	defer policy.mu.Unlock()
	if len(policy.seen) < 7 {
		t.Fatalf("mapping not refreshed: %v", policy.seen)
	}
	for _, uid := range policy.seen {
		if uid != uint32(os.Getuid()) {
			t.Fatalf("kernel UID %d != process UID %d", uid, os.Getuid())
		}
	}
}

func TestPeerModeMissingKernelCredentialsAndBearerTLS(t *testing.T) {
	x := newHarness(t)
	policy := &livePeerPolicy{account: "host-test-account", grant: true}
	adapter, e := auth.NewPeerAuthorizer(policy)
	if e != nil {
		t.Fatal(e)
	}
	opts := x.h.o
	opts.Authorizer, opts.PeerAuthenticator, opts.Log = adapter, adapter, nil
	h, e := New(opts)
	if e != nil {
		t.Fatal(e)
	}
	for _, ctx := range []context.Context{context.Background(), UnixConnContext(context.Background(), nil)} {
		r := httptest.NewRequest("GET", "/v1/me", nil).WithContext(ctx)
		r.Header.Set("Authorization", "Bearer http-viewer")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		requireStatus(t, w, 401, "unauthorized")
		r.Header.Del("Authorization")
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		requireStatus(t, w, 401, "unauthorized")
	}
	var typedNil *auth.PeerAuthorizer
	opts.PeerAuthenticator = typedNil
	if _, e = New(opts); e == nil {
		t.Fatal("typed nil peer adapter accepted")
	}
	// A real verified TLS request retains the existing Bearer authorizer.
	x.h.o.Log = nil
	remote := httptest.NewTLSServer(x.h)
	defer remote.Close()
	r, e := http.NewRequest("GET", remote.URL+"/v1/me", nil)
	if e != nil {
		t.Fatal(e)
	}
	r.Header.Set("Authorization", "Bearer http-viewer")
	response, e := remote.Client().Do(r)
	if e != nil {
		t.Fatal(e)
	}
	body, e := io.ReadAll(response.Body)
	response.Body.Close()
	if e != nil || response.StatusCode != 200 || !strings.Contains(string(body), "synthetic-viewer") {
		t.Fatalf("TLS Bearer lost: %d %s %v", response.StatusCode, body, e)
	}
	// Even a valid Bearer cannot authenticate a captured Unix connection if the
	// integrator forgot to provide its protected peer authenticator.
	r = httptest.NewRequest("GET", "/v1/me", nil).WithContext(UnixConnContext(context.Background(), nil))
	r.Header.Set("Authorization", "Bearer http-viewer")
	w := httptest.NewRecorder()
	x.h.ServeHTTP(w, r)
	requireStatus(t, w, 401, "unauthorized")
}
