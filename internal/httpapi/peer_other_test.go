//go:build !linux && !darwin

package httpapi

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestPeerUnqualifiedPlatformRefuses(t *testing.T) {
	if _, err := acceptedUnixUID(nil); err == nil {
		t.Fatal("unqualified platform admitted peer identity")
	}
	dir, err := os.MkdirTemp("", "peer-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "s"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	client, err := net.DialUnix("unix", nil, l.Addr().(*net.UnixAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	accepted, err := l.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer accepted.Close()
	if _, err := acceptedUnixUID(accepted); err == nil {
		t.Fatal("unqualified platform admitted real peer")
	}
}
