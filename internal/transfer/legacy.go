package transfer

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/relux-works/swarma-board/internal/contract"
	"github.com/relux-works/swarma-board/internal/legacyparse"
)

type legacyActivity struct {
	ElementID string `json:"element_id"`
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
}
type legacyManifest struct {
	Format   string           `json:"format"`
	Version  int              `json:"version"`
	Activity []legacyActivity `json:"activity"`
}

func prepareLegacy(ctx context.Context, files map[string][]byte, target string) (contract.Snapshot, contract.ImportReport, error) {
	s := contract.Snapshot{Project: contract.Project{ID: target}, Elements: []contract.Element{}, Provenance: []contract.ProvenanceFile{}}
	report := contract.ImportReport{Format: "task-board", Version: 2, Exclusions: []string{}, Warnings: []string{}}
	var m legacyManifest
	if err := strictJSON(files[legacyManifestPath], &m); err != nil {
		return s, report, err
	}
	if m.Format != "task-board" || m.Version != 2 {
		return s, report, invalid("unsupported legacy version")
	}
	dirs := []string{}
	for p := range files {
		if path.Base(p) == "README.md" {
			dirs = append(dirs, path.Dir(p))
		}
	}
	if len(dirs) > MaxElements {
		return s, report, invalid("element count limit")
	}
	sort.Strings(dirs)
	byPath := map[string]int{}
	byID := map[string]int{}
	blocks := map[string][]string{}
	consumed := map[string]bool{legacyManifestPath: true}
	for _, dir := range dirs {
		if err := ctx.Err(); err != nil {
			return s, report, err
		}
		parts := strings.SplitN(path.Base(dir), "_", 2)
		if len(parts) != 2 || parts[1] == "" {
			return s, report, invalid("invalid legacy element directory")
		}
		id := parts[0]
		typ := strings.ToLower(strings.SplitN(id, "-", 2)[0])
		if !validID(id, typ) {
			return s, report, invalid("invalid legacy ID")
		}
		key := strings.ToLower(id)
		if _, ok := byID[key]; ok {
			return s, report, invalid("duplicate legacy ID")
		}
		rp := path.Join(dir, "README.md")
		pp := path.Join(dir, "progress.md")
		progress, ok := files[pp]
		if !ok {
			return s, report, invalid("legacy progress missing")
		}
		parsed, err := legacyparse.Parse(files[rp], progress)
		if err != nil {
			return s, report, invalid("legacy Markdown refused: " + err.Error())
		}
		e := parsed.Element
		e.ID = id
		e.Type = typ
		e.Name = parts[1]
		if !leaf(e) {
			e.ManualBlockedBy = e.BlockedBy
			e.BlockedBy = []string{}
		}
		byPath[dir] = len(s.Elements)
		byID[key] = len(s.Elements)
		blocks[key] = parsed.Blocks
		s.Elements = append(s.Elements, e)
		consumed[rp] = true
		consumed[pp] = true
		for _, key := range parsed.Exclusions {
			report.Exclusions = append(report.Exclusions, id+":"+key)
		}
	}
	for dir, i := range byPath {
		parent := path.Dir(dir)
		if parent != "." {
			pi, ok := byPath[parent]
			if !ok {
				return s, report, invalid("legacy hierarchy parent missing")
			}
			s.Elements[i].ParentID = s.Elements[pi].ID
		}
	}
	// Both supplied representations must agree; never fabricate reciprocal facts.
	for _, e := range s.Elements {
		deps := e.BlockedBy
		if !leaf(e) {
			deps = e.ManualBlockedBy
		}
		for _, id := range deps {
			bi, ok := byID[strings.ToLower(id)]
			if !ok || !containsID(blocks[strings.ToLower(s.Elements[bi].ID)], e.ID) {
				return s, report, invalid("legacy reciprocal dependency mismatch")
			}
		}
		seen := map[string]bool{}
		for _, id := range blocks[strings.ToLower(e.ID)] {
			key := strings.ToLower(id)
			i, ok := byID[key]
			if !ok || seen[key] {
				return s, report, invalid("legacy dangling/duplicate blocks")
			}
			seen[key] = true
			other := s.Elements[i]
			otherDeps := other.BlockedBy
			if !leaf(other) {
				otherDeps = other.ManualBlockedBy
			}
			if !containsID(otherDeps, e.ID) {
				return s, report, invalid("legacy reciprocal dependency mismatch")
			}
		}
	}
	activityIDs := map[string]bool{}
	activityPaths := map[string]bool{}
	for _, a := range m.Activity {
		key := strings.ToLower(a.ElementID)
		i, ok := byID[key]
		if !ok || s.Elements[i].ID != a.ElementID || activityIDs[key] || activityPaths[a.Path] || a.Path != ".activity/"+a.ElementID+"/events.ndjson" || !hashPattern.MatchString(a.SHA256) {
			return s, report, invalid("invalid legacy activity manifest")
		}
		data, ok := files[a.Path]
		if !ok || digestBytes(data) != a.SHA256 {
			return s, report, invalid("legacy activity checksum mismatch")
		}
		if err := validateActivity(data); err != nil {
			return s, report, err
		}
		// Opaque sanitized bytes are not re-parsed as live actors or regenerated.
		if err := addProvenance(&s, "provenance/activity-"+a.ElementID+"-"+a.SHA256+".ndjson", data); err != nil {
			return s, report, err
		}
		activityIDs[key] = true
		activityPaths[a.Path] = true
		consumed[a.Path] = true
	}
	mappedResources, err := mapLegacyResources(&s, files, byPath, consumed)
	if err != nil {
		return s, report, err
	}
	if mappedResources {
		report.Warnings = append(report.Warnings, "Legacy file resources mapped to inert content URNs; reversible originals and payloads retained in provenance (swarma-board/legacy-resource-mapping/1).")
	}
	for p := range files {
		if !consumed[p] {
			return s, report, invalid("unrecognized legacy artifact; upstream sanitization required")
		}
	}
	if err := addProvenance(&s, "provenance/legacy-manifest-"+digestBytes(files[legacyManifestPath])+".json", files[legacyManifestPath]); err != nil {
		return s, report, err
	}
	if len(m.Activity) > 0 {
		report.Warnings = append(report.Warnings, "Sanitized activity retained as inert provenance, not authenticated authorship or live audit.")
	}
	sort.Strings(report.Exclusions)
	return s, report, nil
}
func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if strings.EqualFold(id, want) {
			return true
		}
	}
	return false
}

// Check NDJSON syntax/encoding only. No event field becomes an authenticated
// actor, audit record, signature verdict or product mutation.
func validateActivity(data []byte) error {
	if !utf8.Valid(data) || !validUnicodeEscapes(data) {
		return invalid("invalid activity Unicode")
	}
	for line := range bytes.SplitSeq(data, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if line[0] != '{' {
			return invalid("activity event must be a JSON object")
		}
		d := json.NewDecoder(bytes.NewReader(line))
		d.UseNumber()
		if jsonValue(d, 0) != nil {
			return invalid("invalid or duplicate activity JSON")
		}
		if _, e := d.Token(); e != io.EOF {
			return invalid("trailing activity JSON")
		}
	}
	return nil
}
