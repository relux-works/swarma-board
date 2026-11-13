// Package transfer implements pure, bounded native-v1 and sanitized legacy-v2
// transfer. It never opens repositories, extracts files or fetches resources.
package transfer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/relux-works/swarma-board/internal/contract"
)

const nativeManifestPath = ".swarma-board-transfer.json"
const legacyManifestPath = ".task-board-transfer.json"

type fileDigest struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type nativeManifest struct {
	Format                  string       `json:"format"`
	Version                 int          `json:"version"`
	SourceCommit            string       `json:"source_commit"`
	ProjectID               string       `json:"project_id"`
	Revision                uint64       `json:"revision"`
	RedactionManifestSHA256 string       `json:"redaction_manifest_sha256"`
	Files                   []fileDigest `json:"files"`
}
type elementDocument struct {
	SchemaVersion int                `json:"schema_version"`
	Elements      []contract.Element `json:"elements"`
}

// SourceMetadata is an inert provenance payload. It is not an audit actor,
// target revision, grant or authority. The domain may consume these validated
// fields when appending its separate provenance/import-<digest>.json record.
type SourceMetadata struct {
	ArchiveSHA256   string   `json:"archive_sha256"`
	SourceFormat    string   `json:"source_format"`
	SourceVersion   int      `json:"source_version"`
	SourceProjectID string   `json:"source_project_id"`
	SourceRevision  uint64   `json:"source_revision"`
	SourceCommit    string   `json:"source_commit"`
	Exclusions      []string `json:"exclusions"`
}

// MetadataPath identifies the validated source metadata produced by Prepare.
func MetadataPath(digest string) string { return "provenance/transfer-" + digest + ".json" }

// Codec's build commit is explicit, normally an exact build SHA or "dev".
// Neither method accesses storage; instances have no mutable state.
type Codec struct{ sourceCommit string }

func New(sourceCommit string) *Codec { return &Codec{sourceCommit: sourceCommit} }

var _ contract.Transfer = (*Codec)(nil)

