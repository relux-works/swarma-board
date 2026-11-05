package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/relux-works/swarma-board/internal/cli"
	"github.com/relux-works/swarma-board/internal/client"
	"github.com/relux-works/swarma-board/internal/platform"
)

func validateUnixEndpoint(path string) error {
	t, e := client.NewUnixTransport("unix://" + path)
	if e == nil {
		t.CloseIdleConnections()
	}
	return e
}

// RunClient adds explicit executable-only synthetic transport inputs. It never
// reads ambient projection, credentials or local board files. Product commands
// and their validation stay owned by cli. TLS verification remains mandatory.
func RunClient(ctx context.Context, args []string, commit string, out, errOut io.Writer) int {
	if out == nil {
		out = os.Stdout
	}
	if errOut == nil {
		errOut = os.Stderr
	}
	o := cli.Options{Commit: commit, Out: out, Err: errOut}
	var local, ca string
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, value, equals := strings.Cut(a, "=")
		if name != "--synthetic-local-projection" && name != "--synthetic-tls-ca" {
			rest = append(rest, a)
			continue
		}
		if !equals {
			i++
			if i == len(args) {
				fmt.Fprintln(errOut, "invalid_request: explicit synthetic transport input required")
				return 2
			}
			value = args[i]
		}
		target := &local
		if name == "--synthetic-tls-ca" {
			target = &ca
		}
		if *target != "" || value == "" {
			fmt.Fprintln(errOut, "invalid_request: ambiguous synthetic transport input")
			return 2
		}
		*target = value
	}
	refuse := func() int { fmt.Fprintln(errOut, "invalid_request: synthetic transport composition refused"); return 2 }
	if local != "" {
		if ca != "" {
			return refuse()
		}
		for _, a := range rest {
			if strings.HasPrefix(a, "--synthetic-projection") || strings.HasPrefix(a, "--synthetic-credential-fd") {
				return refuse()
			}
		}
		f, e := os.Open(local)
		if e != nil {
			return refuse()
		}
		p, e := platform.Projection(f)
		f.Close()
		if e != nil {
			return refuse()
		}
		projection, e := p.Resolve(ctx)
		if e != nil || !strings.HasPrefix(projection.Endpoint, "unix:///") {
			return refuse()
		}
		t, e := client.NewUnixTransport(projection.Endpoint)
		if e != nil {
			return refuse()
		}
		defer t.CloseIdleConnections()
		o.Projection = p
		o.UnixTransport = t
		fmt.Fprintln(errOut, "synthetic Unix projection; kernel UID mapping is server-owned; shared UID is not per-agent isolation")
	}
	if ca != "" {
		f, e := os.Open(ca)
		if e != nil {
			return refuse()
		}
		b, e := io.ReadAll(io.LimitReader(f, (1<<20)+1))
		f.Close()
		if e != nil || len(b) > 1<<20 {
			return refuse()
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(b) {
			return refuse()
		}
		o.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	}
	code := cli.Run(ctx, rest, o)
	if code == 1 {
		fmt.Fprintln(errOut, "If the selected service is down, ask Host/launcher to recover it; reconcile any unknown write outcome before another write.")
	}
	return code
}
