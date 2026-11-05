package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/relux-works/swarma-board/internal/contract"
	bolt "go.etcd.io/bbolt"
)

var ctx = context.Background()
var project = contract.Project{ID: "alpha", Name: "Synthetic alpha"}
var actor = contract.AuditContext{AccountID: "synthetic-editor", RequestID: "synthetic-request", Operation: "set_notes", At: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)}

// Restore requires an explicitly private offline directory. testing.TempDir
// creates its numbered child with 0777 subject to the caller's umask.
func privateRestoreDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if e := os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	assertRestoreDirectoryMode(t, dir, 0700)
	return dir
}

func assertRestoreDirectoryMode(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	st, e := os.Lstat(dir)
	if e != nil {
		t.Fatal(e)
	}
	if st.Mode() != os.ModeDir|mode {
		t.Fatalf("restore fixture mode: got %v, want %v", st.Mode(), os.ModeDir|mode)
	}
}

func openTest(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "alpha.db")
	d, e := Open(path, project, true)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { d.Close() })
	return d, path
}
func code(err error) string {
	var e *contract.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}
func seed(t *testing.T, d *DB) {
	t.Helper()
	_, e := d.Update(ctx, "alpha", 0, actor, func(contract.Snapshot) (contract.ChangeSet, error) {
		return contract.ChangeSet{Upserts: []contract.Element{{ID: "TASK-a", Notes: "original", BlockedBy: []string{"TASK-b"}}, {ID: "TASK-b"}}, Provenance: []contract.ProvenanceFile{{Path: "provenance/synthetic.json", Data: []byte("inert")}}}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
}
func TestCASConcurrentAndRestart(t *testing.T) {
	d, path := openTest(t)
	seed(t, d)
	var calls atomic.Int32
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := d.Update(ctx, "alpha", 1, actor, func(s contract.Snapshot) (contract.ChangeSet, error) { calls.Add(1); return contract.ChangeSet{}, nil })
			results <- e
		}()
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if code(e) == "revision_conflict" {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 || calls.Load() != 1 {
		t.Fatalf("success=%d conflict=%d callbacks=%d", success, conflict, calls.Load())
	}
	before, e := d.View(ctx, "alpha")
	if e != nil {
		t.Fatal(e)
	}
	if before.Revision != 2 {
		t.Fatal(before.Revision)
	}
	a, e := d.Audit(ctx)
	if e != nil || len(a) != 2 || a[1].Revision != 2 {
		t.Fatalf("audit %v %v", a, e)
	}
	if e = d.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := Open(path, project, false)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	after, e := reopened.View(ctx, "alpha")
	if e != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("restart mismatch %v", e)
	}
	if _, e = reopened.View(ctx, "foreign"); code(e) != "not_found" {
		t.Fatal(e)
	}
}
func TestRollbackAndDetachedSnapshots(t *testing.T) {
	cases := map[string]func(contract.Snapshot) (contract.ChangeSet, error){
		"callback": func(s contract.Snapshot) (contract.ChangeSet, error) {
			s.Elements[0].BlockedBy[0] = "mutated"
			return contract.ChangeSet{}, errors.New("refuse")
		},
		"duplicate": func(s contract.Snapshot) (contract.ChangeSet, error) {
			return contract.ChangeSet{Upserts: []contract.Element{{ID: "TASK-c"}, {ID: "task-C"}}}, nil
		},
		"delete-upsert": func(s contract.Snapshot) (contract.ChangeSet, error) {
			return contract.ChangeSet{Upserts: []contract.Element{{ID: "TASK-a"}}, Deletes: []string{"TASK-a"}}, nil
		},
		"case-collision": func(s contract.Snapshot) (contract.ChangeSet, error) {
			return contract.ChangeSet{Upserts: []contract.Element{{ID: "task-A"}}}, nil
		},
		"provenance-after-fact-write": func(s contract.Snapshot) (contract.ChangeSet, error) {
			return contract.ChangeSet{Upserts: []contract.Element{{ID: "TASK-c"}}, Provenance: []contract.ProvenanceFile{{Path: "same"}, {Path: "same"}}}, nil
		},
		"actual-bbolt-key-failure": func(s contract.Snapshot) (contract.ChangeSet, error) {
			return contract.ChangeSet{Upserts: []contract.Element{{ID: "TASK-c"}, {ID: strings.Repeat("x", 40000)}}}, nil
		},
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			d, _ := openTest(t)
			seed(t, d)
			before, _ := d.View(ctx, "alpha")
			auditsBefore, _ := d.Audit(ctx)
			if _, e := d.Update(ctx, "alpha", 1, actor, fn); e == nil {
				t.Fatal("accepted")
			}
			after, _ := d.View(ctx, "alpha")
			auditsAfter, _ := d.Audit(ctx)
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(auditsBefore, auditsAfter) {
				t.Fatal("partial persistence")
			}
		})
	}
	d, _ := openTest(t)
	seed(t, d)
	s, _ := d.View(ctx, "alpha")
	s.Elements[0].BlockedBy[0] = "foreign"
	s.Provenance[0].Data[0] = 'X'
	fresh, _ := d.View(ctx, "alpha")
	if fresh.Elements[0].BlockedBy[0] != "TASK-b" || string(fresh.Provenance[0].Data) != "inert" {
		t.Fatal("view aliases storage")
	}
}
func TestCancelOverflowAndCallbackOnce(t *testing.T) {
	d, _ := openTest(t)
	seed(t, d)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	called := false
	_, e := d.Update(cancelled, "alpha", 1, actor, func(contract.Snapshot) (contract.ChangeSet, error) { called = true; return contract.ChangeSet{}, nil })
	if !errors.Is(e, context.Canceled) || called {
		t.Fatal(e, called)
	}
	mid, cancelMid := context.WithCancel(ctx)
	_, e = d.Update(mid, "alpha", 1, actor, func(contract.Snapshot) (contract.ChangeSet, error) {
		cancelMid()
		return contract.ChangeSet{Upserts: []contract.Element{{ID: "TASK-c"}}}, nil
	})
	if !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	s, _ := d.View(ctx, "alpha")
	if s.Revision != 1 || len(s.Elements) != 2 {
		t.Fatal(s)
	}
	if e = d.db.Bolt.Update(func(tx *bolt.Tx) error { return tx.Bucket(meta).Put([]byte("revision"), u64(math.MaxUint64)) }); e != nil {
		t.Fatal(e)
	}
	_, e = d.Update(ctx, "alpha", contract.Revision(math.MaxUint64), actor, func(contract.Snapshot) (contract.ChangeSet, error) {
		t.Fatal("overflow invoked callback")
		return contract.ChangeSet{}, nil
	})
	if code(e) != "unavailable" {
		t.Fatal(e)
	}
}
func TestBackupRestoreSchemaAndPrivacy(t *testing.T) {
	d, path := openTest(t)
	seed(t, d)
	before, _ := d.View(ctx, "alpha")
	auditBefore, _ := d.Audit(ctx)
	var b bytes.Buffer
	if e := d.Backup(ctx, &b); e != nil {
		t.Fatal(e)
	}
	// A valid backup must still refuse a public destination directory before
	// creating either the destination or its ownership lock.
	public := privateRestoreDir(t)
	if e := os.Chmod(public, 0755); e != nil {
		t.Fatal(e)
	}
	assertRestoreDirectoryMode(t, public, 0755)
	if e := Restore(ctx, filepath.Join(public, "alpha.db"), project, bytes.NewReader(b.Bytes())); code(e) != "unavailable" {
		t.Fatal("public restore directory accepted", e)
	}
	if entries, e := os.ReadDir(public); e != nil || len(entries) != 0 {
		t.Fatal("public-directory refusal changed fixture", entries, e)
	}
	dst := filepath.Join(privateRestoreDir(t), "alpha.db")
	if e := Restore(ctx, dst, project, bytes.NewReader(b.Bytes())); e != nil {
		t.Fatal(e)
	}
	restored, e := Open(dst, project, false)
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	after, _ := restored.View(ctx, "alpha")
	auditAfter, _ := restored.Audit(ctx)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(auditBefore, auditAfter) {
		t.Fatal("backup lost state")
	}
	if e = Restore(ctx, dst, project, bytes.NewReader(b.Bytes())); e == nil {
		t.Fatal("restore overwrote destination")
	}
	info, e := os.Stat(path)
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, e)
	}
	if e = d.db.Bolt.Update(func(tx *bolt.Tx) error { return tx.Bucket(meta).Put([]byte("schema"), u64(2)) }); e != nil {
		t.Fatal(e)
	}
	if code(d.Ready(ctx)) != "unavailable" {
		t.Fatal("new schema ready")
	}
	d.Close()
	data, _ := os.ReadFile(path)
	sum := sha256.Sum256(data)
	if _, e = Open(path, project, false); code(e) != "unavailable" {
		t.Fatal(e)
	}
	data, _ = os.ReadFile(path)
	if sha256.Sum256(data) != sum {
		t.Fatal("refusal mutated unknown schema")
	}
}

