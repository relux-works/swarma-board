//go:build linux

package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/relux-works/swarma-board/internal/auth"
	"github.com/relux-works/swarma-board/internal/contract"
	"github.com/relux-works/swarma-board/internal/httpapi"
	"github.com/relux-works/swarma-board/internal/testadapter"
)

// This exercises the static resolver through the existing kernel UID hook and
// HTTP adapter on one real keep-alive UDS connection. Shared UID only: this does
// not qualify distinct service/client identities or agent isolation.
func TestStaticPolicyRealUnixKernelUIDNoBearerAndDrift(t *testing.T) {
	dir, e := os.MkdirTemp("", "board-static-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "policy.json")
	data, e := json.Marshal(map[string]any{
		"schema_version": 1, "policy_version": "socket-test-release",
		"mode": "sharedUID", "trust_profile": "single-trust-unit", "remote_tls": false,
		"projects":     []any{map[string]any{"id": "synthetic-board", "name": "Synthetic", "description": "test only"}},
		"accounts":     []any{map[string]any{"id": "socket-test-unit", "disabled": false}},
		"uid_mappings": []any{map[string]any{"uid": uint32(os.Geteuid()), "account_id": "socket-test-unit"}},
		"grants":       []any{map[string]any{"account_id": "socket-test-unit", "project_id": "synthetic-board", "role": "viewer"}},
	})
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, data, 0600); e != nil {
		t.Fatal(e)
	}
	resolver, e := auth.NewStaticPeerResolver(path, "socket-test-release")
	if e != nil {
		t.Fatal(e)
	}
	peer, e := auth.NewPeerAuthorizer(resolver)
	if e != nil {
		t.Fatal(e)
	}
	// These unconfigured hooks refuse any data operation. This test reads only
	// identity, granted projects and readiness; it creates/imports no board data.
	opts := httpapi.Options{Service: testadapter.Service{}, Parser: testadapter.ParserFunc(func(string) (contract.Operation, error) {
		return contract.Operation{}, errors.New("unused test parser")
	}),
		Transfer: testadapter.Transfer{}, Authorizer: peer, PeerAuthenticator: peer,
		StorageReady: func(context.Context) error { return nil }, Commit: "static-socket-test"}
	handler, e := httpapi.New(opts)
	if e != nil {
		t.Fatal(e)
	}
	listener, e := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "board.sock"), Net: "unix"})
	if e != nil {
		t.Fatal(e)
	}
	var accepted atomic.Int32
	server := &http.Server{Handler: handler, ConnContext: httpapi.UnixConnContext,
		ReadHeaderTimeout: time.Second, IdleTimeout: time.Second,
		ConnState: func(_ net.Conn, state http.ConnState) {
			if state == http.StateNew {
				accepted.Add(1)
			}
		}}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Close()
		if e := <-done; !errors.Is(e, http.ErrServerClosed) {
			t.Error(e)
		}
	})
	tr := &http.Transport{MaxConnsPerHost: 1, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", listener.Addr().String())
	}}
	t.Cleanup(tr.CloseIdleConnections)
	client := &http.Client{Transport: tr, Timeout: 3 * time.Second}
	request := func(route, header, value string, want int) string {
		t.Helper()
		r, e := http.NewRequest("GET", "http://unix"+route, nil)
		if e != nil {
			t.Fatal(e)
		}
		if header != "" {
			r.Header.Set(header, value)
		}
		res, e := client.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		body, e := io.ReadAll(res.Body)
		res.Body.Close()
		if e != nil || res.StatusCode != want {
			t.Fatalf("%s status=%d want=%d body=%s err=%v", route, res.StatusCode, want, body, e)
		}
		return string(body)
	}
	if body := request("/v1/me", "", "", 200); !strings.Contains(body, "socket-test-unit") {
		t.Fatal(body)
	}
	if body := request("/v1/boards", "", "", 200); !strings.Contains(body, "synthetic-board") {
		t.Fatal(body)
	}
	request("/ready", "", "", 200)
	for _, header := range []string{"Authorization", "X-Peer-UID", "X-Account-ID"} {
		request("/v1/me", header, "Bearer ignored", 401)
	}
	if e = os.Chmod(path, 0644); e != nil {
		t.Fatal(e)
	}
	request("/ready", "", "", 503)
	request("/v1/me", "", "", 503)
	if e = os.Chmod(path, 0600); e != nil {
		t.Fatal(e)
	}
	request("/ready", "", "", 503)
	request("/v1/boards", "", "", 503)
	if accepted.Load() != 1 {
		t.Fatalf("drift witness used %d connections", accepted.Load())
	}
	if _, e = httpapi.New(opts); e == nil {
		t.Fatal("drifted resolver accepted at startup")
	}
}
