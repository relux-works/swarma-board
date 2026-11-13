package integration

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/relux-works/swarma-board/internal/client"
	"github.com/relux-works/swarma-board/internal/contract"
	"github.com/relux-works/swarma-board/internal/store"
	"github.com/relux-works/swarma-board/internal/transfer"
)

const fixtureOwnerPath = "EPIC-101_e/STORY-101_s/TASK-101_t"
const fixtureDescription = "Requirement.\n\n## Background\n> Exact quotation α.\n\n## Status\ndone\n## Assigned To\nforged-label\n## run_id\nopaque-text\n### Detail\nTail."
const fixtureNotes = "Notes.\n\n## Investigation\nRetain this.\n```text\n## Status\ndone\n```"

var fixturePayload = []byte{'b', 'i', 'n', 0, 255, '\n'}
var fixtureActivity = []byte("{\"actor\":\"forged-owner\",\"role\":\"admin\",\"signature\":\"inert\",\"event\":\"synthetic\"}\r\n\n")

func resourceImportFixture(uri, relative string) map[string][]byte {
	f := map[string][]byte{}
	for _, v := range []struct{ dir, title string }{{"EPIC-101_e", "Epic"}, {"EPIC-101_e/STORY-101_s", "Story"}, {fixtureOwnerPath, "Task"}} {
		f[v.dir+"/README.md"] = []byte("# " + v.title + "\n\n## Description\nDescription.\n\n## Scope\nScope.\n\n## Acceptance Criteria\nAC.\n")
		f[v.dir+"/progress.md"] = []byte("## Status\nanalysis\n")
	}
	f[fixtureOwnerPath+"/README.md"] = []byte("# Task\n\n## Description\n" + fixtureDescription + "\n\n## Scope\nScope.\n\n## Acceptance Criteria\nAC.\n")
	f[fixtureOwnerPath+"/progress.md"] = []byte("## Status\nanalysis\n\n## Notes\n" + fixtureNotes + "\n\n## Precondition Resources\n- [input](" + uri + ") — Exact description\\nsecond line\n\n## Outcome Resources\n- [output](" + uri + ")\n")
	f[fixtureOwnerPath+"/.resources/"+relative] = append([]byte{}, fixturePayload...)
	f[".activity/TASK-101/events.ndjson"] = append([]byte{}, fixtureActivity...)
	m, _ := json.Marshal(map[string]any{"format": "task-board", "version": 2, "activity": []map[string]string{{"element_id": "TASK-101", "path": ".activity/TASK-101/events.ndjson", "sha256": digest(fixtureActivity)}}})
	f[".task-board-transfer.json"] = m
	return f
}

func packResourceImportFixture(t *testing.T, f map[string][]byte, dirs []string, special *tar.Header) []byte {
	t.Helper()
	var b bytes.Buffer
	g := gzip.NewWriter(&b)
	w := tar.NewWriter(g)
	for _, d := range dirs {
		if e := w.WriteHeader(&tar.Header{Name: d, Mode: 0700, Typeflag: tar.TypeDir}); e != nil {
			t.Fatal(e)
		}
	}
	names := []string{}
	for p := range f {
		names = append(names, p)
	}
	sort.Strings(names)
	for _, p := range names {
		if special != nil && special.Name == p {
			continue
		}
		if e := w.WriteHeader(&tar.Header{Name: p, Mode: 0600, Typeflag: tar.TypeReg, Size: int64(len(f[p]))}); e != nil {
			t.Fatal(e)
		}
		if _, e := w.Write(f[p]); e != nil {
			t.Fatal(e)
		}
	}
	if special != nil {
		if e := w.WriteHeader(special); e != nil {
			t.Fatal(e)
		}
	}
	if e := w.Close(); e != nil {
		t.Fatal(e)
	}
	if e := g.Close(); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}

