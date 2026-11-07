//go:build linux

package fs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	dp "github.com/relux-works/swarma-board/dataprovider"
	cf "github.com/relux-works/swarma-board/dataprovider/conformance"
)

func projectionSeed() []dp.Element {
	nano := time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.UTC)
	zero := time.Time{}
	return []dp.Element{
		{ID: "TASK-1", Type: "task", Name: "!!!", ParentID: "STORY-999", Title: "  T#\nnext\t", Description: "## opaque\r\n```\nend", Scope: "scope", AcceptanceCriteria: "AC", Status: "backlog", CreatedAt: &nano, UpdatedAt: &zero, BlockedBy: []string{"TASK-3"}, ManualBlockedBy: []string{"TASK-4"}, Checklist: []dp.ChecklistItem{{Text: "check\nline", Checked: true}}, Notes: "## note", Estimate: &dp.Estimate{Kind: "estimated", Scale: &dp.EstimateScale{Kind: "fibonacci", Number: 3}}, ResourceRefs: []dp.ResourceRef{{Name: "first]", URI: "urn:example:out(1)", Description: " desc ", Type: "outcome"}, {Name: "middle", URI: "urn:example:in", Type: "precondition"}, {Name: "last", URI: "urn:example:out2", Type: "outcome"}}},
		{ID: "TASK-2", Type: "task", Name: strings.Repeat("a", 300), ParentID: "TASK-1", Status: "backlog", BlockedBy: []string{}, ManualBlockedBy: nil, Checklist: []dp.ChecklistItem{}, ResourceRefs: []dp.ResourceRef{}},
	}
}
func seedProjection(t *testing.T) (context.Context, *cf.Instance, dp.Board, dp.Actor, *controlled) {
	t.Helper()
	ctx, _, i, b, a := fixture(t)
	_, err := i.Provider.Update(ctx, b.Scope, dp.Revision{Scope: b.Scope}, a, func(dp.Snapshot) (dp.ChangeSet, error) { return dp.ChangeSet{Upserts: projectionSeed()}, nil })
	if err != nil {
		t.Fatal(err)
	}
	return ctx, i, b, a, i.Provider.(*controlled)
}
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func replaceProjection(t *testing.T, name string, b []byte) {
	t.Helper()
	if err := replaceFixture(name, b); err != nil {
		t.Fatal(err)
	}
}
func generationFixture(t *testing.T, c *controlled, s dp.Scope) string {
	t.Helper()
	dir, err := fixtureGeneration(c.root, s)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
func expectExactSnapshot(t *testing.T, p dp.Provider, ctx context.Context, want dp.Snapshot) {
	t.Helper()
	got, err := p.View(ctx, want.Board.Scope)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("canonical facts changed: got=%+v want=%+v err=%v", got, want, err)
	}
}

