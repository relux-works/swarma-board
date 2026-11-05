//go:build (linux && amd64) || darwin

package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/relux-works/swarma-board/internal/maintenance"
)

func TestCheckpointAtomicNoReplaceAndCanceledStaging(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, name := range []string{"stage", "occupied"} {
		if err = os.Mkdir(filepath.Join(parent, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	// Exercise the final syscall against an empty target, bypassing preflight.
	// An overwrite-capable rename would silently replace this directory.
	if err = renameCheckpointNoReplace(f, "stage", "occupied"); err == nil {
		t.Fatal("replaced existing empty output")
	}
	for _, name := range []string{"stage", "occupied"} {
		if _, err = os.Stat(filepath.Join(parent, name)); err != nil {
			t.Fatal("lost directory", name, err)
		}
	}
	if err = renameCheckpointNoReplace(f, "stage", "new"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = publishCheckpoint(ctx, filepath.Join(parent, "canceled"), maintenance.Bundle{}); err == nil {
		t.Fatal("published after cancellation")
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 2 || entries[0].Name() != "new" || entries[1].Name() != "occupied" {
		t.Fatal("canceled staging leaked or published", entries, err)
	}
}
