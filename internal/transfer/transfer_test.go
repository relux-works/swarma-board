package transfer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/relux-works/swarma-board/internal/contract"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, e := os.ReadFile("../testadapter/testdata/" + name + ".tar.gz")
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func mustPrepare(t *testing.T, b []byte) contract.PreparedImport {
	t.Helper()
	p, e := New("dev").Prepare(context.Background(), bytes.NewReader(b), contract.ImportOptions{ProjectID: "synthetic-board"})
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func unpack(t *testing.T, b []byte) map[string][]byte {
	t.Helper()
	m, _, _, e := readArchive(context.Background(), bytes.NewReader(b))
	if e != nil {
		t.Fatal(e)
	}
	return m
}
func pack(t *testing.T, m map[string][]byte) []byte {
	t.Helper()
	var b bytes.Buffer
	g := gzip.NewWriter(&b)
	w := tar.NewWriter(g)
	names := []string{}
	for p := range m {
		names = append(names, p)
	}
	sort.Strings(names)
	for _, p := range names {
		if e := w.WriteHeader(&tar.Header{Name: p, Size: int64(len(m[p])), Mode: 0600, Typeflag: tar.TypeReg}); e != nil {
			t.Fatal(e)
		}
		if _, e := w.Write(m[p]); e != nil {
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
func native(t *testing.T, doc []byte, edit func(*nativeManifest, map[string][]byte)) []byte {
	t.Helper()
	m := unpack(t, fixture(t, "native-v1"))
	if doc != nil {
		m["elements.json"] = doc
	}
	var manifest nativeManifest
	if e := json.Unmarshal(m[nativeManifestPath], &manifest); e != nil {
		t.Fatal(e)
	}
	manifest.Files = []fileDigest{}
	for p, b := range m {
		if p != nativeManifestPath {
			manifest.Files = append(manifest.Files, fileDigest{p, digestBytes(b)})
		}
	}
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
	if edit != nil {
		edit(&manifest, m)
	}
	m[nativeManifestPath], _ = json.Marshal(manifest)
	return pack(t, m)
}
func refused(t *testing.T, b []byte) {
	t.Helper()
	p, e := New("dev").Prepare(context.Background(), bytes.NewReader(b), contract.ImportOptions{ProjectID: "synthetic-board"})
	var ce *contract.Error
	if !errors.As(e, &ce) || ce.Code != "invalid_snapshot" {
		t.Fatalf("want invalid_snapshot, got %v", e)
	}
	if !reflect.DeepEqual(p, contract.PreparedImport{}) {
		t.Fatal("refusal leaked partially prepared snapshot")
	}
	if len(e.Error()) > 300 {
		t.Fatal("unbounded error")
	}
}

func TestA11NativeRoundtripDeterministicAndSourceMetadata(t *testing.T) {
	archive := fixture(t, "native-v1")
	p := mustPrepare(t, archive)
	digest := fmt.Sprintf("%x", sha256.Sum256(archive))
	if p.SHA256 != digest || p.Report.SHA256 != digest || p.Report.Format != "swarma-board" || p.Report.Version != 1 || p.Report.ElementCount != 7 {
		t.Fatalf("bad report %+v", p.Report)
	}
	if p.Snapshot.Project.ID != "synthetic-board" || p.Snapshot.Revision != 0 {
		t.Fatal("source identity not retained")
	}
	if p.Snapshot.Elements[4].Estimate != nil || p.Snapshot.Elements[5].Estimate == nil || p.Snapshot.Elements[5].Estimate.Kind != "notEstimated" {
		t.Fatal("null and explicit estimate collapsed")
	}
	var meta SourceMetadata
	for _, f := range p.Snapshot.Provenance {
		if f.Path == MetadataPath(digest) {
			if e := json.Unmarshal(f.Data, &meta); e != nil {
				t.Fatal(e)
			}
		}
	}
	if meta.ArchiveSHA256 != digest || meta.SourceCommit != "dev" || meta.SourceVersion != 1 {
		t.Fatalf("bad provenance %+v", meta)
	}
	p.Snapshot.Revision = 42
	var a, b bytes.Buffer
	c := New(strings.Repeat("a", 40))
	if e := c.Encode(context.Background(), &a, p.Snapshot); e != nil {
		t.Fatal(e)
	}
	// Input element/provenance ordering has no effect and Encode is nonmutating.
	for i, j := 0, len(p.Snapshot.Elements)-1; i < j; i, j = i+1, j-1 {
		p.Snapshot.Elements[i], p.Snapshot.Elements[j] = p.Snapshot.Elements[j], p.Snapshot.Elements[i]
	}
	if e := c.Encode(context.Background(), &b, p.Snapshot); e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("nondeterministic Encode")
	}
	g, e := gzip.NewReader(bytes.NewReader(a.Bytes()))
	if e != nil {
		t.Fatal(e)
	}
	if !g.ModTime.IsZero() || g.Name != "" || g.Comment != "" {
		t.Fatal("nondeterministic gzip metadata")
	}
	g.Close()
	q := mustPrepare(t, a.Bytes())
	sort.Slice(p.Snapshot.Elements, func(i, j int) bool { return p.Snapshot.Elements[i].ID < p.Snapshot.Elements[j].ID })
	if !reflect.DeepEqual(p.Snapshot.Elements, q.Snapshot.Elements) {
		t.Fatal("product facts changed during roundtrip")
	}
	if q.Snapshot.Revision != 42 {
		t.Fatal("source revision lost")
	}
	for _, want := range p.Snapshot.Provenance {
		found := false
		for _, got := range q.Snapshot.Provenance {
			if reflect.DeepEqual(want, got) {
				found = true
			}
		}
		if !found {
			t.Fatal("existing provenance changed")
		}
	}
	opts := contract.ImportOptions{ProjectID: "synthetic-board", ExpectedSHA256: digest}
	if _, e := c.Prepare(context.Background(), bytes.NewReader(archive), opts); e != nil {
		t.Fatal(e)
	}
	opts.ExpectedSHA256 = strings.Repeat("0", 64)
	if _, e := c.Prepare(context.Background(), bytes.NewReader(archive), opts); e == nil {
		t.Fatal("digest mismatch accepted")
	}
	opts = contract.ImportOptions{ProjectID: "other"}
	if _, e := c.Prepare(context.Background(), bytes.NewReader(archive), opts); e == nil {
		t.Fatal("implicit remap accepted")
	}
}

func TestA11LegacyFieldsExclusionsReciprocalLinksAndInertActivity(t *testing.T) {
	files := unpack(t, fixture(t, "legacy-v2"))
	dir := "EPIC-001_synthetic-epic/STORY-001_first-story/TASK-001_a/"
	files[dir+"progress.md"] = append(files[dir+"progress.md"], []byte("## Estimate\nestimated(fibonacci(4))\n\n## Assigned To\nsynthetic-person\n\n## Created\n2026-11-05T01:02:03+02:00\n\n## Last Update\n2026-11-05T00:00:00Z\n\n## Checklist\n- [x] first\\nsecond\n- [ ] pending\n\n## Notes\n  Keep leading spaces.\n\\## literal heading\n\n## Precondition Resources\n- [design](urn:synthetic:design) — reference\\ntext\n\n## Outcome Resources\n- [result](https://example.invalid/result)\n\n## integrationCheckpointed\ntrue\n\n## run_id\nSYNTHETIC-RUN\n")...)
	activity := []byte("{\"actor\":\"synthetic-principal-claim-only\",\"signature\":\"inert-do-not-regenerate\",\"event\":\"synthetic\"}\n")
	ap := ".activity/TASK-001/events.ndjson"
	files[ap] = activity
	manifest := legacyManifest{Format: "task-board", Version: 2, Activity: []legacyActivity{{"TASK-001", ap, digestBytes(activity)}}}
	files[legacyManifestPath], _ = json.Marshal(manifest)
	archive := pack(t, files)
	p := mustPrepare(t, archive)
	if p.Report.Format != "task-board" || p.Report.Version != 2 || p.Report.ElementCount != 7 || !reflect.DeepEqual(p.Report.Exclusions, []string{"TASK-001:integrationCheckpointed", "TASK-001:run_id"}) || len(p.Report.Warnings) != 1 {
		t.Fatalf("bad report %+v", p.Report)
	}
	var task contract.Element
	for _, e := range p.Snapshot.Elements {
		if e.ID == "TASK-001" {
			task = e
		}
	}
	if task.Title != "Synthetic A — text preservation" || task.Description != "First line.\nSecond line." || task.Scope != "Synthetic scope" || task.AcceptanceCriteria != "Preserve text and ordered checklist." || task.AssignedTo != "synthetic-person" || task.ParentID != "STORY-001" || task.Estimate.Scale.Number != 4 || task.Checklist[0].Text != "first\nsecond" || !task.Checklist[0].Checked || task.Notes != "  Keep leading spaces.\n## literal heading" || len(task.ResourceRefs) != 2 || task.ResourceRefs[0].Description != "reference\ntext" {
		t.Fatalf("lost fields %+v", task)
	}
	if task.CreatedAt.Format(time.RFC3339) != "2026-11-04T23:02:03Z" {
		t.Fatal("timestamp fact changed")
	}
	found := false
	for _, f := range p.Snapshot.Provenance {
		if bytes.Equal(f.Data, activity) && f.SHA256 == digestBytes(activity) {
			found = true
		}
	}
	if !found {
		t.Fatal("opaque activity was not retained exactly")
	}
	if !reflect.DeepEqual(p.Snapshot.Elements[2].ManualBlockedBy, []string{"STORY-001"}) {
		t.Fatal("manual container link lost")
	}
	var b bytes.Buffer
	if e := New("dev").Encode(context.Background(), &b, p.Snapshot); e != nil {
		t.Fatal(e)
	}
	q := mustPrepare(t, b.Bytes())
	if !reflect.DeepEqual(p.Snapshot.Elements, q.Snapshot.Elements) {
		t.Fatal("legacy->native facts changed")
	}
	files[ap] = append(activity, ' ')
	refused(t, pack(t, files))
}

func TestA12NativeStrictSchemaAndFacts(t *testing.T) {
	baseline := unpack(t, fixture(t, "native-v1"))["elements.json"]
	tests := map[string][]byte{
		"unknown field":      bytes.Replace(baseline, []byte(`"id": "TASK-001"`), []byte(`"secret": "safe-synthetic", "id": "TASK-001"`), 1),
		"duplicate field":    bytes.Replace(baseline, []byte(`"id": "TASK-001"`), []byte(`"id": "TASK-001", "id": "TASK-001"`), 1),
		"case alias":         bytes.Replace(baseline, []byte(`"title": "B"`), []byte(`"TITLE": "B"`), 1),
		"missing required":   bytes.Replace(baseline, []byte(`"title": "B",`), nil, 1),
		"null string":        bytes.Replace(baseline, []byte(`"title": "B"`), []byte(`"title": null`), 1),
		"null array":         bytes.Replace(baseline, []byte(`"blocked_by": []`), []byte(`"blocked_by": null`), 1),
		"unknown nested":     bytes.Replace(baseline, []byte(`"kind": "notEstimated"`), []byte(`"kind": "notEstimated", "nested":true`), 1),
		"invalid UTF8":       bytes.Replace(baseline, []byte(`"title": "B"`), []byte{'"', 't', 'i', 't', 'l', 'e', '"', ':', '"', 0xff, '"'}, 1),
		"unpaired surrogate": bytes.Replace(baseline, []byte(`"title": "B"`), []byte(`"title": "\ud800"`), 1),
		"trailing JSON":      append(append([]byte{}, baseline...), []byte(`{}`)...),
		"negative estimate":  bytes.Replace(baseline, []byte(`"number": 3`), []byte(`"number": -1`), 1),
		"partial estimate":   bytes.Replace(baseline, []byte(`"kind": "notEstimated"`), []byte(`"kind": "partiallyEstimated"`), 1),
		"bad status":         bytes.Replace(baseline, []byte(`"status": "backlog"`), []byte(`"status": "spawned"`), 1),
		"unsafe URI":         bytes.Replace(baseline, []byte(`urn:synthetic:design:a`), []byte(`file:///tmp/synthetic`), 1),
		"URL credentials":    bytes.Replace(baseline, []byte(`https://example.invalid/synthetic/a`), []byte(`https://synthetic:synthetic@example.invalid/a`), 1),
		"URL query":          bytes.Replace(baseline, []byte(`https://example.invalid/synthetic/a`), []byte(`https://example.invalid/a?synthetic=x`), 1),
		"URL fragment":       bytes.Replace(baseline, []byte(`https://example.invalid/synthetic/a`), []byte(`https://example.invalid/a#synthetic`), 1),
		"bad version":        bytes.Replace(baseline, []byte(`"schema_version": 1`), []byte(`"schema_version": 2`), 1),
		"nonUTC native":      bytes.Replace(baseline, []byte(`2026-11-05T00:00:00Z`), []byte(`2026-11-05T01:00:00+01:00`), 1),
	}
	for name, doc := range tests {
		t.Run(name, func(t *testing.T) { refused(t, native(t, doc, nil)) })
	}
	for name, edit := range map[string]func(*nativeManifest, map[string][]byte){
		"version":          func(m *nativeManifest, _ map[string][]byte) { m.Version = 2 },
		"bad hash":         func(m *nativeManifest, _ map[string][]byte) { m.Files[0].SHA256 = strings.Repeat("0", 64) },
		"duplicate listed": func(m *nativeManifest, _ map[string][]byte) { m.Files = append(m.Files, m.Files[0]) },
		"unlisted":         func(_ *nativeManifest, f map[string][]byte) { f["provenance/unlisted"] = []byte("synthetic") },
		"self listed": func(m *nativeManifest, _ map[string][]byte) {
			m.Files = append(m.Files, fileDigest{nativeManifestPath, strings.Repeat("a", 64)})
		},
		"missing file": func(_ *nativeManifest, f map[string][]byte) { delete(f, "elements.json") },
	} {
		t.Run(name, func(t *testing.T) { refused(t, native(t, nil, edit)) })
	}
}

func TestA12GraphValidation(t *testing.T) {
	baseline := mustPrepare(t, fixture(t, "native-v1")).Snapshot.Elements
	cases := map[string]func([]contract.Element){
		"dangling":             func(e []contract.Element) { e[5].BlockedBy = []string{"TASK-999"} },
		"cycle":                func(e []contract.Element) { e[3].BlockedBy = []string{"TASK-004"} },
		"self":                 func(e []contract.Element) { e[3].BlockedBy = []string{"TASK-001"} },
		"missing parent":       func(e []contract.Element) { e[3].ParentID = "STORY-999" },
		"bad parent":           func(e []contract.Element) { e[3].ParentID = "EPIC-001" },
		"hierarchy cycle":      func(e []contract.Element) { e[0].ParentID = "TASK-001" },
		"ancestor manual":      func(e []contract.Element) { e[1].ManualBlockedBy = []string{"EPIC-001"} },
		"mixed dependency":     func(e []contract.Element) { e[3].BlockedBy = []string{"STORY-001"} },
		"duplicate ID":         func(e []contract.Element) { e[4].ID = e[3].ID },
		"case collision":       func(e []contract.Element) { e[4].ID = strings.ToLower(e[3].ID) },
		"duplicate edge":       func(e []contract.Element) { e[5].BlockedBy = []string{"TASK-001", "task-001"} },
		"terminal ancestor":    func(e []contract.Element) { e[0].Status = "done" },
		"done active blocker":  func(e []contract.Element) { e[5].Status = "done" },
		"container estimate":   func(e []contract.Element) { e[0].Estimate = &contract.Estimate{Kind: "notEstimated"} },
		"container task links": func(e []contract.Element) { e[1].BlockedBy = []string{"STORY-002"} },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			raw, _ := json.Marshal(baseline)
			var e []contract.Element
			json.Unmarshal(raw, &e)
			edit(e)
			doc, _ := json.Marshal(elementDocument{1, e})
			refused(t, native(t, doc, nil))
		})
	}
	// Advisory cycles do not turn a valid leaf DAG into a refusal.
	raw, _ := json.Marshal(baseline)
	var e []contract.Element
	json.Unmarshal(raw, &e)
	e[1].ManualBlockedBy = []string{"STORY-002"}
	doc, _ := json.Marshal(elementDocument{1, e})
	mustPrepare(t, native(t, doc, nil))
}

func TestA12LegacyRefusals(t *testing.T) {
	for name, edit := range map[string]func(map[string][]byte){
		"unknown artifact":      func(f map[string][]byte) { f["unknown.txt"] = []byte("meaningful synthetic") },
		"runtime":               func(f map[string][]byte) { f[".runtime/synthetic.json"] = []byte("{}") },
		"binary resource":       func(f map[string][]byte) { f["EPIC-001_synthetic-epic/.resources/synthetic.bin"] = []byte{0, 1} },
		"credential":            func(f map[string][]byte) { f["credentials.json"] = []byte(`{"synthetic":true}`) },
		"unmanifested activity": func(f map[string][]byte) { f[".activity/TASK-001/events.ndjson"] = []byte("{}\n") },
		"missing progress":      func(f map[string][]byte) { delete(f, "EPIC-001_synthetic-epic/progress.md") },
		"unknown readme section": func(f map[string][]byte) {
			f["EPIC-001_synthetic-epic/README.md"] = append(f["EPIC-001_synthetic-epic/README.md"], []byte("\n## Meaningful custom data\nMust not disappear\n")...)
		},
		"reciprocal mismatch": func(f map[string][]byte) {
			p := "EPIC-001_synthetic-epic/STORY-001_first-story/TASK-001_a/progress.md"
			f[p] = bytes.Replace(f[p], []byte("- TASK-003"), []byte("- TASK-004"), 1)
		},
		"reserved authority notes": func(f map[string][]byte) {
			p := "EPIC-001_synthetic-epic/progress.md"
			f[p] = append(f[p], []byte("## Notes\n<!-- integrationCheckpointed=true -->\n")...)
		},
		"unsupported resources": func(f map[string][]byte) {
			p := "EPIC-001_synthetic-epic/progress.md"
			f[p] = append(f[p], []byte("## Outcome Resources\n- [required](./synthetic-resource.md)\n")...)
		},
	} {
		t.Run(name, func(t *testing.T) { f := unpack(t, fixture(t, "legacy-v2")); edit(f); refused(t, pack(t, f)) })
	}
}

type entry struct {
	name string
	typ  byte
	size int64
	data []byte
}

func rawArchive(t *testing.T, entries []entry, tail []byte) []byte {
	t.Helper()
	var raw bytes.Buffer
	for _, e := range entries {
		var h bytes.Buffer
		w := tar.NewWriter(&h)
		if err := w.WriteHeader(&tar.Header{Name: e.name, Typeflag: e.typ, Size: e.size, Mode: 0600, Linkname: func() string {
			if e.typ == tar.TypeSymlink || e.typ == tar.TypeLink {
				return "synthetic-target"
			}
			return ""
		}()}); err != nil {
			t.Fatal(err)
		}
		raw.Write(h.Bytes())
		raw.Write(e.data)
		if e.size == int64(len(e.data)) {
			raw.Write(make([]byte, (512-len(e.data)%512)%512))
		}
	}
	raw.Write(make([]byte, 1024))
	raw.Write(tail)
	var b bytes.Buffer
	g := gzip.NewWriter(&b)
	g.Write(raw.Bytes())
	g.Close()
	return b.Bytes()
}
func TestA12ArchivePathsTypesFramingAndLimits(t *testing.T) {
	for _, name := range []string{"../evil", "/absolute", "a/../b", "a//b", "./README.md", "a\\b", "C:/synthetic", "a/./b"} {
		t.Run(name, func(t *testing.T) { refused(t, rawArchive(t, []entry{{name, tar.TypeReg, 0, nil}}, nil)) })
	}
	for _, typ := range []byte{tar.TypeSymlink, tar.TypeLink, tar.TypeChar, tar.TypeBlock, tar.TypeFifo} {
		t.Run(fmt.Sprint(typ), func(t *testing.T) { refused(t, rawArchive(t, []entry{{"special", typ, 0, nil}}, nil)) })
	}
	refused(t, rawArchive(t, []entry{{"a", tar.TypeReg, 0, nil}, {"a", tar.TypeReg, 0, nil}}, nil))
	refused(t, rawArchive(t, []entry{{"a/", tar.TypeDir, 0, nil}, {"a", tar.TypeReg, 0, nil}}, nil))
	refused(t, rawArchive(t, []entry{{"a", tar.TypeReg, 0, nil}, {"a/b", tar.TypeReg, 0, nil}}, nil))
	refused(t, rawArchive(t, []entry{{"oversized", tar.TypeReg, MaxEntry + 1, nil}}, nil))
	refused(t, rawArchive(t, nil, []byte("trailing uncompressed data")))
	valid := fixture(t, "native-v1")
	refused(t, append(append([]byte{}, valid...), valid...))
	refused(t, append(append([]byte{}, valid...), 1))
	refused(t, valid[:len(valid)-3])
	corrupt := append([]byte{}, valid...)
	corrupt[len(corrupt)-8] ^= 1
	refused(t, corrupt)
	es := make([]entry, MaxEntries+1)
	for i := range es {
		es[i] = entry{fmt.Sprintf("directory-%05d/", i), tar.TypeDir, 0, nil}
	}
	refused(t, rawArchive(t, es, nil))
	// Reader budget includes framing bytes, not only file data, and reads at
	// most one byte beyond the bound. Test the same gate with a small budget.
	br := &budgetReader{ctx: context.Background(), r: strings.NewReader(strings.Repeat("x", 100)), max: 10}
	if _, e := io.ReadAll(br); e == nil || br.n != 11 {
		t.Fatalf("budget failed: %v, %d", e, br.n)
	}
	// Valid canonical directory entries are accepted and included in entry limits.
	f := unpack(t, valid)
	var raw bytes.Buffer
	g := gzip.NewWriter(&raw)
	w := tar.NewWriter(g)
	w.WriteHeader(&tar.Header{Name: "provenance/", Typeflag: tar.TypeDir, Mode: 0700})
	for p, b := range f {
		w.WriteHeader(&tar.Header{Name: p, Size: int64(len(b)), Mode: 0600})
		w.Write(b)
	}
	w.Close()
	g.Close()
	mustPrepare(t, raw.Bytes())
}
func TestCancellationWriterFailureAndCollision(t *testing.T) {
	c := New("dev")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := c.Prepare(ctx, bytes.NewReader(fixture(t, "native-v1")), contract.ImportOptions{ProjectID: "synthetic-board"}); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	s := mustPrepare(t, fixture(t, "native-v1")).Snapshot
	if e := c.Encode(ctx, io.Discard, s); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	sentinel := errors.New("synthetic writer failure")
	if e := c.Encode(context.Background(), errorWriter{sentinel}, s); !errors.Is(e, sentinel) {
		t.Fatal(e)
	}
	if e := addProvenance(&s, "provenance/synthetic.txt", []byte("different bytes")); e == nil {
		t.Fatal("provenance collision accepted")
	}
	var out bytes.Buffer
	s.Elements[0].Name = string([]byte{0xff})
	if e := c.Encode(context.Background(), &out, s); e == nil || out.Len() != 0 {
		t.Fatal("invalid export wrote output")
	}
}

type errorWriter struct{ err error }

func (w errorWriter) Write([]byte) (int, error) { return 0, w.err }

func TestRealExpandedBudgetAndElementCount(t *testing.T) {
	// A small compressed stream expands beyond 256 MiB. Every entry remains
	// within 8 MiB; the aggregate gate refuses before allocating entry 32.
	var b bytes.Buffer
	g := gzip.NewWriter(&b)
	w := tar.NewWriter(g)
	zero := make([]byte, 8<<20)
	for i := 0; i < 33; i++ {
		if e := w.WriteHeader(&tar.Header{Name: fmt.Sprintf("synthetic-%02d", i), Size: int64(len(zero)), Mode: 0600}); e != nil {
			t.Fatal(e)
		}
		if _, e := w.Write(zero); e != nil {
			t.Fatal(e)
		}
	}
	w.Close()
	g.Close()
	_, _, _, e := readArchive(context.Background(), bytes.NewReader(b.Bytes()))
	if e == nil || e.Error() != "expanded archive limit" {
		t.Fatalf("aggregate expanded gate: %v", e)
	}
	// Exercise the real compressed-byte gate without materializing 64 MiB.
	limited := &budgetReader{ctx: context.Background(), r: io.LimitReader(zeroReader{}, MaxCompressed+1), max: MaxCompressed}
	if _, e = io.Copy(io.Discard, limited); e == nil || limited.n != MaxCompressed+1 {
		t.Fatalf("compressed gate: %v %d", e, limited.n)
	}
	elements := make([]contract.Element, MaxElements+1)
	if e = validateElements(elements); e == nil || e.Error() != "element count limit" {
		t.Fatal("element count gate", e)
	}
}

type zeroReader struct{}

func (zeroReader) Read(b []byte) (int, error) { clear(b); return len(b), nil }

func FuzzPrepare(f *testing.F) {
	for _, name := range []string{"native-v1", "legacy-v2"} {
		b, e := os.ReadFile("../testadapter/testdata/" + name + ".tar.gz")
		if e != nil {
			f.Fatal(e)
		}
		f.Add(b)
	}
	f.Add([]byte("invalid"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		_, _ = New("dev").Prepare(context.Background(), bytes.NewReader(data), contract.ImportOptions{ProjectID: "synthetic-board"})
	})
}

func TestActivitySyntaxAndReservedArtifacts(t *testing.T) {
	for _, data := range [][]byte{[]byte("{} trailing\n"), []byte("{\"a\":1,\"a\":2}\n"), []byte("[]\n"), {0xff}, []byte("{\"a\":\"\\ud800\"}\n")} {
		if e := validateActivity(data); e == nil {
			t.Fatalf("invalid activity accepted %q", data)
		}
	}
	// Empty forbidden directories also refuse; they cannot disappear through
	// archive/tar's directory handling.
	var b bytes.Buffer
	g := gzip.NewWriter(&b)
	w := tar.NewWriter(g)
	w.WriteHeader(&tar.Header{Name: ".runtime/", Typeflag: tar.TypeDir, Mode: 0700})
	for p, d := range unpack(t, fixture(t, "native-v1")) {
		w.WriteHeader(&tar.Header{Name: p, Size: int64(len(d)), Mode: 0600})
		w.Write(d)
	}
	w.Close()
	g.Close()
	refused(t, b.Bytes())
}

func TestEncodeBudgetsAndProvenanceRefusalsBeforeOutput(t *testing.T) {
	baseline := mustPrepare(t, fixture(t, "native-v1")).Snapshot
	c := New("dev")
	for name, edit := range map[string]func(*contract.Snapshot){
		"large product text":       func(s *contract.Snapshot) { s.Elements[0].Description = strings.Repeat("x", int(MaxEntry)+1) },
		"provenance hash mismatch": func(s *contract.Snapshot) { s.Provenance[0].SHA256 = strings.Repeat("0", 64) },
		"provenance collision": func(s *contract.Snapshot) {
			p := s.Provenance[0]
			p.Data = []byte("different synthetic bytes")
			p.SHA256 = digestBytes(p.Data)
			s.Provenance = append(s.Provenance, p)
		},
		"count limit": func(s *contract.Snapshot) {
			s.Provenance = nil
			for i := 0; i < MaxEntries; i++ {
				s.Provenance = append(s.Provenance, contract.ProvenanceFile{Path: fmt.Sprintf("provenance/synthetic-%05d", i), SHA256: digestBytes(nil)})
			}
		},
		"expanded limit": func(s *contract.Snapshot) {
			s.Provenance = nil
			data := make([]byte, MaxEntry)
			digest := digestBytes(data)
			for i := 0; i < 33; i++ {
				s.Provenance = append(s.Provenance, contract.ProvenanceFile{Path: fmt.Sprintf("provenance/synthetic-%02d", i), SHA256: digest, Data: data})
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := baseline
			s.Elements = append([]contract.Element{}, baseline.Elements...)
			s.Provenance = append([]contract.ProvenanceFile{}, baseline.Provenance...)
			edit(&s)
			var out bytes.Buffer
			if e := c.Encode(context.Background(), &out, s); e == nil || out.Len() != 0 {
				t.Fatalf("invalid export wrote %d bytes, error %v", out.Len(), e)
			}
		})
	}
	var out bytes.Buffer
	bw := &boundedWriter{ctx: context.Background(), w: &out, max: 10}
	if _, e := bw.Write(make([]byte, 11)); e == nil || out.Len() != 0 {
		t.Fatal("compressed writer bound failed")
	}
}

func TestStrictJSONCountAndBoundedError(t *testing.T) {
	doc := []byte(`{"schema_version":1,"elements":[` + strings.Repeat(`{},`, MaxElements) + `{}]}`)
	var d elementDocument
	if e := strictJSON(doc, &d); e == nil || d.Elements != nil {
		t.Fatal("element count was not refused before typed allocation", e)
	}
	raw := []byte(`{"created_at":"` + strings.Repeat("synthetic-not-a-time", 20000) + `"}`)
	// Supply a tiny schema just to verify malformed timestamp payloads never
	// appear in validation errors.
	var stamp struct {
		CreatedAt *time.Time `json:"created_at"`
	}
	if e := strictJSON(raw, &stamp); e == nil || len(e.Error()) > 100 {
		t.Fatal("unbounded timestamp error")
	}
	// Native redaction digest remains available in exact inert manifest bytes.
	archive := native(t, nil, func(m *nativeManifest, _ map[string][]byte) { m.RedactionManifestSHA256 = strings.Repeat("b", 64) })
	p := mustPrepare(t, archive)
	found := false
	for _, f := range p.Snapshot.Provenance {
		if strings.HasPrefix(f.Path, "provenance/native-manifest-") && bytes.Contains(f.Data, []byte(strings.Repeat("b", 64))) {
			found = true
		}
	}
	if !found {
		t.Fatal("source redaction digest lost")
	}
}
