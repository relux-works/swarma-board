//go:build linux && amd64

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/relux-works/swarma-board/internal/contract"
	"github.com/relux-works/swarma-board/internal/maintenance"
	"github.com/relux-works/swarma-board/internal/store"
	"github.com/relux-works/swarma-board/internal/transfer"
	bolt "go.etcd.io/bbolt"
)

// This launches the actual server executable over synthetic temp storage. Native
// reimport goes into a fresh synthetic target; it never resumes an old writer.
func TestMaintenanceExecutableUnsignedReadOnlyReimportAndRefusal(t *testing.T) {
	ctx := context.Background()
	commit := os.Getenv("BOARD_CANDIDATE_COMMIT")
	if commit == "" {
		commit = build // Explicit synthetic build identity for ordinary test runs.
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "swarma-board-server")
	cmd := exec.Command("go", "build", "-mod=readonly", "-p", "2", "-ldflags", "-X main.commit="+commit, "-o", binary, "./cmd/swarma-board-server")
	cmd.Dir = ".."
	cmd.Env = append(os.Environ(), "GOMAXPROCS=2")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, out)
	}
	type observation struct {
		Name    string   `json:"name"`
		Args    []string `json:"args"`
		Exit    int      `json:"exit"`
		Message string   `json:"message"`
	}
	var observations []observation
	runCLI := func(name string, want int, args ...string) string {
		t.Helper()
		deadline, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		p := exec.CommandContext(deadline, binary, args...)
		out, err := p.CombinedOutput()
		code := 0
		if err != nil {
			if e, ok := err.(*exec.ExitError); ok {
				code = e.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
		observations = append(observations, observation{name, args, code, string(out)})
		if code != want {
			t.Fatalf("%s exit=%d want=%d: %s", name, code, want, out)
		}
		t.Logf("%s: exit %d: %s", name, code, strings.TrimSpace(string(out)))
		return string(out)
	}
	read := func(path string) []byte {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	source := filepath.Join(dir, "source.db")
	p := contract.Project{ID: project}
	db, err := store.Open(source, p, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	prepared, err := transfer.New(commit).Prepare(ctx, bytes.NewReader(fixture(t, "native-v1.tar.gz")), contract.ImportOptions{ProjectID: project})
	if err != nil {
		t.Fatal(err)
	}
	actor := contract.AuditContext{AccountID: "synthetic-admin", RequestID: "seed", Operation: "synthetic-import", At: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)}
	if _, err = db.Update(ctx, project, 0, actor, func(contract.Snapshot) (contract.ChangeSet, error) {
		return contract.ChangeSet{Upserts: prepared.Snapshot.Elements, Provenance: prepared.Snapshot.Provenance}, nil
	}); err != nil {
		t.Fatal(err)
	}
	actor.RequestID, actor.Operation = "post-checkpoint", "synthetic-task-mutation"
	if _, err = db.Update(ctx, project, 1, actor, func(s contract.Snapshot) (contract.ChangeSet, error) {
		for _, e := range s.Elements {
			if e.Type == "task" {
				e.Notes += "\nsynthetic post-checkpoint preserved"
				return contract.ChangeSet{Upserts: []contract.Element{e}}, nil
			}
		}
		t.Fatal("fixture missing task")
		return contract.ChangeSet{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	expected, err := db.View(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	args := func(output string) []string {
		return []string{"maintenance-export", "--database", source, "--project", project, "--output", output, "--writer-stopped"}
	}
	busy := filepath.Join(dir, "busy")
	beforeBusy := read(source)
	runCLI("writer-busy", 1, args(busy)...)
	if !bytes.Equal(beforeBusy, read(source)) {
		t.Fatal("busy refusal changed source")
	}
	if _, err = os.Lstat(busy); !os.IsNotExist(err) {
		t.Fatal("busy refusal published output", err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	before := read(source)
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "checkpoint")
	runCLI("missing-stopped-assertion", 2, args(dest)[:len(args(dest))-1]...)
	runCLI("help", 0, "maintenance-export", "--help")
	mixed := append(args(dest), "--synthetic-fixture", "must-not-be-opened")
	runCLI("mixed-service-flags", 2, mixed...)
	message := runCLI("unsigned-export", 0, args(dest)...)
	if !strings.Contains(message, "unsigned") || !strings.Contains(message, "writer remains stopped") {
		t.Fatal(message)
	}
	manifest := read(filepath.Join(dest, maintenance.ManifestName))
	snapshot := read(filepath.Join(dest, maintenance.SnapshotName))
	journal := read(filepath.Join(dest, maintenance.JournalName))
	integrity, err := maintenance.Verify(ctx, bytes.NewReader(manifest), bytes.NewReader(snapshot), bytes.NewReader(journal), maintenance.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	m := integrity.Manifest
	if m.Status != maintenance.Unsigned || m.DeclaredSignerKeyID != "" || m.ProjectID != project || m.ExporterCommit != commit || m.Revision != 2 || m.Journal.Count != 2 {
		t.Fatalf("bad manifest: %+v", m)
	}
	if !bytes.Contains(journal, []byte("post-checkpoint")) {
		t.Fatal("missing post-checkpoint journal mutation")
	}
	entries, err := os.ReadDir(dest)
	if err != nil || len(entries) != 3 {
		t.Fatal("artifact set", entries, err)
	}
	for _, path := range []string{dest, filepath.Join(dest, maintenance.ManifestName), filepath.Join(dest, maintenance.SnapshotName), filepath.Join(dest, maintenance.JournalName)} {
		st, e := os.Lstat(path)
		want := os.FileMode(0600)
		if path == dest {
			want = 0700
		}
		if e != nil || st.Mode().Perm() != want {
			t.Fatal("private mode", path, e)
		}
	}
	// Reimport validation uses frozen native-v1 Prepare, then one authorized
	// synthetic target transaction. It does not replay the v1 audit journal.
	reimport, err := transfer.New(commit).Prepare(ctx, bytes.NewReader(snapshot), contract.ImportOptions{ProjectID: project, ExpectedSHA256: m.Snapshot.SHA256})
	if err != nil || !reflect.DeepEqual(reimport.Snapshot.Elements, expected.Elements) {
		t.Fatal("native reimport lost elements", err)
	}
	target, err := store.Open(filepath.Join(dir, "fresh-target.db"), p, true)
	if err != nil {
		t.Fatal(err)
	}
	actor.RequestID, actor.Operation = "reimport", "synthetic-reimport"
	_, err = target.Update(ctx, project, 0, actor, func(contract.Snapshot) (contract.ChangeSet, error) {
		return contract.ChangeSet{Upserts: reimport.Snapshot.Elements, Provenance: reimport.Snapshot.Provenance}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := target.View(ctx, project)
	if err != nil || restored.Revision != 1 || !reflect.DeepEqual(restored.Elements, expected.Elements) {
		t.Fatal("fresh reimport mismatch", err)
	}
	if err = target.Close(); err != nil {
		t.Fatal(err)
	}
	tampered := append(bytes.Clone(snapshot), 'x')
	if _, err = maintenance.Verify(ctx, bytes.NewReader(manifest), bytes.NewReader(tampered), bytes.NewReader(journal), maintenance.Limits{}); err == nil {
		t.Fatal("tampered artifact accepted")
	}
	runCLI("existing-output", 1, args(dest)...)
	if !bytes.Equal(manifest, read(filepath.Join(dest, maintenance.ManifestName))) {
		t.Fatal("existing evidence overwritten")
	}
	// Even an empty existing directory and a symlink must never be replaced.
	empty := filepath.Join(dir, "empty-output")
	if err = os.Mkdir(empty, 0700); err != nil {
		t.Fatal(err)
	}
	runCLI("empty-existing-output", 1, args(empty)...)
	link := filepath.Join(dir, "output-link")
	if err = os.Symlink(dest, link); err != nil {
		t.Fatal(err)
	}
	runCLI("symlink-output", 1, args(link)...)
	parentLink := filepath.Join(dir, "parent-link")
	if err = os.Symlink(dir, parentLink); err != nil {
		t.Fatal(err)
	}
	runCLI("symlink-output-parent", 1, args(filepath.Join(parentLink, "refused"))...)
	missing := args(filepath.Join(dir, "missing-export"))
	missing[2] = filepath.Join(dir, "missing.db")
	runCLI("missing-source-no-create", 1, missing...)
	if _, err = os.Lstat(missing[2]); !os.IsNotExist(err) {
		t.Fatal("created missing source")
	}
	uninit := filepath.Join(dir, "uninitialized.db")
	raw, err := bolt.Open(uninit, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	uninitialized := read(uninit)
	missing[2] = uninit
	runCLI("uninitialized-source-no-init", 1, missing...)
	if !bytes.Equal(uninitialized, read(uninit)) {
		t.Fatal("initialized source")
	}
	linkedSource := filepath.Join(dir, "source-link.db")
	if err = os.Symlink(source, linkedSource); err != nil {
		t.Fatal(err)
	}
	missing[2] = linkedSource
	runCLI("symlink-source", 1, missing...)
	wrongProject := args(filepath.Join(dir, "wrong-project"))
	wrongProject[4] = "other-project"
	runCLI("wrong-project", 1, wrongProject...)
	if !bytes.Equal(before, read(source)) {
		t.Fatal("maintenance changed source bytes")
	}
	afterInfo, err := os.Stat(source)
	if err != nil || !info.ModTime().Equal(afterInfo.ModTime()) || !os.SameFile(info, afterInfo) {
		t.Fatal("maintenance changed source identity/mtime", err)
	}
	// The original schema-v1 writer can still open without a migration.
	compatible, err := store.Open(source, p, false)
	if err != nil {
		t.Fatal("v1 compatibility", err)
	}
	current, err := compatible.View(ctx, project)
	if err != nil || !reflect.DeepEqual(current, expected) {
		t.Fatal("v1 state changed", err)
	}
	if err = compatible.Close(); err != nil {
		t.Fatal(err)
	}
	// Evidence capture is opt-in and writes only into this workbench's new output.
	if evidence := os.Getenv("MAINTENANCE_CLI_EVIDENCE_DIR"); evidence != "" {
		if err = os.Mkdir(evidence, 0700); err != nil {
			t.Fatal(err)
		}
		process, e := json.MarshalIndent(observations, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		for name, data := range map[string][]byte{maintenance.ManifestName: manifest, maintenance.SnapshotName: snapshot, maintenance.JournalName: journal, "source.db": before, "process.json": append(process, '\n')} {
			if err = os.WriteFile(filepath.Join(evidence, name), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Log("synthetic only: exact source bytes/inode/mtime retained; hashes verified; fresh native-v1 reimport preserved task mutation; no approved signer or journal replay")
}
