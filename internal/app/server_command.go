package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

// RunSyntheticServer is the server executable's explicit synthetic harness.
// Production and offline signed export require follow-up contracts; no default
// fixture, UID mapping, signer or transport is inferred here.
func RunSyntheticServer(ctx context.Context, args []string, commit string, out io.Writer, compose func(context.Context, SyntheticOptions) (*Synthetic, error)) (exit int) {
	fs := flag.NewFlagSet("swarma-board-server", flag.ContinueOnError)
	fs.SetOutput(out)
	fixture := fs.String("synthetic-fixture", "", "Explicit hash-only synthetic fixture")
	listen := fs.String("listen", "", "Explicit literal-loopback address:port")
	dir := fs.String("data-dir", "", "Explicit private synthetic storage directory")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 || *fixture == "" || *listen == "" || *dir == "" {
		fmt.Fprintln(out, "unavailable: explicit --synthetic-fixture, --listen and --data-dir required; production composition is gated")
		return 2
	}
	if compose == nil {
		fmt.Fprintln(out, "unavailable: server composition missing")
		return 1
	}
	f, err := os.Open(*fixture)
	if err != nil {
		fmt.Fprintln(out, "unavailable: synthetic fixture unavailable")
		return 1
	}
	s, err := compose(ctx, SyntheticOptions{Fixture: f, Listen: *listen, DataDir: *dir, Commit: commit})
	_ = f.Close()
	if err != nil {
		fmt.Fprintln(out, "unavailable: synthetic startup refused")
		return 1
	}
	defer func() {
		if err := s.Close(); err != nil && exit == 0 {
			fmt.Fprintln(out, "unavailable: storage close failed")
			exit = 1
		}
	}()
	defer s.Server.Close()
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintln(out, "unavailable: synthetic listener unavailable")
		return 1
	}
	defer ln.Close()
	if _, err = fmt.Fprintf(out, "synthetic fixture mode; listening %s\n", ln.Addr()); err != nil {
		return 1
	}
	done := make(chan error, 1)
	go func() { done <- s.Server.Serve(ln) }()
	select {
	case err = <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return 0
		}
		fmt.Fprintln(out, "unavailable: synthetic server stopped")
		return 1
	case <-ctx.Done():
		drain, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err = s.Server.Shutdown(drain); err != nil {
			_ = s.Server.Close()
		}
		<-done
		if err != nil {
			fmt.Fprintln(out, "unavailable: shutdown deadline exceeded")
			return 1
		}
		return 0
	}
}
