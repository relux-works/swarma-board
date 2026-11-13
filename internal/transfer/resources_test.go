package transfer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/relux-works/swarma-board/internal/domain"
	"github.com/relux-works/swarma-board/internal/store"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/relux-works/swarma-board/internal/contract"
)

const resourceTaskDir = "EPIC-001_synthetic-epic/STORY-001_first-story/TASK-001_a"

func resourceFixture(t *testing.T) map[string][]byte {
	t.Helper()
	files := unpack(t, fixture(t, "legacy-v2"))
	files[resourceTaskDir+"/.resources/plain.txt"] = []byte("neutral resource\n")
	files[resourceTaskDir+"/.resources/release:notes.txt"] = []byte("neutral resource\n")
	files[resourceTaskDir+"/progress.md"] = append(files[resourceTaskDir+"/progress.md"], []byte("## Precondition Resources\n- [plan](file://TASK-001/plain.txt) — original description\n\n## Outcome Resources\n- [result](file://TASK-001/release:notes.txt)\n")...)
	return files
}

func TestLegacyResourcesLosslessMappingAndNativeRoundtrip(t *testing.T) {
	// Exact owner-scoped file references become inert URNs; all other facts and both original associations survive dedup/export.
	baseline := mustPrepare(t, fixture(t, "legacy-v2"))
	files := resourceFixture(t)
	p := mustPrepare(t, pack(t, files))
	digest := digestBytes([]byte("neutral resource\n"))
	urn := "urn:swarma-board:resource:sha256:" + digest
	payloadPath := "provenance/resource-" + digest + ".bin"
	expected := baseline.Snapshot.Elements
	for i := range expected {
		if expected[i].ID == "TASK-001" {
			expected[i].ResourceRefs = []contract.ResourceRef{{Name: "plan", URI: urn, Description: "original description", Type: "precondition"}, {Name: "result", URI: urn, Type: "outcome"}}
		}
	}
	if !reflect.DeepEqual(p.Snapshot.Elements, expected) || len(p.Report.Exclusions) != 0 {
		t.Fatal("resource projection changed unrelated fields/exclusions")
	}
	payloads := 0
	var mapping []byte
	for _, f := range p.Snapshot.Provenance {
		if f.Path == payloadPath {
			payloads++
			if !bytes.Equal(f.Data, []byte("neutral resource\n")) || f.SHA256 != digest {
				t.Fatal("payload bytes/digest changed")
			}
		}
		if f.Path == "provenance/legacy-resource-map-v1.json" {
			mapping = f.Data
		}
	}
	if payloads != 1 {
		t.Fatal("identical payloads did not deduplicate")
	}
	var record struct {
		Schema  string `json:"schema"`
		Entries []struct {
			ElementID  string `json:"element_id"`
			Ordinal    int    `json:"resource_ordinal"`
			URI        string `json:"original_uri"`
			Path       string `json:"original_path"`
			URN        string `json:"native_urn"`
			Provenance string `json:"provenance_path"`
			SHA        string `json:"sha256"`
			Bytes      int64  `json:"bytes"`
		} `json:"entries"`
	}
	if err := strictJSON(mapping, &record); err != nil {
		t.Fatal(err)
	}
	if record.Schema != "swarma-board/legacy-resource-mapping/1" || len(record.Entries) != 2 {
		t.Fatal("mapping schema/count")
	}
	for i, e := range record.Entries {
		name := []string{"plain.txt", "release:notes.txt"}[i]
		if e.ElementID != "TASK-001" || e.Ordinal != i || e.URI != "file://TASK-001/"+name || e.Path != resourceTaskDir+"/.resources/"+name || e.URN != urn || e.Provenance != payloadPath || e.SHA != digest || e.Bytes != int64(len("neutral resource\n")) {
			t.Fatal("original mapping association changed")
		}
	}
	if len(p.Report.Warnings) != 1 || !strings.Contains(p.Report.Warnings[0], "reversible") {
		t.Fatal("mapping not declared in import report")
	}
	var encoded bytes.Buffer
	if err := New("dev").Encode(context.Background(), &encoded, p.Snapshot); err != nil {
		t.Fatal(err)
	}
	q := mustPrepare(t, encoded.Bytes())
	if !reflect.DeepEqual(q.Snapshot.Elements, p.Snapshot.Elements) {
		t.Fatal("native roundtrip changed element facts")
	}
	for _, f := range q.Snapshot.Provenance {
		if f.Path == "provenance/legacy-resource-map-v1.json" && !bytes.Equal(f.Data, mapping) {
			t.Fatal("native roundtrip changed opaque mapping")
		}
	}
}