func (c *Codec) Prepare(ctx context.Context, r io.Reader, opts contract.ImportOptions) (contract.PreparedImport, error) {
	empty := contract.PreparedImport{}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if r == nil || !projectPattern.MatchString(opts.ProjectID) {
		return empty, invalid("explicit valid target project required")
	}
	if opts.ExpectedSHA256 != "" && !hashPattern.MatchString(opts.ExpectedSHA256) {
		return empty, invalid("invalid expected archive digest")
	}
	files, directories, digest, err := readArchive(ctx, r)
	if err != nil {
		return empty, err
	}
	if opts.ExpectedSHA256 != "" && opts.ExpectedSHA256 != digest {
		return empty, invalid("archive digest mismatch")
	}
	_, native := files[nativeManifestPath]
	_, legacy := files[legacyManifestPath]
	if native == legacy {
		return empty, invalid("exactly one supported manifest required")
	}
	recognizedDirs := map[string]bool{}
	legacyResources := legacy && confirmedLegacyResources(files)
	for file := range files {
		for dir := path.Dir(file); dir != "."; dir = path.Dir(dir) {
			recognizedDirs[dir] = true
		}
	}
	for _, dir := range directories {
		if !recognizedDirs[dir] {
			return empty, invalid("unrecognized archive directory")
		}
		for part := range strings.SplitSeq(dir, "/") {
			if strings.HasPrefix(part, ".") && part != ".activity" && !(part == ".resources" && legacyResources && legacyResourceDirectory(files, dir)) {
				return empty, invalid("execution or unknown archive directory")
			}
		}
		if native && dir != "provenance" {
			return empty, invalid("unsupported native directory")
		}
	}
	var s contract.Snapshot
	var report contract.ImportReport
	var commit string
	if native {
		s, report, commit, err = prepareNative(files, opts.ProjectID)
	} else {
		s, report, err = prepareLegacy(ctx, files, opts.ProjectID)
	}
	if err != nil {
		return empty, err
	}
	if err = ctx.Err(); err != nil {
		return empty, err
	}
	for _, e := range s.Elements {
		if err = validateText(e); err != nil {
			return empty, err
		}
	}
	if err = validateElements(s.Elements); err != nil {
		return empty, err
	}
	report.SHA256 = digest
	report.ElementCount = len(s.Elements)
	metadata := SourceMetadata{ArchiveSHA256: digest, SourceFormat: report.Format, SourceVersion: report.Version, SourceProjectID: s.Project.ID, SourceRevision: uint64(s.Revision), SourceCommit: commit, Exclusions: report.Exclusions}
	data, _ := json.Marshal(metadata)
	data = append(data, '\n')
	if err = addProvenance(&s, MetadataPath(digest), data); err != nil {
		return empty, err
	}
	sort.Slice(s.Elements, func(i, j int) bool { return s.Elements[i].ID < s.Elements[j].ID })
	sort.Slice(s.Provenance, func(i, j int) bool { return s.Provenance[i].Path < s.Provenance[j].Path })
	return contract.PreparedImport{SHA256: digest, Snapshot: s, Report: report}, nil
}
func prepareNative(files map[string][]byte, target string) (contract.Snapshot, contract.ImportReport, string, error) {
	s := contract.Snapshot{Elements: []contract.Element{}, Provenance: []contract.ProvenanceFile{}}
	report := contract.ImportReport{Format: "swarma-board", Version: 1, Exclusions: []string{}, Warnings: []string{}}
	var m nativeManifest
	if e := strictJSON(files[nativeManifestPath], &m); e != nil {
		return s, report, "", e
	}
	if m.Format != "swarma-board" || m.Version != 1 || m.ProjectID != target || m.SourceCommit == "" || !validString(m.SourceCommit) || m.RedactionManifestSHA256 != "" && !hashPattern.MatchString(m.RedactionManifestSHA256) {
		return s, report, "", invalid("unsupported native manifest or target mismatch")
	}
	listed := map[string]bool{}
	previous := ""
	for _, f := range m.Files {
		if f.Path <= previous || f.Path == nativeManifestPath || listed[f.Path] || !hashPattern.MatchString(f.SHA256) {
			return s, report, "", invalid("invalid native file manifest")
		}
		previous = f.Path
		listed[f.Path] = true
		b, ok := files[f.Path]
		if !ok || digestBytes(b) != f.SHA256 {
			return s, report, "", invalid("missing entry or checksum mismatch")
		}
		if f.Path != "elements.json" {
			if !validProvenancePath(f.Path) {
				return s, report, "", invalid("unsupported native entry")
			}
			if e := addProvenance(&s, f.Path, b); e != nil {
				return s, report, "", e
			}
		}
	}
	if !listed["elements.json"] || len(listed)+1 != len(files) {
		return s, report, "", invalid("unlisted or missing native entry")
	}
	var doc elementDocument
	if e := strictJSON(files["elements.json"], &doc); e != nil {
		return s, report, "", e
	}
	if doc.SchemaVersion != 1 {
		return s, report, "", invalid("unsupported element schema")
	}
	for i := 1; i < len(doc.Elements); i++ {
		if doc.Elements[i-1].ID >= doc.Elements[i].ID {
			return s, report, "", invalid("native element IDs must be sorted")
		}
	}
	s.Project = contract.Project{ID: m.ProjectID}
	s.Revision = contract.Revision(m.Revision)
	s.Elements = doc.Elements
	// Preserve the validated original manifest verbatim, including its source
	// redaction digest. New native exports have not performed a redaction.
	if err := addProvenance(&s, "provenance/native-manifest-"+digestBytes(files[nativeManifestPath])+".json", files[nativeManifestPath]); err != nil {
		return s, report, "", err
	}
	return s, report, m.SourceCommit, nil
}
func addProvenance(s *contract.Snapshot, p string, data []byte) error {
	for _, f := range s.Provenance {
		if f.Path == p {
			if !bytes.Equal(f.Data, data) {
				return invalid("provenance path collision")
			}
			return nil
		}
	}
	s.Provenance = append(s.Provenance, contract.ProvenanceFile{Path: p, SHA256: digestBytes(data), Data: data})
	return nil
}
func validString(s string) bool { return utf8.ValidString(s) && !strings.ContainsAny(s, "\x00\r\n") }