// Kill the real Repository.Update after facts/provenance/revision/audit writes,
// immediately before bbolt commits. Hook is unexported and only set by this test.
func TestCrashAtomicity(t *testing.T) {
	if os.Getenv("SWARMA_SYNTHETIC_CRASH_CHILD") == "1" {
		path := os.Getenv("SWARMA_SYNTHETIC_CRASH_DB")
		d, e := Open(path, project, false)
		if e != nil {
			os.Exit(91)
		}
		d.beforeCommit = func() error {
			_ = os.WriteFile(path+".witness", []byte("transaction written; not committed"), 0600)
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
			os.Exit(92)
			return nil
		}
		_, _ = d.Update(ctx, "alpha", 1, actor, func(contract.Snapshot) (contract.ChangeSet, error) {
			return contract.ChangeSet{Upserts: []contract.Element{{ID: "TASK-c", BlockedBy: []string{"TASK-a"}}}, Provenance: []contract.ProvenanceFile{{Path: "provenance/changed", Data: []byte("partial")}}}, nil
		})
		os.Exit(93)
	}
	d, path := openTest(t)
	seed(t, d)
	before, _ := d.View(ctx, "alpha")
	auditsBefore, _ := d.Audit(ctx)
	d.Close()
	command := exec.Command(os.Args[0], "-test.run=^TestCrashAtomicity$")
	command.Env = append(os.Environ(), "SWARMA_SYNTHETIC_CRASH_CHILD=1", "SWARMA_SYNTHETIC_CRASH_DB="+path)
	if e := command.Run(); e == nil {
		t.Fatal("child did not die")
	}
	if _, e := os.Stat(path + ".witness"); e != nil {
		t.Fatal("child never reached precommit", e)
	}
	reopened, e := Open(path, project, false)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	after, _ := reopened.View(ctx, "alpha")
	auditsAfter, _ := reopened.Audit(ctx)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(auditsBefore, auditsAfter) {
		t.Fatal("crash exposed partial update")
	}
	exported, history, e := reopened.ExportView(ctx, "alpha")
	if e != nil || !reflect.DeepEqual(before, exported) || !reflect.DeepEqual(auditsBefore, history) {
		t.Fatal("post-kill coherent export mismatch", e)
	}
}