func TestLegacyResourceConstructionRefusals(t *testing.T) {
	// Each malformed association refuses specifically, returning no partially prepared elements or payloads; the valid neighbor imports.
	mustPrepare(t, pack(t, resourceFixture(t)))
	for name, edit := range map[string]func(map[string][]byte){
		"missing payload": func(f map[string][]byte) { delete(f, resourceTaskDir+"/.resources/plain.txt") },
		"orphan payload":  func(f map[string][]byte) { f[resourceTaskDir+"/.resources/orphan.txt"] = []byte("neutral") },
		"foreign authority": func(f map[string][]byte) {
			f[resourceTaskDir+"/progress.md"] = bytes.ReplaceAll(f[resourceTaskDir+"/progress.md"], []byte("file://TASK-001/plain.txt"), []byte("file://TASK-003/plain.txt"))
		},
		"absolute local URI": func(f map[string][]byte) {
			f[resourceTaskDir+"/progress.md"] = bytes.ReplaceAll(f[resourceTaskDir+"/progress.md"], []byte("file://TASK-001/plain.txt"), []byte("file:///plain.txt"))
		},
		"query": func(f map[string][]byte) {
			f[resourceTaskDir+"/progress.md"] = bytes.ReplaceAll(f[resourceTaskDir+"/progress.md"], []byte("file://TASK-001/plain.txt"), []byte("file://TASK-001/plain.txt?query=value"))
		},
		"fragment": func(f map[string][]byte) {
			f[resourceTaskDir+"/progress.md"] = bytes.ReplaceAll(f[resourceTaskDir+"/progress.md"], []byte("file://TASK-001/plain.txt"), []byte("file://TASK-001/plain.txt#detail"))
		},
		"empty fragment": func(f map[string][]byte) {
			f[resourceTaskDir+"/progress.md"] = bytes.ReplaceAll(f[resourceTaskDir+"/progress.md"], []byte("file://TASK-001/plain.txt"), []byte("file://TASK-001/plain.txt#"))
		},
		"userinfo": func(f map[string][]byte) {
			f[resourceTaskDir+"/progress.md"] = bytes.ReplaceAll(f[resourceTaskDir+"/progress.md"], []byte("file://TASK-001/plain.txt"), []byte("file://label@TASK-001/plain.txt"))
		},
		"encoded traversal": func(f map[string][]byte) {
			f[resourceTaskDir+"/progress.md"] = bytes.ReplaceAll(f[resourceTaskDir+"/progress.md"], []byte("file://TASK-001/plain.txt"), []byte("file://TASK-001/%2e%2e/plain.txt"))
		},
		"encoded separator": func(f map[string][]byte) {
			f[resourceTaskDir+"/progress.md"] = bytes.ReplaceAll(f[resourceTaskDir+"/progress.md"], []byte("file://TASK-001/plain.txt"), []byte("file://TASK-001/nested%2fplain.txt"))
		},
		"supplied conflicting map": func(f map[string][]byte) {
			f["provenance/legacy-resource-map-v1.json"] = []byte(`{"schema":"untrusted"}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := resourceFixture(t)
			edit(f)
			want := "invalid legacy file resource reference"
			switch name {
			case "missing payload":
				want = "legacy resource payload missing"
			case "orphan payload":
				want = "orphan legacy resource payload"
			case "supplied conflicting map":
				want = "unrecognized legacy artifact"
			}
			p, err := New("dev").Prepare(context.Background(), bytes.NewReader(pack(t, f)), contract.ImportOptions{ProjectID: "synthetic-board"})
			var ce *contract.Error
			if !errors.As(err, &ce) || ce.Code != "invalid_snapshot" || !strings.Contains(ce.Message, want) || !reflect.DeepEqual(p, contract.PreparedImport{}) {
				t.Fatalf("wanted atomic %q refusal, got %v", want, err)
			}
		})
	}
	// A changed archive cannot reuse the custodian's expected digest, even when its new payload could otherwise be mapped.
	f := resourceFixture(t)
	original := pack(t, f)
	f[resourceTaskDir+"/.resources/plain.txt"] = []byte("changed neutral bytes")
	p, err := New("dev").Prepare(context.Background(), bytes.NewReader(pack(t, f)), contract.ImportOptions{ProjectID: "synthetic-board", ExpectedSHA256: digestBytes(original)})
	var ce *contract.Error
	if !errors.As(err, &ce) || ce.Code != "invalid_snapshot" || ce.Message != "archive digest mismatch" || !reflect.DeepEqual(p, contract.PreparedImport{}) {
		t.Fatal("digest mismatch did not refuse atomically", err)
	}
}

func TestResourceColonAdmissionStaysFormatAndNamespaceScoped(t *testing.T) {
	// A reserved ordinary filename may contain colon; native, malformed manifests and other namespaces retain old path refusal.
	mustPrepare(t, pack(t, resourceFixture(t)))
	for _, edit := range []func(map[string][]byte){
		func(f map[string][]byte) { f["outside:label.txt"] = []byte("neutral") },
		func(f map[string][]byte) {
			f[legacyManifestPath] = []byte(`{"format":"task-board","version":1,"activity":[]}`)
		},
		func(f map[string][]byte) {
			f[legacyManifestPath] = []byte(`{"format":"task-board","version":2,"activity":[],"unknown":true}`)
		},
		func(f map[string][]byte) { delete(f, resourceTaskDir+"/README.md") },
	} {
		f := resourceFixture(t)
		edit(f)
		refused(t, pack(t, f))
	}
	f := unpack(t, fixture(t, "native-v1"))
	f["provenance/label:note.txt"] = []byte("neutral")
	refused(t, pack(t, f))
}

func TestNativeV1ResourceMappingNamesRemainOpaque(t *testing.T) {
	// Existing native v1 never interprets proposed mapping names or generic resource URNs as new authority/schema.
	archive := native(t, nil, func(m *nativeManifest, f map[string][]byte) {
		p := "provenance/legacy-resource-map-v1.json"
		f[p] = []byte(`{"historical_opaque_note":"neutral content"}`)
		m.Files = append(m.Files, fileDigest{Path: p, SHA256: digestBytes(f[p])})
		sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	})
	mustPrepare(t, archive)
	files := unpack(t, fixture(t, "native-v1"))
	var doc elementDocument
	if err := json.Unmarshal(files["elements.json"], &doc); err != nil {
		t.Fatal(err)
	}
	doc.Elements[0].ResourceRefs = []contract.ResourceRef{{Name: "neutral", URI: "urn:swarma-board:resource:sha256:" + strings.Repeat("a", 64), Type: "precondition"}}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	mustPrepare(t, native(t, raw, nil))
}

func TestMappedResourceImportFailureAtomicAndRestart(t *testing.T) {
	// Corrupt prepared resource bytes cannot create facts/provenance/revision; a valid control persists exact mapped data after reopen.
	p := mustPrepare(t, pack(t, resourceFixture(t)))
	ctx := context.Background()
	project := p.Snapshot.Project
	file := filepath.Join(t.TempDir(), "copy.db")
	db, err := store.Open(file, project, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	svc, err := domain.New(db, func() time.Time { return now }, func(string) (string, error) { return "", errors.New("unused") })
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(p)
	var corrupted contract.PreparedImport
	if json.Unmarshal(raw, &corrupted) != nil {
		t.Fatal("copy")
	}
	for i := range corrupted.Snapshot.Provenance {
		if strings.HasPrefix(corrupted.Snapshot.Provenance[i].Path, "provenance/resource-") {
			corrupted.Snapshot.Provenance[i].Data = append(corrupted.Snapshot.Provenance[i].Data, 'x')
			break
		}
	}
	audit := contract.AuditContext{AccountID: "fixture-owner", At: now}
	commit, err := svc.Import(ctx, project.ID, 0, audit, corrupted)
	var ce *contract.Error
	if !errors.As(err, &ce) || ce.Code != "invalid_snapshot" || commit.Revision != 0 {
		t.Fatal("corrupt payload did not refuse", err)
	}
	before, err := db.View(ctx, project.ID)
	if err != nil || before.Revision != 0 || len(before.Elements) != 0 || len(before.Provenance) != 0 {
		t.Fatal("refusal changed target")
	}
	commit, err = svc.Import(ctx, project.ID, 0, audit, p)
	if err != nil || commit.Revision != 1 {
		t.Fatal("valid control failed", err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(file, project, false)
	if err != nil {
		t.Fatal(err)
	}
	after, err := db.View(ctx, project.ID)
	if err != nil || after.Revision != 1 || !reflect.DeepEqual(after.Elements, p.Snapshot.Elements) {
		t.Fatal("restart changed facts", err)
	}
	found := map[string]contract.ProvenanceFile{}
	for _, f := range after.Provenance {
		found[f.Path] = f
	}
	for _, f := range p.Snapshot.Provenance {
		if !reflect.DeepEqual(found[f.Path], f) {
			t.Fatal("restart changed provenance")
		}
	}
}

func TestResourceColonArchiveGateRefusesOtherContexts(t *testing.T) {
	// Format/owner gating happens in the bounded archive reader itself; later Markdown/schema refusal cannot hide an admitted unsafe path.
	valid := resourceFixture(t)
	files, _, _, err := readArchive(context.Background(), bytes.NewReader(pack(t, valid)))
	if err != nil || len(files) != len(valid) {
		t.Fatal("valid colon neighbor refused", err)
	}
	for name, edit := range map[string]func(map[string][]byte){
		"wrong version": func(f map[string][]byte) {
			f[legacyManifestPath] = []byte(`{"format":"task-board","version":1,"activity":[]}`)
		},
		"unknown manifest key": func(f map[string][]byte) {
			f[legacyManifestPath] = []byte(`{"format":"task-board","version":2,"activity":[],"unknown":true}`)
		},
		"duplicate manifest key": func(f map[string][]byte) {
			f[legacyManifestPath] = []byte(`{"format":"task-board","version":2,"version":2,"activity":[]}`)
		},
		"missing owner":   func(f map[string][]byte) { delete(f, resourceTaskDir+"/README.md") },
		"native context":  func(f map[string][]byte) { f[nativeManifestPath] = []byte(`{"format":"swarma-board","version":1}`) },
		"other namespace": func(f map[string][]byte) { f["elsewhere:note.txt"] = []byte("neutral") },
		"drive root":      func(f map[string][]byte) { f["C:/copy/.resources/note:label.txt"] = []byte("neutral") },
		"absolute root":   func(f map[string][]byte) { f["/copy/.resources/note:label.txt"] = []byte("neutral") },
		"URI root": func(f map[string][]byte) {
			f["https://example.invalid/copy/.resources/note:label.txt"] = []byte("neutral")
		},
		"traversal": func(f map[string][]byte) { f[resourceTaskDir+"/.resources/../note:label.txt"] = []byte("neutral") },
		"backslash": func(f map[string][]byte) { f[resourceTaskDir+"/.resources/back\\note:label.txt"] = []byte("neutral") },
	} {
		t.Run(name, func(t *testing.T) {
			f := resourceFixture(t)
			edit(f)
			got, dirs, digest, err := readArchive(context.Background(), bytes.NewReader(pack(t, f)))
			var ce *contract.Error
			if !errors.As(err, &ce) || ce.Code != "invalid_snapshot" || ce.Message != "unsafe archive path or metadata" || got != nil || dirs != nil || digest != "" {
				t.Fatalf("format/path gate failed: files=%d dirs=%d digest-present=%t err=%v", len(got), len(dirs), digest != "", err)
			}
		})
	}
}

func TestGeneratedResourceMappingRetainsEntryBudget(t *testing.T) {
	// A bounded source can expand into mapping metadata; refuse at the existing entry budget without increasing it, while a small control succeeds.
	for _, count := range []int{10, 6000} {
		f := unpack(t, fixture(t, "legacy-v2"))
		label := strings.Repeat("a", 700) + ".txt"
		f[resourceTaskDir+"/.resources/"+label] = []byte("neutral")
		var body strings.Builder
		body.Write(f[resourceTaskDir+"/progress.md"])
		body.WriteString("## Precondition Resources\n")
		for i := 0; i < count; i++ {
			body.WriteString(fmt.Sprintf("- [reference-%d](file://TASK-001/%s)\n", i, label))
		}
		if body.Len() > int(MaxEntry) {
			t.Fatal("control setup exceeded source-entry budget")
		}
		f[resourceTaskDir+"/progress.md"] = []byte(body.String())
		p, err := New("dev").Prepare(context.Background(), bytes.NewReader(pack(t, f)), contract.ImportOptions{ProjectID: "synthetic-board"})
		if count == 10 {
			if err != nil {
				t.Fatal("valid mapping budget control refused", err)
			}
			continue
		}
		var ce *contract.Error
		if !errors.As(err, &ce) || ce.Code != "invalid_snapshot" || ce.Message != "legacy resource mapping size limit" || !reflect.DeepEqual(p, contract.PreparedImport{}) {
			t.Fatal("generated mapping entry limit not enforced specifically", err)
		}
	}
}

func resourceFixtureWithPayload(t *testing.T, relative string, payload []byte) map[string][]byte {
	t.Helper()
	files := resourceFixture(t)
	delete(files, resourceTaskDir+"/.resources/plain.txt")
	files[resourceTaskDir+"/.resources/"+relative] = append([]byte(nil), payload...)
	progressPath := resourceTaskDir + "/progress.md"
	progress := string(files[progressPath])
	oldReference := "file://TASK-001/plain.txt"
	if !strings.Contains(progress, oldReference) {
		t.Fatal("resource fixture lost its first reference")
	}
	files[progressPath] = []byte(strings.Replace(progress, oldReference, "file://TASK-001/"+relative, 1))
	return files
}

func resourceEntriesForTest(t *testing.T, prepared contract.PreparedImport) []legacyResourceEntry {
	t.Helper()
	for _, file := range prepared.Snapshot.Provenance {
		if file.Path == resourceMapPath {
			var mapping legacyResourceMap
			if err := json.Unmarshal(file.Data, &mapping); err != nil {
				t.Fatal(err)
			}
			return mapping.Entries
		}
	}
	t.Fatal("legacy resource map is missing")
	return nil
}

// This synthetic archive keeps owner-contained README payloads and their ordinal; filename and content do not create elements.
func TestLegacyResourceREADMEIsPayload(t *testing.T) {
	metadataLookingPayload := []byte("# TASK-999_fake_readme\n\n## Description\nResource bytes.\n\n## Scope\nFixture payload.\n\n## Acceptance Criteria\nPreserve these exact bytes.\n")
	baseline := mustPrepare(t, fixture(t, "legacy-v2"))
	for _, relative := range []string{"README.md", "docs/README.md", "TASK-999_fake/README.md", "readme.txt"} {
		t.Run(relative, func(t *testing.T) {
			prepared := mustPrepare(t, pack(t, resourceFixtureWithPayload(t, relative, metadataLookingPayload)))
			if len(prepared.Snapshot.Elements) != len(baseline.Snapshot.Elements) {
				t.Fatalf("resource created or removed an element: got %d want %d", len(prepared.Snapshot.Elements), len(baseline.Snapshot.Elements))
			}
			for i := range baseline.Snapshot.Elements {
				if prepared.Snapshot.Elements[i].ID != baseline.Snapshot.Elements[i].ID {
					t.Fatalf("element order or identity changed at %d: got %s want %s", i, prepared.Snapshot.Elements[i].ID, baseline.Snapshot.Elements[i].ID)
				}
			}
			entries := resourceEntriesForTest(t, prepared)
			if len(entries) != 2 {
				t.Fatalf("resource count changed: got %d want 2", len(entries))
			}
			first := entries[0]
			if first.ElementID != "TASK-001" || first.ResourceOrdinal != 0 || first.OriginalURI != "file://TASK-001/"+relative || first.OriginalPath != resourceTaskDir+"/.resources/"+relative || first.Bytes != int64(len(metadataLookingPayload)) {
				t.Fatalf("README resource association changed: %+v", first)
			}
			second := entries[1]
			if second.ElementID != "TASK-001" || second.ResourceOrdinal != 1 || second.OriginalURI != "file://TASK-001/release:notes.txt" {
				t.Fatalf("neighboring resource order changed: %+v", second)
			}
			wantPayloadPath := "provenance/resource-" + digestBytes(metadataLookingPayload) + ".bin"
			foundPayload := false
			for _, file := range prepared.Snapshot.Provenance {
				if file.Path == wantPayloadPath && bytes.Equal(file.Data, metadataLookingPayload) {
					foundPayload = true
				}
			}
			if !foundPayload {
				t.Fatal("resource payload bytes changed or were dropped")
			}
		})
	}
}

func packWithDirectoryHeaders(t *testing.T, files map[string][]byte, directories []string, directoriesFirst bool) []byte {
	t.Helper()
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gz)
	writeDirectories := func() {
		t.Helper()
		for _, name := range directories {
			if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0700, Typeflag: tar.TypeDir}); err != nil {
				t.Fatal(err)
			}
		}
	}
	writeFiles := func() {
		t.Helper()
		names := make([]string, 0, len(files))
		for name := range files {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			data := files[name]
			if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0600, Typeflag: tar.TypeReg, Size: int64(len(data))}); err != nil {
				t.Fatal(err)
			}
			if _, err := tarWriter.Write(data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if directoriesFirst {
		writeDirectories()
		writeFiles()
	} else {
		writeFiles()
		writeDirectories()
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

// The no-header archive is the control; nested owner-scoped headers keep the same resource bytes and original order.
func TestLegacyColonResourceDirectoryHeadersPreserveMapping(t *testing.T) {
	files := resourceFixture(t)
	delete(files, resourceTaskDir+"/.resources/release:notes.txt")
	resourcePath := resourceTaskDir + "/.resources/release:one/a.bin"
	files[resourcePath] = []byte("nested colon resource\x00bytes")
	progressPath := resourceTaskDir + "/progress.md"
	files[progressPath] = []byte(strings.Replace(string(files[progressPath]), "file://TASK-001/release:notes.txt", "file://TASK-001/release:one/a.bin", 1))
	baseline := mustPrepare(t, pack(t, files))
	directories := []string{resourceTaskDir + "/.resources/", resourceTaskDir + "/.resources/release:one/"}
	cases := []struct {
		name             string
		directories      []string
		directoriesFirst bool
	}{
		{name: "canonical-parent-first", directories: directories, directoriesFirst: true},
		{name: "nested-header-first", directories: []string{directories[1], directories[0]}, directoriesFirst: true},
		{name: "headers-after-files", directories: directories, directoriesFirst: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			prepared := mustPrepare(t, packWithDirectoryHeaders(t, files, testCase.directories, testCase.directoriesFirst))
			resourceEntries := resourceEntriesForTest(t, prepared)
			baselineEntries := resourceEntriesForTest(t, baseline)
			resourceProvenance := func(p contract.PreparedImport) []contract.ProvenanceFile {
				stable := make([]contract.ProvenanceFile, 0, len(p.Snapshot.Provenance))
				for _, file := range p.Snapshot.Provenance {
					if !strings.HasPrefix(file.Path, "provenance/transfer-") {
						stable = append(stable, file)
					}
				}
				return stable
			}
			if !reflect.DeepEqual(prepared.Snapshot.Elements, baseline.Snapshot.Elements) || !reflect.DeepEqual(resourceProvenance(prepared), resourceProvenance(baseline)) || !reflect.DeepEqual(resourceEntries, baselineEntries) {
				t.Fatal("directory header order changed resource associations, payloads, or element facts")
			}
			if len(resourceEntries) != 2 || resourceEntries[0].ResourceOrdinal != 0 || resourceEntries[1].ResourceOrdinal != 1 || resourceEntries[1].OriginalURI != "file://TASK-001/release:one/a.bin" || resourceEntries[1].OriginalPath != resourcePath {
				t.Fatalf("nested colon resource mapping changed: %+v", resourceEntries)
			}
		})
	}
}

// Owner, format, and path refusals remain active for nearby colon-directory attacks.
func TestLegacyColonResourceDirectoryHeadersRejectOtherNamespaces(t *testing.T) {
	validFiles := resourceFixture(t)
	owner := resourceTaskDir
	for _, testCase := range []struct {
		name      string
		directory string
		files     map[string][]byte
	}{
		{name: "outside-resources", directory: owner + "/release:one/", files: validFiles},
		{name: "wrong-owner", directory: "EPIC-999_fake/.resources/release:one/", files: validFiles},
		{name: "traversal", directory: owner + "/.resources/release:one/../escape/", files: validFiles},
		{name: "drive-root", directory: "C:/" + owner + "/.resources/release:one/", files: validFiles},
		{name: "uri-root", directory: "file://" + owner + "/.resources/release:one/", files: validFiles},
		{name: "unc-root", directory: "//server/share/" + owner + "/.resources/release:one/", files: validFiles},
		{name: "backslash", directory: owner + "/.resources/release:one\\/", files: validFiles},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, dirs, digest, err := readArchive(context.Background(), bytes.NewReader(packWithDirectoryHeaders(t, testCase.files, []string{testCase.directory}, true)))
			var archiveErr *contract.Error
			if !errors.As(err, &archiveErr) || archiveErr.Code != "invalid_snapshot" || dirs != nil || digest != "" {
				t.Fatalf("unsafe colon directory did not refuse atomically: dirs=%v digest=%q err=%v", dirs, digest, err)
			}
		})
	}
	t.Run("native-v1", func(t *testing.T) {
		nativeFiles := unpack(t, native(t, nil, func(manifest *nativeManifest, files map[string][]byte) {
			files[owner+"/README.md"] = []byte("neutral owner readme")
			files[owner+"/progress.md"] = []byte("neutral owner progress")
			manifest.Files = append(manifest.Files,
				fileDigest{Path: owner + "/README.md", SHA256: digestBytes(files[owner+"/README.md"])},
				fileDigest{Path: owner + "/progress.md", SHA256: digestBytes(files[owner+"/progress.md"])},
			)
			sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
		}))
		_, dirs, digest, err := readArchive(context.Background(), bytes.NewReader(packWithDirectoryHeaders(t, nativeFiles, []string{owner + "/.resources/release:one/"}, true)))
		var archiveErr *contract.Error
		if !errors.As(err, &archiveErr) || archiveErr.Code != "invalid_snapshot" || dirs != nil || digest != "" {
			t.Fatalf("native-v1 accepted a legacy colon directory: dirs=%v digest=%q err=%v", dirs, digest, err)
		}
	})
}

func TestLegacyEncodedFragmentCharacterIsFilenameData(t *testing.T) {
	// Encoded # belongs to a literal archive filename, unlike an actual empty/nonempty URI fragment; preserve the exact source URI.
	f := resourceFixture(t)
	data := f[resourceTaskDir+"/.resources/plain.txt"]
	delete(f, resourceTaskDir+"/.resources/plain.txt")
	f[resourceTaskDir+"/.resources/literal#mark.txt"] = data
	f[resourceTaskDir+"/progress.md"] = bytes.ReplaceAll(f[resourceTaskDir+"/progress.md"], []byte("file://TASK-001/plain.txt"), []byte("file://TASK-001/literal%23mark.txt"))
	p := mustPrepare(t, pack(t, f))
	found := false
	for _, v := range p.Snapshot.Provenance {
		if v.Path == "provenance/legacy-resource-map-v1.json" {
			var m struct {
				Entries []struct {
					URI  string `json:"original_uri"`
					Path string `json:"original_path"`
				} `json:"entries"`
			}
			if json.Unmarshal(v.Data, &m) != nil {
				t.Fatal("mapping decode")
			}
			for _, e := range m.Entries {
				if e.URI == "file://TASK-001/literal%23mark.txt" && e.Path == resourceTaskDir+"/.resources/literal#mark.txt" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("encoded filename character/URI changed")
	}
}