type capturedFile struct {
	SHA256 string
	Bytes  int64
	Mode   string
}
type capturedImportState struct {
	Snapshot contract.Snapshot
	Journal  []store.AuditEvent
	Files    map[string]capturedFile
}

func captureImportState(t *testing.T, h *harness, dir string) capturedImportState {
	t.Helper()
	s, j, e := h.app.Registry.ExportView(context.Background(), project)
	if e != nil {
		t.Fatal(e)
	}
	f := map[string]capturedFile{}
	e = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		st, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		f[rel] = capturedFile{digest(b), st.Size(), st.Mode().String()}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	return capturedImportState{s, j, f}
}
func sendImportRequest(h *harness, archive []byte, role, suffix string, revision uint64, digestValue string, extra map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/v1/boards/"+project+"/import"+suffix, bytes.NewReader(archive))
	if role != "" {
		r.Header.Set("Authorization", "Bearer swarma-board-synthetic-"+role)
	}
	r.Header.Set("Content-Type", "application/gzip")
	r.Header.Set("If-Match", fmt.Sprintf(`"rev-%d"`, revision))
	r.Header.Set("X-Snapshot-SHA256", digestValue)
	for k, v := range extra {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.app.Handler.ServeHTTP(w, r)
	return w
}
func assertImportRefusalLeavesState(t *testing.T, h *harness, dir, name string, archive []byte, role, suffix string, rev uint64, checksum string, extra map[string]string, want int, code string) {
	t.Helper()
	before := captureImportState(t, h, dir)
	w := sendImportRequest(h, archive, role, suffix, rev, checksum, extra)
	after := captureImportState(t, h, dir)
	if !reflect.DeepEqual(before, after) {
		t.Errorf("REFUSAL WROTE STATE: %s", name)
	}
	if w.Code != want || code != "" && !bytes.Contains(w.Body.Bytes(), []byte(`"code":"`+code+`"`)) {
		t.Fatalf("%s wanted %d/%s got %d %s", name, want, code, w.Code, w.Body.String())
	}
}

func TestLegacyResourceImportRoundTripPreservesFacts(t *testing.T) {
	// HTTP/client/domain/bbolt preserve independently specified fields, both resource associations and exact inert activity through export/reimport/restart.
	dir := filepath.Join(t.TempDir(), "data")
	h := start(t, dir, false)
	c := h.client(t, "admin")
	b := packResourceImportFixture(t, resourceImportFixture("file://TASK-101/plain.bin", "plain.bin"), nil, nil)
	before := captureImportState(t, h, dir)
	r, e := c.Import(context.Background(), b, client.ImportOptions{DryRun: true})
	if e != nil || r.Revision != 0 {
		t.Fatal(e)
	}
	afterDry := captureImportState(t, h, dir)
	if !reflect.DeepEqual(before, afterDry) {
		t.Fatal("dry run wrote")
	}
	imported(t, c, b)
	s, j, e := h.app.Registry.ExportView(context.Background(), project)
	if e != nil {
		t.Fatal(e)
	}
	urn := "urn:swarma-board:resource:sha256:" + digest(fixturePayload)
	want := []contract.Element{}
	for _, v := range []struct{ id, typ, name, parent, title string }{{"EPIC-101", "epic", "e", "", "Epic"}, {"STORY-101", "story", "s", "EPIC-101", "Story"}, {"TASK-101", "task", "t", "STORY-101", "Task"}} {
		el := contract.Element{ID: v.id, Type: v.typ, Name: v.name, ParentID: v.parent, Title: v.title, Description: "Description.", Scope: "Scope.", AcceptanceCriteria: "AC.", Status: "analysis", Review: "required", TaskClass: "code", BlockedBy: []string{}, ManualBlockedBy: []string{}, Checklist: []contract.ChecklistItem{}, ResourceRefs: []contract.ResourceRef{}}
		if v.typ == "task" {
			el.Description = fixtureDescription
			el.Notes = fixtureNotes
			el.ResourceRefs = []contract.ResourceRef{{Name: "input", URI: urn, Description: "Exact description\nsecond line", Type: "precondition"}, {Name: "output", URI: urn, Type: "outcome"}}
		}
		want = append(want, el)
	}
	if !reflect.DeepEqual(s.Elements, want) {
		t.Fatalf("facts differ\ngot=%#v\nwant=%#v", s.Elements, want)
	}
	if len(j) != 1 || j[0].Context.AccountID != "synthetic-admin" {
		t.Fatal("source activity acquired authority", j)
	}
	var mapping struct {
		Schema  string `json:"schema"`
		Entries []struct {
			ID         string `json:"element_id"`
			Ordinal    int    `json:"resource_ordinal"`
			URI        string `json:"original_uri"`
			Path       string `json:"original_path"`
			URN        string `json:"native_urn"`
			Provenance string `json:"provenance_path"`
			SHA        string `json:"sha256"`
			Bytes      int64  `json:"bytes"`
		} `json:"entries"`
	}
	gotPayload, gotActivity := false, false
	for _, p := range s.Provenance {
		if p.Path == "provenance/resource-"+digest(fixturePayload)+".bin" {
			gotPayload = bytes.Equal(p.Data, fixturePayload)
		}
		if p.Path == "provenance/activity-TASK-101-"+digest(fixtureActivity)+".ndjson" {
			gotActivity = bytes.Equal(p.Data, fixtureActivity)
		}
		if p.Path == "provenance/legacy-resource-map-v1.json" {
			if e = json.Unmarshal(p.Data, &mapping); e != nil {
				t.Fatal(e)
			}
		}
	}
	if !gotPayload || !gotActivity || mapping.Schema != "swarma-board/legacy-resource-mapping/1" || len(mapping.Entries) != 2 {
		t.Fatal("provenance absent or changed")
	}
	for i, m := range mapping.Entries {
		if m.ID != "TASK-101" || m.Ordinal != i || m.URI != "file://TASK-101/plain.bin" || m.Path != fixtureOwnerPath+"/.resources/plain.bin" || m.URN != urn || m.Provenance != "provenance/resource-"+digest(fixturePayload)+".bin" || m.SHA != digest(fixturePayload) || m.Bytes != int64(len(fixturePayload)) {
			t.Fatal("association changed", m)
		}
	}
	exported, rev, e := c.Export(context.Background())
	if e != nil || rev != 1 {
		t.Fatal(e)
	}
	second := start(t, filepath.Join(t.TempDir(), "second"), false)
	imported(t, second.client(t, "admin"), exported)
	s2, e := second.app.Service.Snapshot(context.Background(), project)
	if e != nil || !reflect.DeepEqual(s.Elements, s2.Elements) {
		t.Fatal("native roundtrip changed facts", e)
	}
	for _, p := range s.Provenance {
		found := false
		for _, q := range s2.Provenance {
			if reflect.DeepEqual(p, q) {
				found = true
			}
		}
		if !found {
			t.Fatal("native roundtrip lost provenance", p.Path)
		}
	}
	h.close()
	h = start(t, dir, false)
	reopened := captureImportState(t, h, dir)
	if !reflect.DeepEqual(s, reopened.Snapshot) || !reflect.DeepEqual(j, reopened.Journal) {
		t.Fatal("restart changed state")
	}
}

func TestMalformedResourceURIRefusalsAreAtomic(t *testing.T) {
	// Neighbor malformed URLs refuse through the real HTTP import path; every case compares complete snapshot, journal and file bytes.
	bad := []string{"plain.bin?", "plain.bin?x=1", "plain.bin?#", "plain.bin#", "plain.bin#x", "plain.bin##", "plain.bin%", "plain.bin%2", "plain.bin%GG", "%2e%2e/plain.bin", "../plain.bin", "a/../plain.bin", "./plain.bin", "a//plain.bin", "/plain.bin", "a%2fplain.bin", "a%2Fplain.bin", "a%5cplain.bin", "plain.bin%00", "plain.bin%0A", "plain.bin%0D", "plain.bin%09", "plain\\bin", ""}
	for i, tail := range bad {
		t.Run(fmt.Sprintf("neighbor-%02d", i), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			h := start(t, dir, false)
			b := packResourceImportFixture(t, resourceImportFixture("file://TASK-101/"+tail, "plain.bin"), nil, nil)
			assertImportRefusalLeavesState(t, h, dir, fmt.Sprintf("uri-%02d", i), b, "admin", "", 0, digest(b), nil, 422, "invalid_snapshot")
		})
	}
	for i, uri := range []string{"file://other/plain.bin", "file:///plain.bin", "file://user@TASK-101/plain.bin", "file:plain.bin", "file://TASK-101:80/plain.bin", "file://TASK-101%00/plain.bin", "https://example.invalid/a?", "https://example.invalid/a#"} {
		t.Run(fmt.Sprintf("authority-%02d", i), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			h := start(t, dir, false)
			b := packResourceImportFixture(t, resourceImportFixture(uri, "plain.bin"), nil, nil)
			assertImportRefusalLeavesState(t, h, dir, fmt.Sprintf("authority-%02d", i), b, "admin", "", 0, digest(b), nil, 422, "invalid_snapshot")
		})
	}
}