func TestUnversionedDatabaseRefusalDoesNotWriteStormMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, e := bolt.Open(path, 0600, nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = raw.Update(func(tx *bolt.Tx) error { _, e := tx.CreateBucket([]byte("legacy")); return e }); e != nil {
		t.Fatal(e)
	}
	raw.Close()
	data, _ := os.ReadFile(path)
	before := sha256.Sum256(data)
	if _, e = Open(path, project, true); code(e) != "unavailable" {
		t.Fatal(e)
	}
	data, _ = os.ReadFile(path)
	if sha256.Sum256(data) != before {
		t.Fatal("unversioned refusal changed database")
	}
}
func TestElementCapAndDuplicateDeletesRollBack(t *testing.T) {
	d, _ := openTest(t)
	elements := make([]contract.Element, 10001)
	for i := range elements {
		elements[i].ID = fmt.Sprintf("TASK-%05d", i)
	}
	if _, e := d.Update(ctx, "alpha", 0, actor, func(contract.Snapshot) (contract.ChangeSet, error) { return contract.ChangeSet{Upserts: elements}, nil }); code(e) != "invalid_snapshot" {
		t.Fatal(e)
	}
	s, _ := d.View(ctx, "alpha")
	if s.Revision != 0 || len(s.Elements) != 0 {
		t.Fatal("element cap persisted data")
	}
	seed(t, d)
	if _, e := d.Update(ctx, "alpha", 1, actor, func(contract.Snapshot) (contract.ChangeSet, error) {
		return contract.ChangeSet{Deletes: []string{"TASK-a", "task-A"}}, nil
	}); code(e) != "invalid_request" {
		t.Fatal(e)
	}
}
func TestViewsAndBackupAreCoherentDuringWrites(t *testing.T) {
	d, _ := openTest(t)
	done := make(chan error, 1)
	start := make(chan struct{})
	go func() {
		<-start
		for i := 0; i < 40; i++ {
			_, e := d.Update(ctx, "alpha", contract.Revision(i), actor, func(s contract.Snapshot) (contract.ChangeSet, error) {
				text := fmt.Sprint(s.Revision + 1)
				return contract.ChangeSet{Upserts: []contract.Element{{ID: "TASK-a", Notes: text}, {ID: "TASK-b", Notes: text}}, Provenance: []contract.ProvenanceFile{{Path: "provenance/revision", Data: []byte(text)}}}, nil
			})
			if e != nil {
				done <- e
				return
			}
		}
		done <- nil
	}()
	close(start)
	check := func(s contract.Snapshot) {
		t.Helper()
		if s.Revision == 0 {
			if len(s.Elements) != 0 {
				t.Fatal(s)
			}
			return
		}
		text := fmt.Sprint(s.Revision)
		if len(s.Elements) != 2 || s.Elements[0].Notes != text || s.Elements[1].Notes != text || len(s.Provenance) != 1 || string(s.Provenance[0].Data) != text {
			t.Fatal("torn snapshot", s)
		}
	}
	for i := 0; i < 8; i++ {
		s, e := d.View(ctx, "alpha")
		if e != nil {
			t.Fatal(e)
		}
		check(s)
		var backup bytes.Buffer
		if e = d.Backup(ctx, &backup); e != nil {
			t.Fatal(e)
		}
		path := filepath.Join(privateRestoreDir(t), "alpha.db")
		if e = Restore(ctx, path, project, bytes.NewReader(backup.Bytes())); e != nil {
			t.Fatal(e)
		}
		restored, e := Open(path, project, false)
		if e != nil {
			t.Fatal(e)
		}
		s, e = restored.View(ctx, "alpha")
		if e != nil {
			t.Fatal(e)
		}
		check(s)
		audit, e := restored.Audit(ctx)
		if e != nil || len(audit) != int(s.Revision) {
			t.Fatal("backup audit mismatch", len(audit), s.Revision, e)
		}
		restored.Close()
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}
