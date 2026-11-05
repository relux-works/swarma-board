//go:build !darwin && (!linux || !amd64)

package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckpointUnqualifiedPlatformRefuses(t *testing.T) {
	parent := t.TempDir()
	f, err := os.Open(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := os.Mkdir(filepath.Join(parent, "stage"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := renameCheckpointNoReplace(f, "stage", "dest"); err == nil {
		t.Fatal("unqualified platform admitted checkpoint publication")
	}
	if _, err := os.Stat(filepath.Join(parent, "stage")); err != nil {
		t.Fatal("source lost", err)
	}
	if _, err := os.Lstat(filepath.Join(parent, "dest")); !os.IsNotExist(err) {
		t.Fatal("destination created", err)
	}
}