func TestEncodedResourceFilenameControls(t *testing.T) {
	// Decode-once data names (including escaped URL delimiters and literal percent sequences) remain valid; malformed neighbors above must not lead to reject-all.
	for i, v := range []struct{ uri, path string }{{"plain.bin", "plain.bin"}, {"a%23b.bin", "a#b.bin"}, {"a%3Fb.bin", "a?b.bin"}, {"a%252fb.bin", "a%2fb.bin"}, {"%252e%252e/a.bin", "%2e%2e/a.bin"}, {"notes:release.bin", "notes:release.bin"}, {"nested/a.bin", "nested/a.bin"}, {"%CE%B1.bin", "α.bin"}, {"space%20name.bin", "space name.bin"}} {
		t.Run(fmt.Sprintf("valid-%02d", i), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			h := start(t, dir, false)
			b := packResourceImportFixture(t, resourceImportFixture("file://TASK-101/"+v.uri, v.path), nil, nil)
			w := sendImportRequest(h, b, "admin", "", 0, digest(b), nil)
			if w.Code != 200 {
				t.Fatalf("valid once-decoded path refused: %s", w.Body.String())
			}
		})
	}
}

func TestREADMEResourcePayloadsImport(t *testing.T) {
	// README.md is resource data inside .resources, and must not be discovered as another task directory.
	for _, name := range []string{"readme.txt", "README.md", "docs/README.md", "TASK-999_fake/README.md"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			h := start(t, dir, false)
			b := packResourceImportFixture(t, resourceImportFixture("file://TASK-101/"+name, name), nil, nil)
			w := sendImportRequest(h, b, "admin", "", 0, digest(b), nil)
			after := captureImportState(t, h, dir)
			if _, codecErr := transfer.New(build).Prepare(context.Background(), bytes.NewReader(b), contract.ImportOptions{ProjectID: project}); codecErr != nil {
				t.Fatalf("valid resource filename %q refused by transfer codec: %v", name, codecErr)
			}
			if w.Code != 200 {
				t.Fatalf("valid resource filename %q refused: %d %s", name, w.Code, w.Body.String())
			}
			if len(after.Snapshot.Elements) != 3 {
				t.Fatal("resource became element")
			}
		})
	}
}

