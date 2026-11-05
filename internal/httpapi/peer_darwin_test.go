//go:build darwin

package httpapi

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

type wrappedDarwinConn struct{ net.Conn }

func TestDarwinAcceptedPeerAndExtractionRefusals(t *testing.T) {
	dir, err := os.MkdirTemp("", "peer-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
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
	if uid, err := acceptedUnixUID(accepted); err != nil || uid != uint32(os.Geteuid()) {
		t.Fatal("accepted kernel peer UID mismatch", uid, err)
	}
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	var typedNil *net.UnixConn
	for _, conn := range []net.Conn{nil, typedNil, a, wrappedDarwinConn{accepted}} {
		if _, err := acceptedUnixUID(conn); err == nil {
			t.Fatal("non-kernel connection admitted")
		}
	}
	// Real TCP must refuse even when it belongs to the same effective UID.
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	tcpClient, err := net.Dial("tcp", tcp.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer tcpClient.Close()
	tcpAccepted, err := tcp.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer tcpAccepted.Close()
	if _, err := acceptedUnixUID(tcpAccepted); err == nil {
		t.Fatal("TCP admitted")
	}
	accepted.Close()
	if _, err := acceptedUnixUID(accepted); err == nil {
		t.Fatal("closed socket admitted")
	}
}

func TestDarwinUnknownCredentialsRefuse(t *testing.T) {
	if uid, err := darwinCredentialUID(&unix.Xucred{Uid: 42, Ngroups: 1}); err != nil || uid != 42 {
		t.Fatal("valid credential rejected", uid, err)
	}
	for _, cred := range []*unix.Xucred{nil, {}, {Version: 1, Ngroups: 1}, {Ngroups: -1}, {Ngroups: 17}} {
		if _, err := darwinCredentialUID(cred); err == nil {
			t.Fatal("uncertain credential admitted", cred)
		}
	}
}
