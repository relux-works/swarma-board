package integration

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/relux-works/swarma-board/internal/app"
	"github.com/relux-works/swarma-board/internal/cli"
	"github.com/relux-works/swarma-board/internal/client"
	"github.com/relux-works/swarma-board/internal/contract"
	"github.com/relux-works/swarma-board/internal/testadapter"
)

// mutablePeer is explicitly synthetic authority over this process's shared UID.
// Its observations prove kernel extraction and current-policy calls, never
// distinct-UID provisioning or isolation between agents sharing that identity.
type mutablePeer struct {
	mu                    sync.Mutex
	fake                  app.SyntheticPeerResolver
	deny, unready, viewer bool
	seen                  uint32
	calls                 int
}

func (p *mutablePeer) Ready(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.unready {
		return errors.New("synthetic policy down")
	}
	return p.fake.Ready(ctx)
}
func (p *mutablePeer) ResolvePeer(ctx context.Context, uid uint32) (contract.Principal, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen = uid
	p.calls++
	if p.deny {
		return contract.Principal{}, errors.New("synthetic mapping revoked")
	}
	return p.fake.ResolvePeer(ctx, uid)
}
func (p *mutablePeer) Require(ctx context.Context, who contract.Principal, project, role string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.deny || (p.viewer && role != "viewer") {
		return errors.New("synthetic grant revoked")
	}
	return p.fake.Require(ctx, who, project, role)
}
func (p *mutablePeer) Projects(ctx context.Context, who contract.Principal) ([]contract.Project, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.deny {
		return nil, errors.New("synthetic mapping revoked")
	}
	return p.fake.Projects(ctx, who)
}

type spyCredential struct{ calls atomic.Int32 }

func (s *spyCredential) Bearer(context.Context, contract.Projection) (string, error) {
	s.calls.Add(1)
	return "", errors.New("Unix must not call credential source")
}

type transportHarness struct {
	app              *app.Synthetic
	peer             *mutablePeer
	socket, endpoint string
	local, remote    *http.Server
	tlsURL           string
	tls              *tls.Config
	accepted         atomic.Int32
	stop             func()
}

