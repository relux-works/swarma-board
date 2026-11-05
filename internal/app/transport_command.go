package app

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"

	"github.com/relux-works/swarma-board/internal/auth"
	"github.com/relux-works/swarma-board/internal/contract"
)

// RunServer exposes explicit synthetic qualification only. Production startup,
// protected mapping custody and maintenance export remain separate gated work.
func RunServer(ctx context.Context, args []string, commit string, out io.Writer) (exit int) {
	if out == nil {
		out = os.Stderr
	}
	transport := false
	for _, a := range args {
		if strings.HasPrefix(a, "--unix-socket") || strings.HasPrefix(a, "--tls-") || strings.HasPrefix(a, "--synthetic-peer-") {
			transport = true
		}
	}
	if !transport {
		return RunSyntheticServer(ctx, args, commit, out, ConcreteSynthetic)
	}
	fs := flag.NewFlagSet("swarma-board-server", flag.ContinueOnError)
	fs.SetOutput(out)
	fixture := fs.String("synthetic-fixture", "", "Explicit synthetic provisioning fixture")
	dir := fs.String("data-dir", "", "Explicit synthetic storage directory")
	socket := fs.String("unix-socket", "", "Absolute socket in an existing private directory")
	account := fs.String("synthetic-peer-account", "", "Fake current-UID account; no per-agent isolation")
	project := fs.String("synthetic-peer-project", "", "Fake single-project admin grant")
	listen := fs.String("listen", "", "Explicit synthetic TLS literal-loopback address")
	cert := fs.String("tls-cert", "", "Explicit synthetic server certificate")
	key := fs.String("tls-key", "", "Explicit synthetic server key")
	if e := fs.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	local := *socket != ""
	if fs.NArg() != 0 || *fixture == "" || *dir == "" || (local && (*listen != "" || *cert != "" || *key != "" || *account == "" || *project == "")) || (!local && (*listen == "" || *cert == "" || *key == "" || *account != "" || *project != "")) {
		fmt.Fprintln(out, "unavailable: explicit unambiguous synthetic Unix or TLS dependencies required")
		return 2
	}
	// Validate transport authority before provisioning any storage.
	var config *tls.Config
	if local {
		// Validate the endpoint with the client's canonical rules without dialing.
		if e := validateUnixEndpoint(*socket); e != nil {
			fmt.Fprintln(out, "unavailable: synthetic socket policy refused")
			return 1
		}
	} else {
		if e := auth.ValidateSyntheticListen(*listen); e != nil {
			fmt.Fprintln(out, "unavailable: synthetic TLS listen policy refused")
			return 1
		}
		pair, e := tls.LoadX509KeyPair(*cert, *key)
		if e != nil {
			fmt.Fprintln(out, "unavailable: synthetic TLS certificate unavailable")
			return 1
		}
		config = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}}
	}
	f, e := os.Open(*fixture)
	if e != nil {
		fmt.Fprintln(out, "unavailable: synthetic fixture unavailable")
		return 1
	}
	s, e := ConcreteSynthetic(ctx, SyntheticOptions{Fixture: f, DataDir: *dir, Listen: "127.0.0.1:0", Commit: commit})
	f.Close()
	if e != nil {
		fmt.Fprintln(out, "unavailable: synthetic startup refused")
		return 1
	}
	defer func() {
		if e := s.Close(); e != nil && exit == 0 {
			fmt.Fprintln(out, "unavailable: storage close failed")
			exit = 1
		}
	}()
	server := s.Server
	var listener net.Listener
	if local {
		// Snapshot validates that the explicit synthetic grant names a provisioned project.
		snap, err := s.Service.Snapshot(ctx, *project)
		if err != nil {
			fmt.Fprintln(out, "unavailable: synthetic peer project refused")
			return 1
		}
		resolver := &SyntheticPeerResolver{UID: uint32(os.Geteuid()), Account: *account, Project: contract.Project{ID: snap.Project.ID, Name: snap.Project.Name, Description: snap.Project.Description}}
		server, e = NewUnixServer(s, resolver, commit)
		if e == nil {
			listener, e = ListenUnix(*socket)
		}
	} else {
		server, e = NewVerifiedTLSServer(s.Handler, config)
		if e == nil {
			listener, e = net.Listen("tcp", *listen)
			if e == nil {
				listener = tls.NewListener(listener, server.TLSConfig)
			}
		}
	}
	if e != nil {
		fmt.Fprintln(out, "unavailable: synthetic transport startup refused")
		return 1
	}
	defer listener.Close()
	mode := "verified TLS"
	if local {
		mode = "Unix kernel UID with synthetic resolver; shared UID is not per-agent isolation"
	}
	if _, e = fmt.Fprintf(out, "synthetic fixture mode (%s); listening %s\n", mode, listener.Addr()); e != nil {
		return 1
	}
	if e = ServeUntilCanceled(ctx, server, listener); e != nil {
		fmt.Fprintln(out, "unavailable: synthetic service stopped; ask Host/launcher to recover service")
		return 1
	}
	return 0
}
