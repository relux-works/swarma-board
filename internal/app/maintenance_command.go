package app

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"regexp"

	"github.com/relux-works/swarma-board/internal/contract"
	"github.com/relux-works/swarma-board/internal/maintenance"
)

// BuildOfflineCheckpoint composes the frozen read-only exporter and verifier.
// Host maintenance ownership must exclude writer restart and path replacement
// through publication. This assertion is separate from the database file lock.
func BuildOfflineCheckpoint(ctx context.Context, opts maintenance.Options) (maintenance.Bundle, error) {
	b, err := maintenance.Export(ctx, opts)
	if err != nil {
		return maintenance.Bundle{}, err
	}
	_, err = maintenance.Verify(ctx, bytes.NewReader(b.Manifest), bytes.NewReader(b.Snapshot), bytes.NewReader(b.Journal), opts.Limits)
	if err != nil {
		return maintenance.Bundle{}, err
	}
	return b, nil
}

func runMaintenanceExport(ctx context.Context, args []string, commit string, out io.Writer) int {
	fs := flag.NewFlagSet("swarma-board-server maintenance-export", flag.ContinueOnError)
	fs.SetOutput(out)
	db := fs.String("database", "", "Absolute existing private schema-v1 database; never initialized")
	project := fs.String("project", "", "Explicit project ID")
	dest := fs.String("output", "", "Absolute NEW checkpoint directory under an existing private output parent")
	stopped := fs.Bool("writer-stopped", false, "Assert Host has stopped writer and excludes restart/path replacement through publication")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 || !*stopped || !absoluteClean(*db) || !absoluteClean(*dest) || *project == "" {
		fmt.Fprintln(out, "unavailable: explicit --database, --project, --output and --writer-stopped required; Host retains maintenance ownership")
		return 2
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(commit) {
		fmt.Fprintln(out, "unavailable: maintenance requires an exact build commit")
		return 1
	}
	b, err := BuildOfflineCheckpoint(ctx, maintenance.Options{Path: *db, Project: contract.Project{ID: *project}, WriterStopped: *stopped, ExporterCommit: commit})
	if err != nil {
		fmt.Fprintln(out, "unavailable: offline read-only export refused; keep writer stopped and ask Host to check source/lock/schema")
		return 1
	}
	if err = publishCheckpoint(ctx, *dest, b); err != nil {
		fmt.Fprintln(out, "unavailable: checkpoint publication not confirmed; Host must inspect output before retry; writer remains stopped")
		return 1
	}
	if _, err = fmt.Fprintln(out, "unsigned checkpoint published; integrity verified; approved external signature pending; writer remains stopped"); err != nil {
		return 1
	}
	return 0
}

func absoluteClean(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && path != string(filepath.Separator)
}
