package cli

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/relux-works/swarma-board/internal/client"
	"github.com/relux-works/swarma-board/internal/contract"
	"github.com/relux-works/swarma-board/internal/platform"
	"github.com/spf13/cobra"
)

// Options are explicit composition inputs. Commit is supplied by P5's build;
// empty means dev. Streams default to the process stdout/stderr, never files.
type Options struct {
	Projection contract.ProjectionSource
	Credential contract.CredentialSource
	TLSConfig  *tls.Config
	Commit     string
	Out, Err   io.Writer
}

func local(message string) error { return client.LocalError("invalid_request", message) }

// NewCommand returns a fresh Cobra tree. Call Run for safe envelope/exit handling.
func NewCommand(o Options) *cobra.Command {
	if o.Out == nil {
		o.Out = os.Stdout
	}
	if o.Err == nil {
		o.Err = os.Stderr
	}
	if o.Commit == "" {
		o.Commit = "dev"
	}
	root := &cobra.Command{Use: "swarma-board", Short: "Query and edit task facts through the swarma-board v1 service", SilenceErrors: true, SilenceUsage: true, RunE: func(*cobra.Command, []string) error { return local("a product command is required") }}
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetOut(o.Out)
	root.SetErr(o.Err)
	root.SetFlagErrorFunc(func(*cobra.Command, error) error { return local("invalid command flags") })
	var asJSON bool
	var fixture string
	var fd int
	root.PersistentFlags().BoolVar(&asJSON, "json", false, "Write JSON results")
	root.PersistentFlags().StringVar(&fixture, "synthetic-projection", "", "Explicit synthetic projection fixture")
	root.PersistentFlags().IntVar(&fd, "synthetic-credential-fd", -1, "Explicit synthetic bearer fd (consumed once and closed)")
	connect := func(cmd *cobra.Command) (*client.Client, error) {
		p, c := o.Projection, o.Credential
		fixtureSet := root.PersistentFlags().Changed("synthetic-projection")
		fdSet := root.PersistentFlags().Changed("synthetic-credential-fd")
		if fixtureSet || fdSet {
			if !fixtureSet || !fdSet || fixture == "" || fd < 0 || p != nil || c != nil {
				return nil, local("synthetic projection and credential fd must be explicit and unambiguous")
			}
			f := os.NewFile(uintptr(fd), "synthetic-credential")
			var err error
			p, c, err = platform.Synthetic(cmd.Context(), fixture, f)
			if err != nil {
				return nil, err
			}
			if _, err = io.WriteString(o.Err, "synthetic fixture mode\n"); err != nil {
				return nil, local("cannot write mode notice")
			}
		}
		return client.New(cmd.Context(), p, c, client.Options{TLSConfig: o.TLSConfig})
	}
	emit := func(v any) error {
		enc := json.NewEncoder(o.Out)
		enc.SetEscapeHTML(false)
		if !asJSON {
			enc.SetIndent("", "  ")
		}
		if err := enc.Encode(v); err != nil {
			return outputFailure("cannot write command result")
		}
		return nil
	}
	argsExactly := func(n int) func(*cobra.Command, []string) error {
		return func(_ *cobra.Command, a []string) error {
			if len(a) != n {
				return local("invalid number of command arguments")
			}
			return nil
		}
	}
	for _, name := range []string{"q", "m"} {
		mutation := name == "m"
		var expected string
		cmd := &cobra.Command{Use: name + " QUERY", Short: map[bool]string{false: "Execute one read-only query", true: "Execute one mutation with a revision precondition"}[mutation], Args: argsExactly(1)}
		if mutation {
			cmd.Flags().StringVar(&expected, "if-match", "", "Expected selected-project revision")
		}
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			op, err := Validate(args[0], mutation)
			if err != nil {
				return err
			}
			rev, err := revision(expected, cmd.Flags().Changed("if-match"))
			if err != nil {
				return err
			}
			c, err := connect(cmd)
			if err != nil {
				return err
			}
			defer c.Close()
			r, err := c.Query(cmd.Context(), args[0], mutation, rev)
			if err != nil {
				return err
			}
			if op.Name == "plan" {
				p, err := decodePlan(r.JSON)
				if err != nil {
					return err
				}
				if !*p.Usable {
					if err := emit(r.JSON); err != nil {
						return err
					}
					return unusable(r.RequestID)
				}
			}
			return emit(r.JSON)
		}
		root.AddCommand(cmd)
	}
	var scope, mode string
	var active, critical bool
	var phase int
	plan := &cobra.Command{Use: "plan [ID]", Short: "Read a plan, its critical path or one phase", Args: func(_ *cobra.Command, a []string) error {
		if len(a) > 1 {
			return local("plan accepts at most one ID")
		}
		return nil
	}}
	plan.Flags().StringVar(&scope, "scope", "", "Comma-separated scope roots (excludes positional ID)")
	plan.Flags().StringVar(&mode, "mode", "", "children or related")
	plan.Flags().BoolVar(&active, "active", false, "Exclude terminal phase nodes")
	plan.Flags().BoolVar(&critical, "critical-path", false, "Show the fetched plan's critical path")
	plan.Flags().IntVar(&phase, "phase", 0, "Show one fetched phase")
	plan.RunE = func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().Changed("scope") && len(args) > 0 {
			return local("--scope conflicts with positional ID")
		}
		if cmd.Flags().Changed("critical-path") && cmd.Flags().Changed("phase") {
			return local("--critical-path conflicts with --phase")
		}
		if cmd.Flags().Changed("phase") && phase < 1 {
			return local("phase must be positive")
		}
		var parts []string
		if cmd.Flags().Changed("scope") {
			ids := strings.Split(scope, ",")
			for _, id := range ids {
				if !validRoot(id) {
					return local("invalid scope root")
				}
			}
			b, _ := json.Marshal(ids)
			parts = append(parts, "scope="+string(b))
		} else if len(args) > 0 {
			if !validRoot(args[0]) {
				return local("invalid plan ID")
			}
			parts = append(parts, "scope="+strconv.Quote(args[0]))
		}
		if cmd.Flags().Changed("mode") {
			if !oneOf(mode, "children related") {
				return local("invalid plan mode")
			}
			parts = append(parts, "mode="+mode)
		}
		if active {
			parts = append(parts, "active=true")
		}
		query := "plan(" + strings.Join(parts, ",") + ")"
		if _, err := Validate(query, false); err != nil {
			return err
		}
		c, err := connect(cmd)
		if err != nil {
			return err
		}
		defer c.Close()
		r, err := c.Query(cmd.Context(), query, false, nil)
		if err != nil {
			return err
		}
		p, err := decodePlan(r.JSON)
		if err != nil {
			return err
		}
		if !*p.Usable {
			if err := emit(r.JSON); err != nil {
				return err
			}
			return unusable(r.RequestID)
		}
		if critical {
			return emit(map[string][]string{"criticalPath": p.CriticalPath})
		}
		if cmd.Flags().Changed("phase") {
			for _, p := range p.Phases {
				if p.Phase == phase {
					return emit(p)
				}
			}
			return local("requested phase does not exist")
		}
		return emit(r.JSON)
	}
	root.AddCommand(plan)
	var dry, accept bool
	var sha, importRev string
	imp := &cobra.Command{Use: "import FILE", Short: "Validate or commit an explicitly supplied snapshot", Args: argsExactly(1)}
	imp.Flags().BoolVar(&dry, "dry-run", false, "Validate without writing")
	imp.Flags().BoolVar(&accept, "accept-exclusions", false, "Consent to reported structured metadata exclusions")
	imp.Flags().StringVar(&sha, "sha256", "", "Expected archive digest from validation")
	imp.Flags().StringVar(&importRev, "if-match", "", "Expected selected-project revision")
	imp.RunE = func(cmd *cobra.Command, args []string) error {
		rev, err := revision(importRev, cmd.Flags().Changed("if-match"))
		if err != nil {
			return err
		}
		if dry && (rev != nil || accept) {
			return local("dry-run does not accept commit preconditions or exclusion consent")
		}
		b, err := readArchive(args[0])
		if err != nil {
			return err
		}
		if err := validateDigest(b, sha, dry); err != nil {
			return err
		}
		c, err := connect(cmd)
		if err != nil {
			return err
		}
		defer c.Close()
		r, err := c.Import(cmd.Context(), b, client.ImportOptions{DryRun: dry, SHA256: sha, IfMatch: rev, AcceptExclusions: accept})
		if err != nil {
			return err
		}
		return emit(r.JSON)
	}
	root.AddCommand(imp)
	exp := &cobra.Command{Use: "export FILE", Short: "Write a verified native snapshot to a new private file", Args: argsExactly(1), RunE: func(cmd *cobra.Command, args []string) error {
		if _, err := os.Lstat(args[0]); err == nil {
			return local("export destination already exists")
		} else if !os.IsNotExist(err) {
			return local("export destination is unavailable")
		}
		c, err := connect(cmd)
		if err != nil {
			return err
		}
		defer c.Close()
		b, rev, err := c.Export(cmd.Context())
		if err != nil {
			return err
		}
		if err = writeArchive(args[0], b); err != nil {
			return err
		}
		return emit(map[string]any{"export": map[string]any{"sha256": digest(b), "revision": rev}})
	}}
	root.AddCommand(exp)
	var server bool
	ver := &cobra.Command{Use: "version", Short: "Show local version or verified server version", Args: argsExactly(0), RunE: func(cmd *cobra.Command, _ []string) error {
		if !server {
			return emit(map[string]any{"product": "swarma-board", "commit": o.Commit})
		}
		c, err := connect(cmd)
		if err != nil {
			return err
		}
		defer c.Close()
		return emit(c.ServerVersion())
	}}
	ver.Flags().BoolVar(&server, "server", false, "Verify server capabilities and account")
	root.AddCommand(ver)
	return root
}
func revision(value string, present bool) (*contract.Revision, error) {
	if !present {
		return nil, nil
	}
	if value == "" {
		return nil, local("--if-match requires an unsigned revision")
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return nil, local("--if-match requires an unsigned revision")
		}
	}
	n, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return nil, local("--if-match revision exceeds uint64")
	}
	v := contract.Revision(n)
	return &v, nil
}

// Run centralizes process-independent exit handling; it never calls os.Exit.
func Run(ctx context.Context, args []string, o Options) int {
	root := NewCommand(o)
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	e, ok := err.(*client.Error)
	if !ok {
		e = client.LocalError("invalid_request", "invalid command arguments")
	}
	w := o.Err
	if w == nil {
		w = os.Stderr
	}
	_ = json.NewEncoder(w).Encode(e)
	return client.ExitCode(e)
}

func unusable(id string) error {
	return &client.Error{Code: "unusable_plan", Message: "plan has dependency problems or task cycles", Details: map[string]any{}, RequestID: id}
}

func outputFailure(message string) error {
	return &client.Error{Code: "output_error", Message: message, Details: map[string]any{}, RequestID: client.RequestID()}
}