func TestProjectionCanonicalFactsRoundTrip(t *testing.T) {
	ctx, i, b, a, c := seedProjection(t)
	before, err := i.Provider.View(ctx, b.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Elements, projectionSeed()) {
		t.Fatal("names, nanoseconds, nulls, policies, blockers or resource order changed")
	}
	dir := generationFixture(t, c, b.Scope)
	readme := readFixture(t, filepath.Join(dir, "elements/00000/README.md"))
	const requiredHeader = "Generated view; snapshot.json is authoritative. Native display defaults are not stored facts.\n\n"
	if !bytes.HasPrefix(readme, []byte(requiredHeader)) {
		t.Fatal("required literal generated-view header missing")
	}
	wantReadme := projectionHeader + "# \\s\\sT\\#\\nnext\\t\n\n## Description\n\\## opaque\r\n\\```\nend\n\n## Scope\nscope\n\n## Acceptance Criteria\nAC\n"
	if string(readme) != wantReadme {
		t.Fatalf("native canonical README bytes: %q", readme)
	}
	progress := readFixture(t, filepath.Join(dir, "elements/00000/progress.md"))
	wantProgress := projectionHeader + "## Status\nbacklog\n\n## Review\nrequired\n\n## Task Class\ncode\n\n## Estimate\nestimated(fibonacci(3))\n\n## Assigned To\n(none)\n\n## Created\n2026-01-01T00:00:00Z\n\n## Last Update\n\n\n## Blocked By\n- TASK-3\n\n## Blocks\n- (none)\n\n## Checklist\n- [x] check\\nline\n\n## Notes\n\\## note\n\n## Precondition Resources\n- [middle](urn:example:in)\n\n## Outcome Resources\n- [first\\]](urn:example:out\\(1\\)) — \\sdesc\\s\n- [last](urn:example:out2)\n"
	if string(progress) != wantProgress {
		t.Fatalf("native canonical progress bytes: %q", progress)
	}
	// Different stored distinctions intentionally have identical display bytes.
	alias := dp.CloneElement(before.Elements[0])
	alias.Review = "required"
	alias.TaskClass = "code"
	alias.CreatedAt = func() *time.Time { x := alias.CreatedAt.Truncate(time.Second); return &x }()
	alias.UpdatedAt = nil
	alias.ManualBlockedBy = nil
	views := renderViews(alias)
	if !bytes.Equal(views[0], readme) || !bytes.Equal(views[1], progress) {
		t.Fatal("display-only equivalences differ")
	}
	cp, err := i.Provider.Checkpoint(ctx, b.Scope)
	if err != nil {
		t.Fatal(err)
	}
	defer cp.Close()
	events := cp.Events()
	var archive bytes.Buffer
	exportCP, err := i.Provider.Checkpoint(ctx, b.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dp.EncodeCheckpoint(ctx, &archive, exportCP); err != nil {
		t.Fatal(err)
	}
	var decoded dp.Snapshot
	if err := json.Unmarshal(readFixture(t, filepath.Join(dir, "snapshot.json")), &decoded); err != nil {
		t.Fatal(err)
	}
	decodedEvents := []dp.Event{}
	for _, line := range bytes.Split(bytes.TrimSuffix(readFixture(t, filepath.Join(dir, "events.ndjson")), []byte("\n")), []byte("\n")) {
		var event dp.Event
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		decodedEvents = append(decodedEvents, event)
	}
	if !reflect.DeepEqual(decoded, before) || !reflect.DeepEqual(decodedEvents, events) {
		t.Fatal("canonical disk round-trip lost facts")
	}
	roundTrip, err := dp.OpenCheckpoint(ctx, decoded, decodedEvents, dp.ResourceBacking{Release: func() error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	var roundTripBytes bytes.Buffer
	if _, err := dp.EncodeCheckpoint(ctx, &roundTripBytes, roundTrip); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(archive.Bytes(), roundTripBytes.Bytes()) {
		t.Fatal("checkpoint byte round-trip lost facts")
	}
	// Reranking changes physical locations but never uses slot as identity.
	_, err = i.Provider.Update(ctx, b.Scope, before.Revision, a, func(dp.Snapshot) (dp.ChangeSet, error) {
		return dp.ChangeSet{Upserts: []dp.Element{{ID: "TASK-0", Type: "task", Name: "new", Status: "backlog"}}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	dir = generationFixture(t, c, b.Scope)
	if !bytes.Equal(readFixture(t, filepath.Join(dir, "elements/00001/README.md")), readme) || !bytes.Equal(readFixture(t, filepath.Join(dir, "elements/00001/progress.md")), progress) {
		t.Fatal("reranking mixed generations")
	}
	if !reflect.DeepEqual(cp.Snapshot(), before) || !reflect.DeepEqual(cp.Events(), events) {
		t.Fatal("checkpoint followed CURRENT after reranking")
	}
	if err := cp.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := i.Provider.View(ctx, b.Scope)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after.Elements[1:], before.Elements) {
		t.Fatal("reranking changed records")
	}
	for n := 0; n < 2; n++ {
		p, _, err := i.Restart(ctx)
		if err != nil {
			t.Fatal(err)
		}
		expectExactSnapshot(t, p, ctx, after)
	}
}

// Rebind only integrity metadata. Even coherent checksums do not authorize a
// Markdown edit: bytes must be the deterministic projection of canonical facts.
func rebindProjection(t *testing.T, root, dir string, s dp.Scope, manifest []byte) {
	t.Helper()
	replaceProjection(t, filepath.Join(dir, "projection.json"), manifest)
	var env envelope
	if err := json.Unmarshal(readFixture(t, filepath.Join(dir, "envelope.json")), &env); err != nil {
		t.Fatal(err)
	}
	env.ProjectionSHA256 = hash(manifest)
	eb, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	replaceProjection(t, filepath.Join(dir, "envelope.json"), eb)
	pb, err := json.Marshal(pointer{Generation: filepath.Base(dir), SHA256: hash(eb)})
	if err != nil {
		t.Fatal(err)
	}
	replaceProjection(t, filepath.Join(root, scopePath(s), "CURRENT"), pb)
}
func TestProjectionRefusesEditsWithoutRepair(t *testing.T) {
	cases := []string{"edited-readme", "edited-progress", "missing-header", "missing-readme", "missing-progress", "extra-file", "extra-slot", "missing-slot", "slot-symlink", "file-symlink", "hardlink", "writable-view", "missing-manifest", "edited-manifest", "manifest-scope", "manifest-revision", "manifest-snapshot", "manifest-layout", "manifest-order", "coherently-rehashed-edit", "missing-envelope-digest", "old-schema", "mixed-slots"}
	for _, kind := range cases {
		t.Run(kind, func(t *testing.T) {
			ctx, i, b, _, c := seedProjection(t)
			if err := i.Provider.Close(); err != nil {
				t.Fatal(err)
			}
			dir := generationFixture(t, c, b.Scope)
			readme := filepath.Join(dir, "elements/00000/README.md")
			progress := filepath.Join(dir, "elements/00000/progress.md")
			manifestPath := filepath.Join(dir, "projection.json")
			require := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "edited-readme":
				replaceProjection(t, readme, append(readFixture(t, readme), []byte("edited\n")...))
			case "edited-progress":
				replaceProjection(t, progress, []byte(projectionHeader+"## Notes\nedited\n"))
			case "missing-header":
				replaceProjection(t, readme, bytes.TrimPrefix(readFixture(t, readme), []byte(projectionHeader)))
			case "missing-readme":
				require(os.Remove(readme))
			case "missing-progress":
				require(os.Remove(progress))
			case "extra-file":
				require(os.WriteFile(filepath.Join(dir, "elements/00000/extra.md"), []byte("unknown"), 0400))
			case "extra-slot":
				require(os.Mkdir(filepath.Join(dir, "elements/10000"), 0700))
			case "missing-slot":
				require(os.RemoveAll(filepath.Join(dir, "elements/00001")))
			case "slot-symlink":
				require(os.Rename(filepath.Join(dir, "elements/00001"), filepath.Join(dir, "saved-slot")))
				require(os.Symlink("../saved-slot", filepath.Join(dir, "elements/00001")))
			case "file-symlink":
				require(os.Remove(readme))
				require(os.Symlink("../../snapshot.json", readme))
			case "hardlink":
				require(os.Remove(readme))
				require(os.Link(progress, readme))
			case "writable-view":
				require(os.Chmod(readme, 0600))
			case "missing-manifest":
				require(os.Remove(manifestPath))
			case "edited-manifest":
				replaceProjection(t, manifestPath, []byte("{}"))
			case "old-schema":
				replaceProjection(t, filepath.Join(c.root, "schema.json"), []byte(`{"format":"scoped-filesystem","version":1}`))
			case "mixed-slots":
				x, y := readFixture(t, readme), readFixture(t, filepath.Join(dir, "elements/00001/README.md"))
				replaceProjection(t, readme, y)
				replaceProjection(t, filepath.Join(dir, "elements/00001/README.md"), x)
			case "missing-envelope-digest":
				var env envelope
				require(json.Unmarshal(readFixture(t, filepath.Join(dir, "envelope.json")), &env))
				env.ProjectionSHA256 = ""
				eb, err := json.Marshal(env)
				require(err)
				replaceProjection(t, filepath.Join(dir, "envelope.json"), eb)
				pb, err := json.Marshal(pointer{Generation: filepath.Base(dir), SHA256: hash(eb)})
				require(err)
				replaceProjection(t, filepath.Join(c.root, scopePath(b.Scope), "CURRENT"), pb)
			default:
				var m projectionManifest
				require(json.Unmarshal(readFixture(t, manifestPath), &m))
				switch kind {
				case "manifest-scope":
					m.Scope.ProductID = "foreign"
				case "manifest-revision":
					m.Revision.Counter++
				case "manifest-snapshot":
					m.SnapshotSHA256 = strings.Repeat("0", 64)
				case "manifest-layout":
					m.Layout = "unknown/1"
				case "manifest-order":
					slices.Reverse(m.Files)
				case "coherently-rehashed-edit":
					edited := []byte(projectionHeader + "# unauthorized\n")
					replaceProjection(t, readme, edited)
					m.Files[0] = projectionEntry(m.Files[0].Path, edited)
				}
				mb, err := json.Marshal(m)
				require(err)
				rebindProjection(t, c.root, dir, b.Scope, mb)
			}
			before, err := fingerprintTree(c.root)
			require(err)
			for n := 0; n < 2; n++ {
				p, err := Open(ctx, c.root)
				if p != nil {
					require(p.Close())
				}
				if code, _ := dp.CodeOf(err); code != dp.StorageError && !(kind == "old-schema" && code == dp.UnsupportedSchema) {
					t.Fatalf("unsafe projection admitted: %v", err)
				}
				after, err := fingerprintTree(c.root)
				require(err)
				if before != after {
					t.Fatal("refusal rewrote evidence")
				}
			}
		})
	}
}

func TestProjectionRecord10001Refused(t *testing.T) {
	// This small-record slot boundary is not the maximum-state C50 workload.
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	p, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := p.Close(); err != nil {
			t.Error(err)
		}
	}()
	b := dp.Board{Scope: dp.Scope{ProductID: "alpha", BoardID: "slots"}, Name: "Slots"}
	if err := p.EnsureEmpty(ctx, b); err != nil {
		t.Fatal(err)
	}
	a := dp.Actor{AccountID: "example", RequestID: "slot-boundary", Operation: "create", At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	elements := make([]dp.Element, dp.MaxElements)
	for n := range elements {
		elements[n] = dp.Element{ID: fmt.Sprintf("TASK-%05d", n), Type: "task", Name: "x", Status: "backlog"}
	}
	started := time.Now()
	commit, err := p.Update(ctx, b.Scope, dp.Revision{Scope: b.Scope}, a, func(dp.Snapshot) (dp.ChangeSet, error) { return dp.ChangeSet{Upserts: elements}, nil })
	t.Logf("10000 small records durable projection elapsed=%s limit=%s", time.Since(started), dp.UpdateTimeout)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := fixtureGeneration(root, b.Scope)
	if err != nil {
		t.Fatal(err)
	}
	for _, slot := range []string{"00000", "09999"} {
		for _, file := range []string{"README.md", "progress.md"} {
			if !bytes.HasPrefix(readFixture(t, filepath.Join(dir, "elements", slot, file)), []byte(projectionHeader)) {
				t.Fatal("boundary header absent")
			}
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "elements/10000")); !os.IsNotExist(err) {
		t.Fatal("out-of-range slot exists", err)
	}
	before, err := p.View(ctx, b.Scope)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := fingerprintTree(root)
	if err != nil {
		t.Fatal(err)
	}
	refused, err := p.Update(ctx, b.Scope, commit.Revision, a, func(dp.Snapshot) (dp.ChangeSet, error) {
		return dp.ChangeSet{Upserts: []dp.Element{{ID: "TASK-10000", Type: "task", Name: "10001st", Status: "backlog"}}}, nil
	})
	if code, _ := dp.CodeOf(err); code != dp.InvalidSnapshot || !reflect.DeepEqual(refused, dp.Commit{}) {
		t.Fatal("record 10001 did not explicitly refuse", err, refused)
	}
	after, err := fingerprintTree(root)
	if err != nil || after != tree {
		t.Fatal("one-over mutated generation", err)
	}
	expectExactSnapshot(t, p, ctx, before)
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	p, err = Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	expectExactSnapshot(t, p, ctx, before)
	var count int
	var size int64
	if err := filepath.WalkDir(dir, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			count++
			size += info.Size()
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Logf("small-record boundary files=%d bytes=%d; no payload maximum or C50 qualification", count, size)
}
