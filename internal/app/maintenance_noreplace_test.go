//go:build (linux && amd64) || darwin

package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/relux-works/swarma-board/internal/maintenance"
)

func TestCheckpointPublishesCompleteSyntheticBundle(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(parent, "checkpoint")
	bundle := maintenance.Bundle{Snapshot: []byte("synthetic snapshot"), Journal: []byte("synthetic journal"), Manifest: []byte("synthetic manifest")}
	if err := publishCheckpoint(context.Background(), dest, bundle); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{maintenance.SnapshotName: string(bundle.Snapshot), maintenance.JournalName: string(bundle.Journal), maintenance.ManifestName: string(bundle.Manifest)} {
		path := filepath.Join(dest, name)
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Fatal("incomplete checkpoint", name, string(data), err)
		}
		st, err := os.Stat(path)
		if err != nil || st.Mode().Perm() != 0600 {
			t.Fatal("unsafe output mode", name, err)
		}
	}
	if err := publishCheckpoint(context.Background(), dest, maintenance.Bundle{}); err == nil {
		t.Fatal("repeat publication succeeded")
	}
	if data, err := os.ReadFile(filepath.Join(dest, maintenance.SnapshotName)); err != nil || string(data) != string(bundle.Snapshot) {
		t.Fatal("repeat publication overwrote bytes", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 1 || entries[0].Name() != "checkpoint" {
		t.Fatal("staging residue", entries, err)
	}
}

func TestCheckpointNoReplaceAllDestinationKinds(t *testing.T) {
	for _, kind := range []string{"empty-directory", "nonempty-directory", "file", "symlink", "dangling-symlink"} {
		t.Run(kind, func(t *testing.T) {
			parent := t.TempDir()
			f, err := os.Open(parent)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			src, dst := filepath.Join(parent, "stage"), filepath.Join(parent, "dest")
			if err = os.Mkdir(src, 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(src, "sentinel"), []byte("candidate"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "empty-directory", "nonempty-directory":
				err = os.Mkdir(dst, 0700)
				if err == nil && kind == "nonempty-directory" {
					err = os.WriteFile(filepath.Join(dst, "sentinel"), []byte("original"), 0600)
				}
			case "file":
				err = os.WriteFile(dst, []byte("original"), 0600)
			case "symlink":
				err = os.Symlink(src, dst)
			case "dangling-symlink":
				err = os.Symlink("absent", dst)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(dst)
			if err != nil {
				t.Fatal(err)
			}
			// Invoke the final primitive directly, beyond any preflight check.
			if err = renameCheckpointNoReplace(f, "stage", "dest"); !errors.Is(err, os.ErrExist) {
				t.Fatal("occupied destination did not return EEXIST", err)
			}
			after, err := os.Lstat(dst)
			if err != nil || !os.SameFile(before, after) {
				t.Fatal("destination replaced", err)
			}
			if data, err := os.ReadFile(filepath.Join(src, "sentinel")); err != nil || string(data) != "candidate" {
				t.Fatal("source changed on refusal", string(data), err)
			}
			if kind == "file" || kind == "nonempty-directory" {
				name := dst
				if kind == "nonempty-directory" {
					name = filepath.Join(dst, "sentinel")
				}
				if data, err := os.ReadFile(name); err != nil || string(data) != "original" {
					t.Fatal("destination bytes changed", err)
				}
			}
		})
	}
}

func TestCheckpointConcurrentNoReplaceSingleWinner(t *testing.T) {
	parent := t.TempDir()
	f, err := os.Open(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, name := range []string{"a", "b"} {
		if err := os.Mkdir(filepath.Join(parent, name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(parent, name, "sentinel"), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, name := range []string{"a", "b"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			<-start
			results <- renameCheckpointNoReplace(f, name, "winner")
		}(name)
	}
	close(start)
	wg.Wait()
	close(results)
	wins, refusals := 0, 0
	for err := range results {
		if err == nil {
			wins++
		} else if errors.Is(err, os.ErrExist) {
			refusals++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || refusals != 1 {
		t.Fatal("publication was not exclusive", wins, refusals)
	}
	data, err := os.ReadFile(filepath.Join(parent, "winner", "sentinel"))
	if err != nil || (string(data) != "a" && string(data) != "b") {
		t.Fatal("incomplete winner", string(data), err)
	}
	loser := "a"
	if string(data) == "a" {
		loser = "b"
	}
	if data, err := os.ReadFile(filepath.Join(parent, loser, "sentinel")); err != nil || string(data) != loser {
		t.Fatal("loser lost", err)
	}
	f.Close()
	if err := renameCheckpointNoReplace(f, loser, "closed-parent"); err == nil {
		t.Fatal("closed parent admitted")
	}
	if _, err := os.Lstat(filepath.Join(parent, "closed-parent")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("published despite closed parent", err)
	}
}
