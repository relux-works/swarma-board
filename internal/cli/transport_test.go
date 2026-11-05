package cli

import (
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/relux-works/swarma-board/internal/client"
	"github.com/relux-works/swarma-board/internal/contract"
	"github.com/relux-works/swarma-board/internal/testadapter"
)

func TestCLIUnixTransportComposition(t *testing.T) {
	// This is one current-UID connection, not a per-agent isolation test.
	dir, err := os.MkdirTemp("", "board-cli-uds-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "service.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, exists := r.Header["Authorization"]; exists {
			t.Error("CLI sent Unix bearer")
		}
		if cliPreflight(w, r) {
			return
		}
		w.Header().Set("ETag", `"rev-2"`)
		io.WriteString(w, `{"count":2}`)
	})}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(l) }()
	defer func() { server.Close(); <-done }()
	endpoint := "unix://" + socket
	tr, err := client.NewUnixTransport(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.CloseIdleConnections()
	o := Options{Projection: testadapter.Projection{Value: contract.Projection{Endpoint: endpoint, ProjectID: "synthetic-board", AccountID: "synthetic-account"}}, UnixTransport: tr}
	code, out, stderr := runTest([]string{"q", "count()", "--json"}, o)
	if code != 0 || !strings.Contains(out, `"count":2`) || stderr != "" {
		t.Fatal(code, out, stderr)
	}
	server.Close()
	code, out, stderr = runTest([]string{"q", "count()", "--json"}, o)
	if code != 1 || out != "" || !strings.Contains(stderr, `"code":"unavailable"`) || strings.Contains(stderr, socket) {
		t.Fatal("dead service CLI envelope/exit mismatch", code, out, stderr)
	}
}

func TestCLIInjectedLoopbackIsNotImplicitSynthetic(t *testing.T) {
	// Existing synthetic harnesses must now opt in through Options or the
	// explicit fixture flags. Supplying a bearer alone never enables HTTP.
	o := Options{Projection: testadapter.Projection{Value: contract.Projection{Endpoint: "http://127.0.0.1:1", ProjectID: "synthetic-board", AccountID: "synthetic-account"}}, Credential: cliCredential{}}
	code, out, stderr := runTest([]string{"q", "count()", "--json"}, o)
	if code != 2 || out != "" || !strings.Contains(stderr, "explicit synthetic mode") {
		t.Fatal(code, out, stderr)
	}
}