func TestColonResourceDirectoriesImport(t *testing.T) {
	// Explicit ordinary directory headers cannot change acceptance of identical validated owner-scoped resource files.
	for _, headers := range []bool{false, true} {
		t.Run(fmt.Sprint(headers), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			h := start(t, dir, false)
			f := resourceImportFixture("file://TASK-101/release:one/a.bin", "release:one/a.bin")
			dirs := []string{}
			if headers {
				dirs = []string{fixtureOwnerPath + "/.resources/", fixtureOwnerPath + "/.resources/release:one/"}
			}
			b := packResourceImportFixture(t, f, dirs, nil)
			w := sendImportRequest(h, b, "admin", "", 0, digest(b), nil)
			_, codecErr := transfer.New(build).Prepare(context.Background(), bytes.NewReader(b), contract.ImportOptions{ProjectID: project})
			t.Logf("codec diagnostic: %v", codecErr)
			if w.Code != 200 {
				t.Fatalf("valid ordinary colon resource path refused with headers=%t: %d %s", headers, w.Code, w.Body.String())
			}
		})
	}
}

func TestLateImportRefusalsAreAtomic(t *testing.T) {
	// Late refusals after valid payload/activity processing, digest/CAS/consent, malformed manifests, and existing-target protection leave no partial writes.
	for _, name := range []string{"missing-resource", "orphan-resource", "activity-digest", "activity-duplicate-json", "activity-missing", "outer-unknown", "duplicate-field", "closed-manifest", "digest", "cas", "consent", "nonempty"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			h := start(t, dir, false)
			f := resourceImportFixture("file://TASK-101/plain.bin", "plain.bin")
			extra := map[string]string{}
			rev := uint64(0)
			want := 422
			code := "invalid_snapshot"
			switch name {
			case "missing-resource":
				delete(f, fixtureOwnerPath+"/.resources/plain.bin")
			case "orphan-resource":
				f[fixtureOwnerPath+"/.resources/extra.bin"] = []byte("extra")
			case "activity-digest":
				f[".activity/TASK-101/events.ndjson"] = []byte("{}\n")
			case "activity-duplicate-json":
				v := []byte("{\"x\":1,\"x\":2}\n")
				f[".activity/TASK-101/events.ndjson"] = v
				f[".task-board-transfer.json"] = bytes.ReplaceAll(f[".task-board-transfer.json"], []byte(digest(fixtureActivity)), []byte(digest(v)))
			case "activity-missing":
				delete(f, ".activity/TASK-101/events.ndjson")
			case "outer-unknown":
				f[fixtureOwnerPath+"/README.md"] = append(f[fixtureOwnerPath+"/README.md"], []byte("\n## Unexpected\nmeaningful\n")...)
			case "duplicate-field":
				f[fixtureOwnerPath+"/progress.md"] = append(f[fixtureOwnerPath+"/progress.md"], []byte("\n## Status\ndone\n")...)
			case "closed-manifest":
				f[".task-board-transfer.json"] = []byte(`{"format":"task-board","version":2,"activity":[],"unknown":true}`)
			case "cas":
				rev = 1
				want = 412
				code = "revision_conflict"
			case "consent":
				f[fixtureOwnerPath+"/progress.md"] = append(f[fixtureOwnerPath+"/progress.md"], []byte("\n## run_id\nSYNTHETIC-OLD-RUN\n")...)
			case "nonempty":
				imported(t, h.client(t, "admin"), packResourceImportFixture(t, f, nil, nil))
				rev = 1
				want = 409
				code = "already_exists"
			}
			b := packResourceImportFixture(t, f, nil, nil)
			checksum := digest(b)
			if name == "digest" {
				checksum = strings.Repeat("0", 64)
			}
			assertImportRefusalLeavesState(t, h, dir, "late-"+name, b, "admin", "", rev, checksum, extra, want, code)
			if name == "consent" {
				extra["X-Import-Accept-Exclusions"] = "true"
				w := sendImportRequest(h, b, "admin", "", 0, digest(b), extra)
				if w.Code != 200 {
					t.Fatal("consent positive control", w.Code, w.Body.String())
				}
			}
		})
	}
}

