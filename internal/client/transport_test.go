package client

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/relux-works/swarma-board/internal/contract"
)

// All Unix tests connect as the current OS UID. They do not qualify the server's
// peer authenticator, protected account mapping or isolation between agents.
func unixServer(t *testing.T, handler http.Handler) (string, *http.Server) {
	t.Helper()
	// A short private path keeps Linux's sockaddr_un limit independent of the
	// workspace path and test name. No host service or persistent socket is used.
	dir, err := os.MkdirTemp("", "board-uds-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "service.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	s := &http.Server{Handler: handler}
	done := make(chan struct{})
	go func() { defer close(done); _ = s.Serve(l) }()
	t.Cleanup(func() { s.Close(); <-done })
	return "unix://" + socket, s
}

func unixOptions(t *testing.T, endpoint string) Options {
	t.Helper()
	tr, err := NewUnixTransport(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tr.CloseIdleConnections)
	return Options{UnixTransport: tr}
}

func TestUnixAndVerifiedTLSFactsPlanParity(t *testing.T) {
	const facts = `{"get":{"id":"TASK-a","title":"Synthetic task","blockedBy":[]}}`
	var results [][]Response
	for _, mode := range []string{"unix-current-uid-only", "verified-tls"} {
		t.Run(mode, func(t *testing.T) {
			var routes []string
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				routes = append(routes, r.URL.RequestURI())
				if mode == "unix-current-uid-only" {
					if hasAuthorization(r.Header) {
						t.Error("authorization crossed Unix socket")
					}
				} else if r.Header.Get("Authorization") != "Bearer "+syntheticBearer {
					t.Error("missing remote bearer")
				}
				if preflight(w, r) {
					return
				}
				if r.Method != http.MethodPost || r.URL.Path != "/v1/boards/synthetic-board/q" {
					t.Error("wire route changed")
				}
				b, _ := io.ReadAll(r.Body)
				w.Header().Set("ETag", ETag(7))
				w.Header().Set("X-Request-ID", "synthetic-unix-request")
				if string(b) == "plan()" {
					io.WriteString(w, planJSON)
				} else {
					io.WriteString(w, facts)
				}
			})
			var endpoint string
			var opts Options
			cred := credential()
			if mode == "unix-current-uid-only" {
				endpoint, _ = unixServer(t, h)
				opts = unixOptions(t, endpoint)
				cred = credentialFunc(func(context.Context, contract.Projection) (string, error) {
					t.Error("Unix mode called CredentialSource")
					return "", nil
				})
			} else {
				s := httptest.NewTLSServer(h)
				defer s.Close()
				endpoint = s.URL
				opts.TLSConfig = s.Client().Transport.(*http.Transport).TLSClientConfig
			}
			c, err := New(context.Background(), projection(endpoint), cred, opts)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			var got []Response
			for _, q := range []string{"get(TASK-a){full}", "plan()"} {
				r, err := c.Query(context.Background(), q, false, nil)
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, r)
			}
			results = append(results, got)
			if !reflect.DeepEqual(routes, []string{"/v1/version", "/v1/me", "/v1/boards/synthetic-board/q?mode=json", "/v1/boards/synthetic-board/q?mode=json"}) {
				t.Fatal(routes)
			}
		})
	}
	if len(results) != 2 || !reflect.DeepEqual(results[0], results[1]) {
		t.Fatal("Unix and TLS facts/plan differ", results)
	}
}

func TestUnixEndpointAndInjectionFailClosed(t *testing.T) {
	for _, endpoint := range []string{"unix:relative", "unix:/tmp/board.sock", "unix://host/tmp/board.sock", "unix://user@/tmp/board.sock", "unix:///", "unix:////tmp/board.sock", "unix:///tmp/../board.sock", "unix:///tmp/./board.sock", "unix:///tmp//board.sock", "unix:///tmp/board.sock/", "unix:///tmp/board%2esock", "unix:///tmp/board.sock?", "unix:///tmp/board.sock?x=y", "unix:///tmp/board.sock#", "unix:///tmp/board.sock#x", "unix:///tmp/board sock", "unix:///tmp/board\x00sock", "unix:///tmp/board\\sock"} {
		if _, err := NewUnixTransport(endpoint); err == nil {
			t.Errorf("accepted invalid Unix endpoint %q", endpoint)
		}
		p, _ := projection(endpoint).Resolve(context.Background())
		if ValidateProjection(p) == nil {
			t.Errorf("projection accepted invalid Unix endpoint %q", endpoint)
		}
	}
	opts := unixOptions(t, "unix:///tmp/synthetic-not-connected.sock")
	for _, test := range []struct {
		endpoint string
		opts     Options
	}{
		{"unix:///tmp/synthetic-not-connected.sock", Options{}},
		{"unix:///tmp/synthetic-not-connected.sock", Options{UnixTransport: &UnixTransport{}}},
		{"unix:///tmp/different.sock", opts},
		{"https://example.test", opts},
		{"unix:///tmp/synthetic-not-connected.sock", Options{UnixTransport: opts.UnixTransport, TLSConfig: &tls.Config{}}},
	} {
		calls := 0
		_, err := New(context.Background(), projection(test.endpoint), credentialFunc(func(context.Context, contract.Projection) (string, error) {
			calls++
			return syntheticBearer, nil
		}), test.opts)
		if ExitCode(err) != 2 || calls != 0 {
			t.Fatal("invalid transport composition accessed credentials or network", calls, err)
		}
	}
}