// Encode validates the complete detached snapshot before writing anything to
// the caller. A bounded temporary compressed buffer also avoids partial output
// for archive-size/schema errors; final writer failures are returned verbatim.
func (c *Codec) Encode(ctx context.Context, w io.Writer, s contract.Snapshot) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if w == nil || c == nil || c.sourceCommit == "" || !validString(c.sourceCommit) || !projectPattern.MatchString(s.Project.ID) {
		return invalid("invalid exporter metadata")
	}
	var textBytes int64
	for _, e := range s.Elements {
		textBytes += elementTextBytes(e)
		if textBytes > MaxEntry {
			return invalid("elements entry size limit")
		}
		if err := validateText(e); err != nil {
			return err
		}
	}
	if err := validateElements(s.Elements); err != nil {
		return err
	}
	elements := append([]contract.Element{}, s.Elements...)
	sort.Slice(elements, func(i, j int) bool { return elements[i].ID < elements[j].ID })
	data, err := json.Marshal(elementDocument{SchemaVersion: 1, Elements: elements})
	if err != nil {
		return invalid("invalid element encoding")
	}
	if int64(len(data))+1 > MaxEntry {
		return invalid("elements entry size limit")
	}
	files := map[string][]byte{"elements.json": append(data, '\n')}
	for _, p := range s.Provenance {
		if !validProvenancePath(p.Path) || int64(len(p.Data)) > MaxEntry || p.SHA256 != digestBytes(p.Data) {
			return invalid("invalid provenance record")
		}
		if b, ok := files[p.Path]; ok {
			if !bytes.Equal(b, p.Data) {
				return invalid("provenance path collision")
			}
			continue
		}
		files[p.Path] = p.Data
	}
	names := make([]string, 0, len(files))
	for p := range files {
		names = append(names, p)
	}
	sort.Strings(names)
	m := nativeManifest{Format: "swarma-board", Version: 1, SourceCommit: c.sourceCommit, ProjectID: s.Project.ID, Revision: uint64(s.Revision), Files: []fileDigest{}}
	for _, p := range names {
		m.Files = append(m.Files, fileDigest{Path: p, SHA256: digestBytes(files[p])})
	}
	data, err = json.Marshal(m)
	if err != nil {
		return invalid("manifest encoding failed")
	}
	files[nativeManifestPath] = append(data, '\n')
	names = append(names, nativeManifestPath)
	sort.Strings(names)
	if len(names) > MaxEntries {
		return invalid("archive entry limit")
	}
	var expanded int64 = 1024
	for _, p := range names {
		n := int64(len(files[p]))
		if n > MaxEntry {
			return invalid("archive entry size limit")
		}
		expanded += 512 + (n+511)/512*512
	}
	if expanded > MaxExpanded {
		return invalid("expanded archive limit")
	}
	var buf bytes.Buffer
	bw := &boundedWriter{ctx: ctx, w: &buf, max: MaxCompressed}
	gz := gzip.NewWriter(bw)
	gz.Header.ModTime = time.Time{}
	gz.Header.OS = 255
	tw := tar.NewWriter(gz)
	for _, p := range names {
		if err = ctx.Err(); err != nil {
			break
		}
		b := files[p]
		err = tw.WriteHeader(&tar.Header{Name: p, Size: int64(len(b)), Mode: 0600, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR})
		if err != nil {
			break
		}
		if _, err = tw.Write(b); err != nil {
			break
		}
	}
	if err == nil {
		err = tw.Close()
	}
	if closeErr := gz.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	_, err = io.Copy(w, bytes.NewReader(buf.Bytes()))
	return err
}

type boundedWriter struct {
	ctx    context.Context
	w      io.Writer
	n, max int64
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if e := w.ctx.Err(); e != nil {
		return 0, e
	}
	if int64(len(p)) > w.max-w.n {
		return 0, invalid("compressed archive limit")
	}
	n, e := w.w.Write(p)
	w.n += int64(n)
	return n, e
}