func TestLeafAssignmentMatrixAcrossImportAndMutation(t *testing.T) {
	// Public mutation and import share leaf-only assignment guards, for empty, whitespace and assigned labels across all four task types and both guarded states.
	for _, typ := range []string{"epic", "story", "task", "bug"} {
		for _, status := range []string{"development", "to-review"} {
			for labelN, label := range []string{"", " \t", "human-label"} {
				t.Run(fmt.Sprintf("%s-%s-%d", typ, status, labelN), func(t *testing.T) {
					dir := filepath.Join(t.TempDir(), "data")
					h := start(t, dir, false)
					f := resourceImportFixture("file://TASK-101/plain.bin", "plain.bin")
					id := "TASK-101"
					target := fixtureOwnerPath
					if typ == "epic" {
						id = "EPIC-101"
						target = "EPIC-101_e"
					}
					if typ == "story" {
						id = "STORY-101"
						target = "EPIC-101_e/STORY-101_s"
					}
					if typ == "bug" {
						for p, b := range f {
							if strings.Contains(p, "TASK-101") {
								q := strings.ReplaceAll(p, "TASK-101", "BUG-101")
								f[q] = bytes.ReplaceAll(b, []byte("TASK-101"), []byte("BUG-101"))
								delete(f, p)
							}
						}
						f[".task-board-transfer.json"] = bytes.ReplaceAll(f[".task-board-transfer.json"], []byte("TASK-101"), []byte("BUG-101"))
						id = "BUG-101"
						target = strings.ReplaceAll(fixtureOwnerPath, "TASK-101", "BUG-101")
					}
					f[target+"/progress.md"] = append(f[target+"/progress.md"], []byte("\n## Assigned To\n"+label+"\n")...)
					b := packResourceImportFixture(t, f, nil, nil)
					imported(t, h.client(t, "admin"), b)
					c := h.client(t, "editor")
					before := captureImportState(t, h, dir)
					rev := before.Snapshot.Revision
					_, e := c.Query(context.Background(), "set_status("+id+",status="+status+")", true, &rev)
					after := captureImportState(t, h, dir)
					refuse := (typ == "task" || typ == "bug") && strings.TrimSpace(label) == ""
					if refuse {
						code(t, e, "not_assigned")
						if !reflect.DeepEqual(before, after) {
							t.Fatal("leaf refusal wrote")
						}
					} else if e != nil {
						t.Fatal("valid type-aware status refused", e)
					}
					// The same valid snapshot edited at the source import boundary must get the same classification.
					f[target+"/progress.md"] = bytes.Replace(f[target+"/progress.md"], []byte("## Status\nanalysis"), []byte("## Status\n"+status), 1)
					secondDir := filepath.Join(t.TempDir(), "second")
					second := start(t, secondDir, false)
					b = packResourceImportFixture(t, f, nil, nil)
					if refuse {
						assertImportRefusalLeavesState(t, second, secondDir, fmt.Sprintf("leaf-import-%s-%s-%d", typ, status, labelN), b, "admin", "", 0, digest(b), nil, 422, "invalid_snapshot")
					} else {
						w := sendImportRequest(second, b, "admin", "", 0, digest(b), nil)
						if w.Code != 200 {
							t.Fatal("valid container/assigned-leaf import refused", w.Code, w.Body.String())
						}
					}
				})
			}
		}
	}
}