func transports(t *testing.T) *transportHarness {
	t.Helper()
	dir, e := os.MkdirTemp("", "board-integrated-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	a, e := app.ConcreteSynthetic(context.Background(), app.SyntheticOptions{Fixture: bytes.NewReader(fixture(t, "server.json")), DataDir: filepath.Join(dir, "data"), Listen: "127.0.0.1:0", Commit: build})
	if e != nil {
		t.Fatal(e)
	}
	h := &transportHarness{app: a, socket: filepath.Join(dir, "board.sock")}
	h.endpoint = "unix://" + h.socket
	h.peer = &mutablePeer{fake: app.SyntheticPeerResolver{UID: uint32(os.Geteuid()), Account: "synthetic-admin", Project: contract.Project{ID: project}}}
	h.local, e = app.NewUnixServer(a, h.peer, build)
	if e != nil {
		a.Close()
		t.Fatal(e)
	}
	h.local.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			h.accepted.Add(1)
		}
	}
	ln, e := app.ListenUnix(h.socket)
	if e != nil {
		a.Close()
		t.Fatal(e)
	}
	// Obtain a self-signed fixture certificate and independently pinned roots.
	certServer := httptest.NewTLSServer(http.NotFoundHandler())
	config := certServer.TLS.Clone()
	h.tls = certServer.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	certServer.Close()
	h.remote, e = app.NewVerifiedTLSServer(a.Handler, config)
	if e != nil {
		ln.Close()
		a.Close()
		t.Fatal(e)
	}
	tcp, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		ln.Close()
		a.Close()
		t.Fatal(e)
	}
	h.tlsURL = "https://" + tcp.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 2)
	go func() { done <- app.ServeUntilCanceled(ctx, h.local, ln) }()
	go func() { done <- app.ServeUntilCanceled(ctx, h.remote, tls.NewListener(tcp, h.remote.TLSConfig)) }()
	var once sync.Once
	h.stop = func() {
		once.Do(func() {
			cancel()
			for i := 0; i < 2; i++ {
				select {
				case e := <-done:
					if e != nil {
						t.Error(e)
					}
				case <-time.After(15 * time.Second):
					t.Error("shutdown stuck")
				}
			}
			if e := a.Close(); e != nil {
				t.Error(e)
			}
		})
	}
	t.Cleanup(h.stop)
	return h
}
func (h *transportHarness) localClient(t *testing.T) (*client.Client, *spyCredential) {
	t.Helper()
	tr, e := client.NewUnixTransport(h.endpoint)
	if e != nil {
		t.Fatal(e)
	}
	spy := &spyCredential{}
	c, e := client.New(context.Background(), testadapter.Projection{Value: contract.Projection{Endpoint: h.endpoint, ProjectID: project, AccountID: "synthetic-admin"}}, spy, client.Options{UnixTransport: tr})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(c.Close)
	return c, spy
}
func (h *transportHarness) remoteClient(t *testing.T, config *tls.Config) (*client.Client, error) {
	t.Helper()
	return client.New(context.Background(), testadapter.Projection{Value: contract.Projection{Endpoint: h.tlsURL, ProjectID: project, AccountID: "synthetic-admin"}}, credential("swarma-board-synthetic-admin"), client.Options{TLSConfig: config})
}
func (h *transportHarness) rawClient(t *testing.T) *http.Client {
	t.Helper()
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", h.socket)
	}}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 5 * time.Second}
}
func rawStatus(t *testing.T, c *http.Client, path string, headers map[string]string) int {
	t.Helper()
	r, e := http.NewRequest("GET", "http://swarma-board.local"+path, nil)
	if e != nil {
		t.Fatal(e)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	res, e := c.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	return res.StatusCode
}

func TestIntegratedUnixKernelUIDAndVerifiedTLSParityCASJournal(t *testing.T) {
	h := transports(t)
	local, spy := h.localClient(t)
	remote, e := h.remoteClient(t, h.tls)
	if e != nil {
		t.Fatal(e)
	}
	defer remote.Close()
	dry, e := local.Import(context.Background(), fixture(t, "native-v1.tar.gz"), client.ImportOptions{DryRun: true})
	if e != nil || dry.Revision != 0 {
		t.Fatalf("dry-run: %+v %v", dry, e)
	}
	imported(t, local, fixture(t, "native-v1.tar.gz"))
	for _, q := range []string{"count()", "list()", "plan()", "get(TASK-001)"} {
		a, b := query(t, local, q), query(t, remote, q)
		if a.Revision != b.Revision || !bytes.Equal(a.JSON, b.JSON) {
			t.Fatalf("transport parity %s: %s / %s", q, a.JSON, b.JSON)
		}
	}
	// Exercise the same product command tree through both explicit transports.
	tr, e := client.NewUnixTransport(h.endpoint)
	if e != nil {
		t.Fatal(e)
	}
	defer tr.CloseIdleConnections()
	for _, args := range [][]string{{"q", "count()"}, {"plan", "--critical-path"}} {
		var a, b, stderr bytes.Buffer
		for i, o := range []cli.Options{
			{Projection: testadapter.Projection{Value: contract.Projection{Endpoint: h.endpoint, ProjectID: project, AccountID: "synthetic-admin"}}, UnixTransport: tr, Out: &a, Err: &stderr},
			{Projection: testadapter.Projection{Value: contract.Projection{Endpoint: h.tlsURL, ProjectID: project, AccountID: "synthetic-admin"}}, Credential: credential("swarma-board-synthetic-admin"), TLSConfig: h.tls, Out: &b, Err: &stderr},
		} {
			if n := cli.Run(context.Background(), args, o); n != 0 {
				t.Fatalf("CLI transport %d: %d %s", i, n, stderr.String())
			}
		}
		if !bytes.Equal(a.Bytes(), b.Bytes()) {
			t.Fatalf("CLI parity: %s / %s", a.String(), b.String())
		}
	}
	expected := contract.Revision(1)
	errs := make(chan error, 2)
	for _, c := range []*client.Client{local, remote} {
		go func(c *client.Client) {
			_, e := c.Query(context.Background(), `set_notes(TASK-001,text="transport CAS",set=true)`, true, &expected)
			errs <- e
		}(c)
	}
	wins, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		e := <-errs
		if e == nil {
			wins++
		} else {
			code(t, e, "revision_conflict")
			conflicts++
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("CAS wins=%d conflicts=%d", wins, conflicts)
	}
	snap, events, e := h.app.Registry.ExportView(context.Background(), project)
	if e != nil || snap.Revision != 2 || len(events) != 2 {
		t.Fatalf("coherent existing-v1 journal: %d %+v %v", snap.Revision, events, e)
	}
	for _, ev := range events {
		if ev.Context.AccountID != "synthetic-admin" {
			t.Fatalf("untrusted actor: %+v", ev)
		}
	}
	if spy.calls.Load() != 0 {
		t.Fatal("Unix resolved bearer")
	}
	h.peer.mu.Lock()
	seen, calls := h.peer.seen, h.peer.calls
	h.peer.mu.Unlock()
	if seen != uint32(os.Geteuid()) || calls < 4 {
		t.Fatalf("kernel UID or per-request resolution absent: uid=%d calls=%d", seen, calls)
	}
	// Same data directory is reopened after both listeners drain and release ownership.
	h.stop()
	if _, e = os.Lstat(h.socket); !os.IsNotExist(e) {
		t.Fatalf("owned socket not removed: %v", e)
	}
	re, e := app.ConcreteSynthetic(context.Background(), app.SyntheticOptions{Fixture: bytes.NewReader(fixture(t, "server.json")), DataDir: filepath.Dir(h.socket) + "/data", Listen: "127.0.0.1:0", Commit: build})
	if e != nil {
		t.Fatal(e)
	}
	defer re.Close()
	again, e := re.Registry.View(context.Background(), project)
	if e != nil || !reflect.DeepEqual(snap, again) {
		t.Fatalf("restart changed facts: %v", e)
	}
	_, e = local.Query(context.Background(), "count()", false, nil)
	if e == nil {
		t.Fatal("dead Unix service accepted")
	}
	var out, stderr bytes.Buffer
	projection := filepath.Join(filepath.Dir(h.socket), "projection.json")
	b := []byte(`{"endpoint":"` + h.endpoint + `","project_id":"synthetic-board","account_id":"synthetic-admin"}`)
	if e = os.WriteFile(projection, b, 0600); e != nil {
		t.Fatal(e)
	}
	if n := app.RunClient(context.Background(), []string{"--synthetic-local-projection", projection, "q", "count()"}, build, &out, &stderr); n != 1 {
		t.Fatalf("dead service exit %d: %s", n, stderr.String())
	}
}

func TestIntegratedUnixCurrentPolicySpoofRefusalAndTLSFailure(t *testing.T) {
	h := transports(t)
	c, spy := h.localClient(t)
	if h.accepted.Load() != 1 {
		t.Fatalf("preflight did not reuse connection: %d", h.accepted.Load())
	}
	h.peer.mu.Lock()
	h.peer.deny = true
	h.peer.mu.Unlock()
	_, e := c.Query(context.Background(), "count()", false, nil)
	code(t, e, "unauthorized")
	if h.accepted.Load() != 1 {
		t.Fatal("revocation witness did not use original connection")
	}
	h.peer.mu.Lock()
	h.peer.deny = false
	h.peer.viewer = true
	h.peer.mu.Unlock()
	zero := contract.Revision(0)
	_, e = c.Query(context.Background(), `create(type=epic,name="refused")`, true, &zero)
	code(t, e, "forbidden")
	h.peer.mu.Lock()
	h.peer.viewer = false
	h.peer.unready = true
	h.peer.mu.Unlock()
	raw := h.rawClient(t)
	if n := rawStatus(t, raw, "/ready", nil); n != 503 {
		t.Fatalf("peer unavailable readiness: %d", n)
	}
	h.peer.mu.Lock()
	h.peer.unready = false
	h.peer.mu.Unlock()
	for _, header := range []string{"Authorization", "X-Peer-UID", "X-Account-ID", "X-Forwarded-Uid"} {
		if n := rawStatus(t, raw, "/v1/me", map[string]string{header: "synthetic-spoof"}); n == 200 {
			t.Fatalf("spoof %s accepted", header)
		}
	}
	if n := rawStatus(t, raw, "/v1/version", map[string]string{"Authorization": "Bearer swarma-board-synthetic-admin"}); n == 200 {
		t.Fatal("local Bearer fallback")
	}
	if _, e = h.remoteClient(t, nil); e == nil {
		t.Fatal("untrusted TLS accepted")
	}
	if _, e = h.remoteClient(t, &tls.Config{InsecureSkipVerify: true}); e == nil {
		t.Fatal("unverified TLS config accepted")
	}
	remote, e := h.remoteClient(t, h.tls)
	if e != nil {
		t.Fatal(e)
	}
	defer remote.Close()
	query(t, remote, "count()")
	snap, events, e := h.app.Registry.ExportView(context.Background(), project)
	if e != nil || snap.Revision != 0 || len(events) != 0 {
		t.Fatal("refusal changed data or journal")
	}
	if spy.calls.Load() != 0 {
		t.Fatal("Unix credential fallback")
	}
	// A missing protected resolver cannot produce a listener/server.
	if _, e = app.NewUnixServer(h.app, nil, build); e == nil {
		t.Fatal("missing authority accepted")
	}
}

func TestIntegratedUnixMissingKernelHookCannotFallThroughToBearer(t *testing.T) {
	h := transports(t)
	// Deliberately miscompose the dedicated local handler without ConnContext.
	// Even a valid synthetic remote bearer cannot rescue this missing kernel UID.
	dir, e := os.MkdirTemp("", "board-hook-refusal-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "bad.sock")
	l, e := app.ListenUnix(path)
	if e != nil {
		t.Fatal(e)
	}
	bad := &http.Server{Handler: h.local.Handler}
	done := make(chan struct{})
	go func() { defer close(done); bad.Serve(l) }()
	defer func() { bad.Close(); l.Close(); <-done }()
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	defer tr.CloseIdleConnections()
	c := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	for _, headers := range []map[string]string{nil, {"Authorization": "Bearer swarma-board-synthetic-admin"}} {
		if n := rawStatus(t, c, "/v1/me", headers); n == 200 {
			t.Fatal("missing kernel hook accepted")
		}
	}
}