func TestLoopbackHTTPRequiresExplicitSyntheticOptIn(t *testing.T) {
	var requests atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		preflight(w, r)
	}))
	defer s.Close()
	calls := 0
	cred := credentialFunc(func(context.Context, contract.Projection) (string, error) {
		calls++
		return syntheticBearer, nil
	})
	_, err := New(context.Background(), projection(s.URL), cred, Options{})
	if ExitCode(err) != 2 || requests.Load() != 0 || calls != 0 {
		t.Fatal("default mode reached synthetic HTTP", requests.Load(), calls, err)
	}
	c, err := New(context.Background(), projection(s.URL), cred, Options{SyntheticHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if requests.Load() != 2 || calls != 1 {
		t.Fatal("explicit synthetic preflight changed", requests.Load(), calls)
	}
}

func TestUnixAuthorizationHeadersAndAlternateOriginsRefused(t *testing.T) {
	var calls atomic.Int32
	endpoint, _ := unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		preflight(w, r)
	}))
	opts := unixOptions(t, endpoint)
	c, err := New(context.Background(), projection(endpoint), nil, opts)
	if err != nil {
		t.Fatal("Unix mode must accept nil CredentialSource", err)
	}
	defer c.Close()
	for _, name := range []string{"Authorization", "authorization", "Proxy-Authorization", "pRoXy-AuThOrIzAtIoN"} {
		h := http.Header{name: {"Bearer " + syntheticBearer}}
		if _, _, _, err := c.request(context.Background(), http.MethodGet, "/v1/me", nil, h, false, false); ExitCode(err) != 2 {
			t.Fatal("Unix client accepted authorization", name, err)
		}
		req, _ := http.NewRequest(http.MethodGet, unixOrigin+"/v1/me", nil)
		req.Header = h
		if _, err := opts.UnixTransport.RoundTrip(req); ExitCode(err) != 2 {
			t.Fatal("Unix transport accepted authorization", name, err)
		}
	}
	for _, origin := range []string{"https://swarma-board.local", "http://127.0.0.1", "http://user:password@swarma-board.local"} {
		req, _ := http.NewRequest(http.MethodGet, origin+"/v1/me", nil)
		if _, err := opts.UnixTransport.RoundTrip(req); ExitCode(err) != 2 {
			t.Fatal("Unix transport accepted alternate origin", origin, err)
		}
	}
	if calls.Load() != 2 {
		t.Fatal("refused request reached socket", calls.Load())
	}
}

func TestUnixRedirectRefusedWithoutTCPFallback(t *testing.T) {
	for _, route := range []string{"/v1/version", "/v1/me", "/v1/boards/synthetic-board/q"} {
		t.Run(route, func(t *testing.T) {
			var forwarded, attempts atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { forwarded.Add(1) }))
			defer target.Close()
			endpoint, _ := unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == route {
					attempts.Add(1)
					http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
					return
				}
				preflight(w, r)
			}))
			c, err := New(context.Background(), projection(endpoint), nil, unixOptions(t, endpoint))
			if err == nil {
				defer c.Close()
				_, err = c.Query(context.Background(), "count()", false, nil)
			}
			if err == nil || forwarded.Load() != 0 || attempts.Load() != 1 {
				t.Fatal("redirect followed or retried", err, forwarded.Load(), attempts.Load())
			}
		})
	}
}