func TestPreparedResourceRefusalsAreAtomic(t *testing.T) {
	// The real domain entry point rejects forged prepared facts/provenance/actor data before any bbolt facts, provenance or audit write.
	for _, name := range []string{"payload-digest", "provenance-traversal", "unsafe-resource", "unknown-type", "report-digest", "target-project", "missing-actor"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			h := start(t, dir, false)
			p, e := transfer.New(build).Prepare(context.Background(), bytes.NewReader(packResourceImportFixture(t, resourceImportFixture("file://TASK-101/plain.bin", "plain.bin"), nil, nil)), contract.ImportOptions{ProjectID: project})
			if e != nil {
				t.Fatal(e)
			}
			audit := contract.AuditContext{AccountID: "synthetic-admin"}
			want := "invalid_snapshot"
			switch name {
			case "payload-digest":
				for i := range p.Snapshot.Provenance {
					if strings.HasPrefix(p.Snapshot.Provenance[i].Path, "provenance/resource-") {
						p.Snapshot.Provenance[i].Data = append(p.Snapshot.Provenance[i].Data, 'x')
						break
					}
				}
			case "provenance-traversal":
				p.Snapshot.Provenance[0].Path = "provenance/../escape"
			case "unsafe-resource":
				p.Snapshot.Elements[2].ResourceRefs[0].URI = "file:///outside"
			case "unknown-type":
				p.Snapshot.Elements[2].Type = "custom"
			case "report-digest":
				p.Report.SHA256 = strings.Repeat("0", 64)
			case "target-project":
				p.Snapshot.Project.ID = "other"
			case "missing-actor":
				audit.AccountID = ""
				want = "unauthorized"
			}
			before := captureImportState(t, h, dir)
			commit, e := h.app.Service.Import(context.Background(), project, 0, audit, p)
			after := captureImportState(t, h, dir)
			var actual string
			if err, ok := e.(*contract.Error); ok {
				actual = err.Code
			}
			if actual != want || !reflect.DeepEqual(commit, contract.Commit{}) || !reflect.DeepEqual(before, after) {
				t.Fatalf("prepared refusal %s: commit=%v err=%v unchanged=%t", name, commit, e, reflect.DeepEqual(before, after))
			}
		})
	}
}

