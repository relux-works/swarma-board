package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/relux-works/swarma-board/internal/auth"
	"github.com/relux-works/swarma-board/internal/deployment"
	"github.com/relux-works/swarma-board/internal/platform"
)

func deploymentSelected(args []string) bool {
	for _, a := range args {
		name, _, _ := strings.Cut(a, "=")
		if strings.HasPrefix(name, "--deployment-") || name == "--policy-file" || name == "--policy-version" || name == "--audit-export-dir" {
			return true
		}
	}
	return false
}

// Parse all deployment options once, including presence independently of value.
// Client product flags remain owned by cli; no synthetic option crosses this seam.
func deploymentArgs(args []string, server bool) (map[string]string, []string, error) {
	values := map[string]string{}
	var rest []string
	for i := 0; i < len(args); i++ {
		name, value, equal := strings.Cut(args[i], "=")
		boolean := name == "--deployment-status" || (server && name == "--remote-tls")
		known := name == "--deployment-profile" || name == "--deployment-status"
		if server {
			switch name {
			case "--unix-socket", "--data-dir", "--policy-file", "--policy-version", "--audit-export-dir", "--listen", "--tls-cert", "--tls-key", "--remote-tls":
				known = true
			}
		} else if name == "--deployment-projection" {
			known = true
		}
		if !known {
			if server || strings.HasPrefix(name, "--deployment-") || strings.HasPrefix(name, "--synthetic-") || name == "--policy-file" || name == "--policy-version" || name == "--audit-export-dir" {
				return nil, nil, deployment.ErrConfig
			}
			rest = append(rest, args[i])
			continue
		}
		if _, exists := values[name]; exists {
			return nil, nil, deployment.ErrConfig
		}
		if boolean {
			if equal {
				return nil, nil, deployment.ErrConfig
			}
			value = "true"
		} else if !equal {
			i++
			if i == len(args) || strings.HasPrefix(args[i], "--") {
				return nil, nil, deployment.ErrConfig
			}
			value = args[i]
		}
		if value == "" {
			return nil, nil, deployment.ErrConfig
		}
		values[name] = value
	}
	if _, err := deployment.Describe(values["--deployment-profile"]); err != nil {
		return nil, nil, err
	}
	if values["--deployment-status"] != "" && (len(values) != 2 || len(rest) != 0) {
		return nil, nil, deployment.ErrConfig
	}
	return values, rest, nil
}

func runDeploymentServer(ctx context.Context, args []string, commit string, status, notices io.Writer) int {
	v, _, err := deploymentArgs(args, true)
	if err != nil {
		fmt.Fprintln(notices, err)
		return 2
	}
	if v["--deployment-status"] != "" {
		if err = deployment.WriteStatus(v["--deployment-profile"], status); err != nil {
			return 1
		}
		return 0
	}
	// Reject every remote activation flag by presence, before any adapter/storage I/O.
	for _, name := range []string{"--listen", "--tls-cert", "--tls-key", "--remote-tls"} {
		if _, exists := v[name]; exists {
			fmt.Fprintln(notices, deployment.ErrConfig)
			return 2
		}
	}
	c := deployment.Config{Profile: v["--deployment-profile"], Endpoint: "unix://" + v["--unix-socket"], DataDir: v["--data-dir"], PolicyFile: v["--policy-file"], ExportDir: v["--audit-export-dir"]}
	plan, err := deployment.Validate(c)
	if err != nil || v["--policy-version"] == "" {
		fmt.Fprintln(notices, deployment.ErrConfig)
		return 2
	}
	resolver, err := auth.NewStaticPeerResolver(c.PolicyFile, v["--policy-version"])
	if err != nil {
		fmt.Fprintln(notices, "unavailable: static release policy refused")
		return 1
	}
	// This startup catalog is not authentication. Each accepted request obtains
	// its actual kernel UID and rechecks the same immutable policy resolver.
	account, err := resolver.ResolvePeer(ctx, uint32(os.Geteuid()))
	if err != nil {
		fmt.Fprintln(notices, "unavailable: static release account refused")
		return 1
	}
	projects, err := resolver.Projects(ctx, account)
	if err != nil {
		fmt.Fprintln(notices, "unavailable: static release catalog refused")
		return 1
	}
	if err = serveSingleTrustUnit(ctx, plan, resolver, projects, commit, notices); err != nil {
		fmt.Fprintln(notices, "unavailable: single-trust-unit service refused; ask Host/launcher to recover service")
		return 1
	}
	return 0
}

func runDeploymentClient(ctx context.Context, args []string, commit string, out, errOut io.Writer) int {
	v, rest, err := deploymentArgs(args, false)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 2
	}
	if v["--deployment-status"] != "" {
		if deployment.WriteStatus(v["--deployment-profile"], out) != nil {
			return 1
		}
		return 0
	}
	if !absoluteClean(v["--deployment-projection"]) {
		fmt.Fprintln(errOut, deployment.ErrConfig)
		return 2
	}
	f, err := os.Open(v["--deployment-projection"])
	if err != nil {
		fmt.Fprintln(errOut, "unavailable: explicit deployment projection refused")
		return 2
	}
	source, err := platform.Projection(f)
	f.Close()
	if err != nil {
		fmt.Fprintln(errOut, "unavailable: explicit deployment projection refused")
		return 2
	}
	projection, err := source.Resolve(ctx)
	if err != nil {
		fmt.Fprintln(errOut, deployment.ErrConfig)
		return 2
	}
	return runSingleTrustClient(ctx, v["--deployment-profile"], projection, rest, commit, out, errOut)
}
