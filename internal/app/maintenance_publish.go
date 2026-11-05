package app

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/relux-works/swarma-board/internal/maintenance"
)

// publishCheckpoint stages only under an existing private canonical parent.
// The Host must protect that parent and its ancestors throughout maintenance.
// All files and the staging directory are synced before atomic no-replace
// publication, followed by parent sync. A failed post-rename sync leaves a
// complete directory but reports failure; the Host must inspect before retry.
// A process death before publication may leave a private .maintenance-incomplete-*
// directory. No cleanup of another invocation's staging is attempted.
func publishCheckpoint(ctx context.Context, dest string, b maintenance.Bundle) error {
	parent := filepath.Dir(dest)
	canonical, err := filepath.EvalSymlinks(parent)
	if err != nil || canonical != parent {
		return errors.New("output parent must be canonical")
	}
	dir, err := os.Open(parent)
	if err != nil {
		return err
	}
	defer dir.Close()
	st, err := dir.Stat()
	if err != nil || !st.IsDir() || st.Mode().Perm() != 0700 {
		return errors.New("output parent must be private")
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return err
	}
	defer root.Close()
	actual, err := root.Stat(".")
	if err != nil || !os.SameFile(st, actual) {
		return errors.New("output parent changed")
	}
	// This is an early refusal only; atomic no-replace at publication is decisive.
	if _, err = root.Lstat(filepath.Base(dest)); !errors.Is(err, os.ErrNotExist) {
		return errors.New("output already exists or is unavailable")
	}
	stage := ".maintenance-incomplete-" + rand.Text()
	if err = root.Mkdir(stage, 0700); err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			// Remove only the exact private names created by this invocation.
			for _, name := range []string{maintenance.ManifestName, maintenance.SnapshotName, maintenance.JournalName} {
				_ = root.Remove(filepath.Join(stage, name))
			}
			_ = root.Remove(stage)
		}
	}()
	for _, artifact := range []struct {
		name string
		data []byte
	}{{maintenance.SnapshotName, b.Snapshot}, {maintenance.JournalName, b.Journal}, {maintenance.ManifestName, b.Manifest}} {
		if err = ctx.Err(); err != nil {
			return err
		}
		f, e := root.OpenFile(filepath.Join(stage, artifact.name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return e
		}
		n, e := f.Write(artifact.data)
		if e == nil && n != len(artifact.data) {
			e = io.ErrShortWrite
		}
		if e == nil {
			e = f.Sync()
		}
		closeErr := f.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
	}
	staging, err := root.Open(stage)
	if err != nil {
		return err
	}
	err = staging.Sync()
	closeErr := staging.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = renameCheckpointNoReplace(dir, stage, filepath.Base(dest)); err != nil {
		return err
	}
	published = true
	return dir.Sync()
}
