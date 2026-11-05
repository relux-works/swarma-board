package registry

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/relux-works/swarma-board/internal/contract"
	"github.com/relux-works/swarma-board/internal/store"
)

func TestProvisionIsolationRestartAndDrain(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	r, e := New(dir)
	if e != nil {
		t.Fatal(e)
	}
	projects := []contract.Project{{ID: "alpha", Name: "Original alpha"}, {ID: "beta", Name: "Beta"}}
	if e = r.Provision(ctx, projects); e != nil {
		t.Fatal(e)
	}
	_, e = r.Update(ctx, "alpha", 0, contract.AuditContext{AccountID: "synthetic"}, func(contract.Snapshot) (contract.ChangeSet, error) {
		return contract.ChangeSet{Upserts: []contract.Element{{ID: "TASK-one", Notes: "alpha only"}}}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if e = r.Provision(ctx, []contract.Project{{ID: "alpha", Name: "Never replace metadata"}}); e != nil {
		t.Fatal(e)
	}
	a, _ := r.View(ctx, "alpha")
	b, _ := r.View(ctx, "beta")
	if a.Project.Name != "Original alpha" || a.Revision != 1 || len(a.Elements) != 1 || b.Revision != 0 || len(b.Elements) != 0 {
		t.Fatal(a, b)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	updateDone := make(chan error, 1)
	go func() {
		_, e := r.Update(ctx, "alpha", 1, contract.AuditContext{AccountID: "synthetic"}, func(contract.Snapshot) (contract.ChangeSet, error) {
			close(entered)
			<-release
			return contract.ChangeSet{}, nil
		})
		updateDone <- e
	}()
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- r.Close() }()
	// Observe closing under the registry's mutex rather than relying on a sleep.
	deadline := time.Now().Add(time.Second)
	for {
		r.mu.Lock()
		closing := r.closing
		r.mu.Unlock()
		if closing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("close did not start")
		}
		time.Sleep(time.Millisecond)
	}
	if e = r.Ready(ctx); e == nil {
		t.Fatal("closing registry ready")
	}
	if _, e = r.View(ctx, "alpha"); e == nil {
		t.Fatal("closing accepted lease")
	}
	if _, _, e = r.ExportView(ctx, "alpha"); e == nil {
		t.Fatal("closing accepted export lease")
	}
	select {
	case e := <-closed:
		t.Fatal("closed before lease drained", e)
	default:
	}
	close(release)
	if e = <-updateDone; e != nil {
		t.Fatal(e)
	}
	if e = <-closed; e != nil {
		t.Fatal(e)
	}
	if e = r.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := New(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	a, e = reopened.View(ctx, "alpha")
	if e != nil || a.Revision != 2 || len(a.Elements) != 1 {
		t.Fatal(a, e)
	}
	for _, name := range []string{"alpha.db", "beta.db", ".owner.lock"} {
		st, e := os.Stat(filepath.Join(dir, name))
		if e != nil || st.Mode().Perm() != 0600 {
			t.Fatal(name, st, e)
		}
	}
}
func TestPrivateDirectorySchemaAndLockRefusal(t *testing.T) {
	dir := t.TempDir()
	r, e := New(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	started := time.Now()
	if other, e := New(dir); e == nil {
		other.Close()
		t.Fatal("second process owner allowed")
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("unbounded directory lock")
	}
	if e = r.Provision(context.Background(), []contract.Project{{ID: "../escape"}}); e == nil {
		t.Fatal("path project allowed")
	}
	bad := t.TempDir()
	if e = os.Chmod(bad, 0755); e != nil {
		t.Fatal(e)
	}
	if other, e := New(bad); e == nil {
		other.Close()
		t.Fatal("public directory allowed")
	}
	missingSchema := t.TempDir()
	dbpath := filepath.Join(missingSchema, "alpha.db")
	if e = os.WriteFile(dbpath, []byte("invalid database"), 0600); e != nil {
		t.Fatal(e)
	}
	if other, e := New(missingSchema); e == nil {
		other.Close()
		t.Fatal("invalid storage ready")
	}
	// A v1 database with the wrong identity is refused by startup discovery.
	wrong := t.TempDir()
	d, e := store.Open(filepath.Join(wrong, "beta.db"), contract.Project{ID: "alpha"}, true)
	if e != nil {
		t.Fatal(e)
	}
	d.Close()
	if other, e := New(wrong); e == nil {
		other.Close()
		t.Fatal("wrong project identity adopted")
	}
}

func TestRestoreRefusesRunningDirectory(t *testing.T) {
	dir := t.TempDir()
	r, e := New(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	if e = r.Provision(context.Background(), []contract.Project{{ID: "alpha"}}); e != nil {
		t.Fatal(e)
	}
	var backup bytes.Buffer
	if e = r.Backup(context.Background(), "alpha", &backup); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(dir, "restored.db")
	if e = store.Restore(context.Background(), path, contract.Project{ID: "alpha"}, bytes.NewReader(backup.Bytes())); e == nil {
		t.Fatal("restored into live registry directory")
	}
	if _, e = os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("restore created file before ownership check", e)
	}
}

func TestRegistryExportViewProjectIsolationAndDetachment(t *testing.T) {
	r, e := New(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	ctx := context.Background()
	if e = r.Provision(ctx, []contract.Project{{ID: "alpha"}, {ID: "beta"}}); e != nil {
		t.Fatal(e)
	}
	_, e = r.Update(ctx, "alpha", 0, contract.AuditContext{AccountID: "test"}, func(contract.Snapshot) (contract.ChangeSet, error) {
		return contract.ChangeSet{Upserts: []contract.Element{{ID: "TASK-one", Notes: "alpha"}}}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	s, events, e := r.ExportView(ctx, "alpha")
	if e != nil || s.Revision != 1 || len(events) != 1 || events[0].ChangedIDs[0] != "TASK-one" {
		t.Fatal(s, events, e)
	}
	s.Elements[0].Notes = "mutated"
	events[0].ChangedIDs[0] = "mutated"
	s, events, e = r.ExportView(ctx, "alpha")
	if e != nil || s.Elements[0].Notes != "alpha" || events[0].ChangedIDs[0] != "TASK-one" {
		t.Fatal("export aliased", e)
	}
	s, events, e = r.ExportView(ctx, "beta")
	if e != nil || s.Revision != 0 || len(s.Elements) != 0 || len(events) != 0 {
		t.Fatal("cross-project journal", s, events, e)
	}
	if _, _, e = r.ExportView(ctx, "foreign"); e == nil {
		t.Fatal("unknown project accepted")
	}
}
