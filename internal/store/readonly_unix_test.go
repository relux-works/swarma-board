//go:build unix

package store

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/relux-works/swarma-board/internal/contract"
	bolt "go.etcd.io/bbolt"
)

func TestOfflineReadOnlyNoInitializationAndWriterExclusion(t *testing.T) {
	d, path := openTest(t)
	seed(t, d)
	started := time.Now()
	if other, e := OpenReadOnly(path, project); e == nil {
		other.Close()
		t.Fatal("live writer admitted reader")
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("unbounded read-only lock wait")
	}
	if e := d.Close(); e != nil {
		t.Fatal(e)
	}
	original, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	before := sha256.Sum256(original)
	reader, e := OpenReadOnly(path, project)
	if e != nil {
		t.Fatal(e)
	}
	defer reader.Close()
	s, events, e := reader.ExportView(ctx, "alpha")
	if e != nil || s.Revision != 1 || len(events) != 1 {
		t.Fatal(s, events, e)
	}
	called := false
	_, e = reader.Update(ctx, "alpha", 1, actor, func(contract.Snapshot) (contract.ChangeSet, error) { called = true; return contract.ChangeSet{}, nil })
	if e != bolt.ErrDatabaseReadOnly || called {
		t.Fatal("maintenance reader accepted write", e, called)
	}
	started = time.Now()
	if writer, e := Open(path, project, false); e == nil {
		writer.Close()
		t.Fatal("writer admitted while maintenance lock held")
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("unbounded writer lock wait")
	}
	if e = reader.Close(); e != nil {
		t.Fatal(e)
	}
	after, e := os.ReadFile(path)
	if e != nil || sha256.Sum256(after) != before {
		t.Fatal("maintenance changed database", e)
	}
	writer, e := Open(path, project, false)
	if e != nil {
		t.Fatal(e)
	}
	writer.Close()
}
func TestReadOnlyRefusalNeverCreatesOrInitializes(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.db")
	if d, e := OpenReadOnly(missing, project); e == nil {
		d.Close()
		t.Fatal("missing accepted")
	}
	if _, e := os.Stat(missing); !os.IsNotExist(e) {
		t.Fatal("missing database created", e)
	}
	for _, name := range []string{"empty.db", "legacy.db", "public.db"} {
		path := filepath.Join(dir, name)
		data := []byte{}
		mode := os.FileMode(0600)
		if name == "legacy.db" {
			data = []byte("legacy data")
		}
		if name == "public.db" {
			mode = 0644
		}
		if e := os.WriteFile(path, data, mode); e != nil {
			t.Fatal(e)
		}
		before, _ := os.ReadFile(path)
		if d, e := OpenReadOnly(path, project); e == nil {
			d.Close()
			t.Fatal("invalid file accepted", name)
		}
		after, _ := os.ReadFile(path)
		if sha256.Sum256(before) != sha256.Sum256(after) {
			t.Fatal("refusal changed file", name)
		}
	}
	d, path := openTest(t)
	d.Close()
	link := filepath.Join(dir, "link.db")
	if e := os.Symlink(path, link); e != nil {
		t.Fatal(e)
	}
	if d, e := OpenReadOnly(link, project); e == nil {
		d.Close()
		t.Fatal("symlink accepted")
	}
	if d, e := OpenReadOnly(path, contract.Project{ID: "foreign"}); e == nil {
		d.Close()
		t.Fatal("wrong project accepted")
	}
}