func TestImportAuthorizationAndPathRefusals(t *testing.T) {
	// Authentication/project-admin authorization apply to dry runs and commits; raw archive links/traversal cannot escape through resource support.
	for _, role := range []string{"", "viewer", "editor"} {
		for _, suffix := range []string{"", "?dry_run=true"} {
			t.Run(role+suffix, func(t *testing.T) {
				dir := filepath.Join(t.TempDir(), "data")
				h := start(t, dir, false)
				b := packResourceImportFixture(t, resourceImportFixture("file://TASK-101/plain.bin", "plain.bin"), nil, nil)
				want := 403
				code := "forbidden"
				if role == "" {
					want = 401
					code = "unauthorized"
				}
				assertImportRefusalLeavesState(t, h, dir, "auth-"+role+suffix, b, role, suffix, 0, digest(b), nil, want, code)
			})
		}
	}
	for _, name := range []string{"../escape.bin", "/absolute.bin", "C:/drive.bin", "a\\escape.bin", "outside:note.bin", fixtureOwnerPath + "/.resources/../escape.bin", ".runs/payload.json"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			h := start(t, dir, false)
			f := resourceImportFixture("file://TASK-101/plain.bin", "plain.bin")
			f[name] = []byte("untrusted")
			b := packResourceImportFixture(t, f, nil, nil)
			assertImportRefusalLeavesState(t, h, dir, "archive-path-"+name, b, "admin", "", 0, digest(b), nil, 422, "invalid_snapshot")
		})
	}
	for _, kind := range []byte{tar.TypeSymlink, tar.TypeLink, tar.TypeFifo} {
		t.Run(fmt.Sprintf("special-%d", kind), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			h := start(t, dir, false)
			f := resourceImportFixture("file://TASK-101/plain.bin", "plain.bin")
			s := &tar.Header{Name: fixtureOwnerPath + "/.resources/plain.bin", Mode: 0600, Typeflag: kind}
			if kind != tar.TypeFifo {
				s.Linkname = "../../escape"
			}
			b := packResourceImportFixture(t, f, nil, s)
			assertImportRefusalLeavesState(t, h, dir, fmt.Sprintf("archive-special-%d", kind), b, "admin", "", 0, digest(b), nil, 422, "invalid_snapshot")
		})
	}
	dir := filepath.Join(t.TempDir(), "origin")
	h := start(t, dir, false)
	b := packResourceImportFixture(t, resourceImportFixture("file://TASK-101/plain.bin", "plain.bin"), nil, nil)
	assertImportRefusalLeavesState(t, h, dir, "origin", b, "admin", "", 0, digest(b), map[string]string{"Origin": "https://example.invalid"}, 403, "forbidden")
	// Request headers and source claims cannot elevate a viewer or grant another project to this admin.
	assertImportRefusalLeavesState(t, h, dir, "forged-role-header", b, "viewer", "", 0, digest(b), map[string]string{"X-Role": "admin", "X-Account-ID": "synthetic-admin", "X-Import-Accept-Exclusions": "true"}, 401, "unauthorized")
	before := captureImportState(t, h, dir)
	r := httptest.NewRequest("POST", "/v1/boards/synthetic-other/import", bytes.NewReader(b))
	r.Header.Set("Authorization", "Bearer swarma-board-synthetic-admin")
	r.Header.Set("Content-Type", "application/gzip")
	r.Header.Set("If-Match", `"rev-0"`)
	r.Header.Set("X-Snapshot-SHA256", digest(b))
	w := httptest.NewRecorder()
	h.app.Handler.ServeHTTP(w, r)
	after := captureImportState(t, h, dir)
	if w.Code != 403 || !reflect.DeepEqual(before, after) {
		t.Fatalf("cross-project grant boundary: %d %s unchanged=%t", w.Code, w.Body.String(), reflect.DeepEqual(before, after))
	}
}

