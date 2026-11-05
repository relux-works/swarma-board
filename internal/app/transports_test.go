package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func privateSocket(t *testing.T) string {
	t.Helper()
	dir, e := os.MkdirTemp("", "board-socket-policy-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	dir, e = filepath.EvalSymlinks(dir)
	if e != nil {
		t.Fatal(e)
	}
	return filepath.Join(dir, "s.sock")
}

func TestListenUnixRechecksAfterReadOnlyPreflight(t *testing.T) {
	path := privateSocket(t)
	if err := preflightUnixSocket(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("preflight created a socket", err)
	}
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if l, err := ListenUnix(path); err == nil {
		l.Close()
		t.Fatal("parent mode changed after preflight but listener accepted")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := preflightUnixSocket(path); err != nil {
		t.Fatal(err)
	}
	foreign, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if l, err := ListenUnix(path); err == nil {
		l.Close()
		t.Fatal("socket occupied after preflight but listener accepted")
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() {
		t.Fatal("foreign socket changed", err)
	}
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatal("foreign listener no longer reachable", err)
	}
	conn.Close()
}

func TestUnixSocketPolicyNeverReplacesExistingAndPreservesReplacement(t *testing.T) {
	path := privateSocket(t)
	if e := os.WriteFile(path, []byte("existing"), 0600); e != nil {
		t.Fatal(e)
	}
	if l, e := ListenUnix(path); e == nil {
		l.Close()
		t.Fatal("existing file replaced")
	}
	b, _ := os.ReadFile(path)
	if string(b) != "existing" {
		t.Fatal("existing file changed")
	}
	os.Remove(path)
	old, e := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if e != nil {
		t.Fatal(e)
	}
	old.SetUnlinkOnClose(false)
	old.Close()
	if l, e := ListenUnix(path); e == nil {
		l.Close()
		t.Fatal("stale socket removed")
	}
	os.Remove(path)
	l, e := ListenUnix(path)
	if e != nil {
		t.Fatal(e)
	}
	st, e := os.Lstat(path)
	if e != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("socket mode: %v %v", st, e)
	}
	if second, e := ListenUnix(path); e == nil {
		second.Close()
		t.Fatal("active socket replaced")
	}
	// Simulate a shared-UID actor replacing the path: cleanup must preserve it.
	os.Remove(path)
	if e = os.WriteFile(path, []byte("replacement"), 0600); e != nil {
		t.Fatal(e)
	}
	l.Close()
	b, e = os.ReadFile(path)
	if e != nil || string(b) != "replacement" {
		t.Fatal("cleanup removed replacement inode")
	}
}
func TestUnixSocketRefusesNonprivateSymlinkAndNoncanonicalParent(t *testing.T) {
	path := privateSocket(t)
	dir := filepath.Dir(path)
	os.Chmod(dir, 0755)
	if l, e := ListenUnix(path); e == nil {
		l.Close()
		t.Fatal("public parent accepted")
	}
	os.Chmod(dir, 0700)
	link := dir + "-link"
	if e := os.Symlink(dir, link); e != nil {
		t.Fatal(e)
	}
	defer os.Remove(link)
	for _, bad := range []string{"relative.sock", filepath.Join(link, "s.sock"), dir + "/../" + filepath.Base(dir) + "/s.sock"} {
		if l, e := ListenUnix(bad); e == nil {
			l.Close()
			t.Fatalf("unsafe socket path accepted: %s", bad)
		}
	}
}
func TestServeCancellationDrainsBeforeStorageOwnerCanClose(t *testing.T) {
	path := privateSocket(t)
	l, e := ListenUnix(path)
	if e != nil {
		t.Fatal(e)
	}
	admitted := make(chan struct{})
	release := make(chan struct{})
	s := boundedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(admitted)
		<-release
		io.WriteString(w, "completed")
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- ServeUntilCanceled(ctx, s, l) }()
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	defer tr.CloseIdleConnections()
	response := make(chan error, 1)
	go func() {
		r, e := (&http.Client{Transport: tr, Timeout: 5 * time.Second}).Get("http://socket/")
		if e == nil {
			b, err := io.ReadAll(r.Body)
			r.Body.Close()
			e = err
			if string(b) != "completed" {
				e = unavailable()
			}
		}
		response <- e
	}()
	select {
	case <-admitted:
	case <-time.After(5 * time.Second):
		t.Fatal("not admitted")
	}
	cancel()
	select {
	case e := <-done:
		t.Fatalf("returned before drain: %v", e)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if e := <-response; e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("drain stuck")
	}
	if _, e = os.Lstat(path); !os.IsNotExist(e) {
		t.Fatal("socket leaked after drain")
	}
}
func TestTransportExecutableInvalidInputsDoNotProvision(t *testing.T) {
	for _, args := range [][]string{
		{"--unix-socket", "relative.sock", "--synthetic-peer-account", "synthetic-admin", "--synthetic-peer-project", "synthetic-board"},
		{"--listen", "0.0.0.0:0", "--tls-cert", "missing", "--tls-key", "missing"},
		{"--listen", "127.0.0.1:0", "--tls-cert", "missing", "--tls-key", "missing"},
		{"--unix-socket", "/unused.sock", "--listen", "127.0.0.1:0"},
	} {
		dir := filepath.Join(t.TempDir(), "untouched")
		var out bytes.Buffer
		a := append([]string{"--synthetic-fixture", "../testadapter/testdata/server.json", "--data-dir", dir}, args...)
		if RunServer(context.Background(), a, "dev", &out) == 0 {
			t.Fatal("invalid startup accepted")
		}
		if _, e := os.Stat(dir); !os.IsNotExist(e) {
			t.Fatalf("invalid transport provisioned storage: %s", out.String())
		}
	}
	if _, e := NewVerifiedTLSServer(nil, &tls.Config{}); e == nil {
		t.Fatal("missing TLS handler/cert accepted")
	}
	var out, err bytes.Buffer
	if n := RunClient(context.Background(), []string{"--synthetic-local-projection", "x", "--synthetic-tls-ca", "y", "q", "count()"}, "dev", &out, &err); n != 2 {
		t.Fatalf("mixed transports: %d", n)
	}
}