func TestUnixServiceDeadRefusal(t *testing.T) {
	for _, mode := range []string{"missing", "dead-socket"} {
		t.Run(mode, func(t *testing.T) {
			dir, err := os.MkdirTemp("", "board-dead-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			socket := filepath.Join(dir, "service.sock")
			if mode == "dead-socket" {
				l, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
				if err != nil {
					t.Fatal(err)
				}
				l.SetUnlinkOnClose(false)
				l.Close()
			}
			endpoint := "unix://" + socket
			_, err = New(context.Background(), projection(endpoint), nil, unixOptions(t, endpoint))
			e, ok := err.(*Error)
			if !ok || e.Code != "unavailable" || ExitCode(err) != 1 {
				t.Fatal("undefined service-dead refusal", err)
			}
			b, _ := json.Marshal(err)
			if strings.Contains(string(b), socket) || strings.Contains(string(b), dir) {
				t.Fatal("socket path leaked", string(b))
			}
		})
	}
}

func TestUnixDroppedWriteIsNeverRetried(t *testing.T) {
	var attempts atomic.Int32
	endpoint, _ := unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if preflight(w, r) {
			return
		}
		attempts.Add(1)
		io.Copy(io.Discard, r.Body)
		if r.Header.Get("If-Match") != ETag(7) || hasAuthorization(r.Header) {
			t.Error("write header contract changed")
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	}))
	c, err := New(context.Background(), projection(endpoint), nil, unixOptions(t, endpoint))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	rev := contract.Revision(7)
	_, err = c.Query(context.Background(), "rename(TASK-a,name=x)", true, &rev)
	if err == nil || !strings.Contains(err.Error(), "read and reconcile") || attempts.Load() != 1 {
		t.Fatal("unsafe write retry/outcome", attempts.Load(), err)
	}
}

func TestTLSWrongEndpointHostnameRefusedBeforeCredential(t *testing.T) {
	calls := 0
	_, err := New(context.Background(), projection("https://wrong.example.test"), credentialFunc(func(context.Context, contract.Projection) (string, error) {
		calls++
		return syntheticBearer, nil
	}), Options{TLSConfig: &tls.Config{ServerName: "different.example.test"}})
	if ExitCode(err) != 2 || calls != 0 {
		t.Fatal("hostname override bypassed endpoint verification", calls, err)
	}
}

func TestTLSMissingHostnameRefused(t *testing.T) {
	for _, endpoint := range []string{"https://:443", "https://[]:443"} {
		p, _ := projection(endpoint).Resolve(context.Background())
		if ValidateProjection(p) == nil {
			t.Error("accepted missing endpoint hostname", endpoint)
		}
	}
}

func TestTrustedTLSCertificateForWrongHostRefused(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		preflight(w, r)
	}))
	defer s.Close()
	// The httptest certificate is trusted, but does not cover localhost.
	endpoint := strings.Replace(s.URL, "127.0.0.1", "localhost", 1)
	_, err := New(context.Background(), projection(endpoint), credential(), Options{TLSConfig: s.Client().Transport.(*http.Transport).TLSClientConfig})
	if err == nil || calls.Load() != 0 {
		t.Fatal("trusted certificate accepted for wrong endpoint hostname", calls.Load(), err)
	}
}

func TestUnixAccountPreflightRefusals(t *testing.T) {
	for _, kind := range []string{"wrong-account", "unauthorized", "forbidden", "unavailable"} {
		t.Run(kind, func(t *testing.T) {
			var projectCalls atomic.Int32
			endpoint, _ := unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/version" {
					io.WriteString(w, versionJSON)
					return
				}
				if r.URL.Path != "/v1/me" {
					projectCalls.Add(1)
					return
				}
				if kind == "wrong-account" {
					io.WriteString(w, `{"account_id":"other"}`)
					return
				}
				w.WriteHeader(map[string]int{"unauthorized": 401, "forbidden": 403, "unavailable": 503}[kind])
				io.WriteString(w, `{"error":{"code":"`+kind+`","message":"private UID mapping"}}`)
			}))
			_, err := New(context.Background(), projection(endpoint), nil, unixOptions(t, endpoint))
			wantExit := map[string]int{"wrong-account": 2, "unauthorized": 3, "forbidden": 3, "unavailable": 1}[kind]
			if err == nil || ExitCode(err) != wantExit || projectCalls.Load() != 0 || strings.Contains(err.Error(), "private UID mapping") {
				t.Fatal("Unix account preflight did not refuse safely", projectCalls.Load(), err)
			}
		})
	}
}

func TestUnixServiceDiesAfterPreflight(t *testing.T) {
	endpoint, server := unixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { preflight(w, r) }))
	c, err := New(context.Background(), projection(endpoint), nil, unixOptions(t, endpoint))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	server.Close()
	c.Close() // Remove pooled connections so the selected socket must be dialed.
	rev := contract.Revision(7)
	_, err = c.Query(context.Background(), "rename(TASK-a,name=x)", true, &rev)
	e, ok := err.(*Error)
	if !ok || e.Code != "unavailable" || ExitCode(err) != 1 || !strings.Contains(err.Error(), "read and reconcile") {
		t.Fatal("dead service lost the write reconciliation requirement", err)
	}
}