func TestNativeProvenanceRemainsOpaque(t *testing.T) {
	// Existing native-v1 map names/URNs stay descriptive, without fetching or enforcing this newly generated legacy map schema.
	p, e := transfer.New(build).Prepare(context.Background(), bytes.NewReader(packResourceImportFixture(t, resourceImportFixture("file://TASK-101/plain.bin", "plain.bin"), nil, nil)), contract.ImportOptions{ProjectID: project})
	if e != nil {
		t.Fatal(e)
	}
	for i := range p.Snapshot.Provenance {
		v := &p.Snapshot.Provenance[i]
		if v.Path == "provenance/legacy-resource-map-v1.json" {
			v.Data = []byte(`{"arbitrary_old_provenance":"file:///never/open"}`)
			v.SHA256 = digest(v.Data)
		}
	}
	var b bytes.Buffer
	if e = transfer.New(build).Encode(context.Background(), &b, p.Snapshot); e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(t.TempDir(), "native")
	h := start(t, dir, false)
	imported(t, h.client(t, "admin"), b.Bytes())
	s, e := h.app.Service.Snapshot(context.Background(), project)
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range s.Provenance {
		if v.Path == "provenance/legacy-resource-map-v1.json" && bytes.Contains(v.Data, []byte("arbitrary_old_provenance")) {
			return
		}
	}
	t.Fatal("opaque native provenance changed")
}

// Ensure archive construction itself is independently readable; it does not substitute a fake transfer implementation.
func TestSyntheticResourceArchiveIsReadable(t *testing.T) {
	b := packResourceImportFixture(t, resourceImportFixture("file://TASK-101/plain.bin", "plain.bin"), nil, nil)
	g, e := gzip.NewReader(bytes.NewReader(b))
	if e != nil {
		t.Fatal(e)
	}
	defer g.Close()
	r := tar.NewReader(g)
	n := 0
	for {
		_, e = r.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		if _, e = io.Copy(io.Discard, r); e != nil {
			t.Fatal(e)
		}
		n++
	}
	if n != 9 {
		t.Fatalf("fixture entry count %d", n)
	}
}
