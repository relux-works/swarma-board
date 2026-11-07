package dataprovider

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

var scopeID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var digestID = regexp.MustCompile(`^[a-f0-9]{64}$`)
var elementID = regexp.MustCompile(`^(EPIC|STORY|TASK|BUG)-([0-9]+|[0-9]{6}-[a-z0-9]{6})$`)
var lookupID = regexp.MustCompile(`(?i)^(EPIC|STORY|TASK|BUG)-([0-9]+|[0-9]{6}-[a-z0-9]{6})$`)
var provenancePath = regexp.MustCompile(`^provenance/[A-Za-z0-9][A-Za-z0-9._-]*$`)

func ValidateScope(s Scope) error {
	if !scopeID.MatchString(s.ProductID) || !scopeID.MatchString(s.BoardID) {
		return NewError(InvalidRequest)
	}
	return nil
}
func ValidateDigest(d string) error {
	if !digestID.MatchString(d) {
		return NewError(InvalidRequest)
	}
	return nil
}
func ValidateActor(a Actor) error {
	if a.AccountID == "" || len(a.AccountID) > 256 || !utf8.ValidString(a.AccountID) || a.At.IsZero() {
		return NewError(InvalidRequest)
	}
	for _, r := range a.AccountID {
		if unicode.IsControl(r) {
			return NewError(InvalidRequest)
		}
	}
	if len(a.RequestID) < 1 || len(a.RequestID) > 128 {
		return NewError(InvalidRequest)
	}
	for _, c := range []byte(a.RequestID) {
		if c < 32 || c > 126 {
			return NewError(InvalidRequest)
		}
	}
	_, offset := a.At.Zone()
	if offset != 0 {
		return NewError(InvalidRequest)
	}
	if _, err := a.At.MarshalJSON(); err != nil {
		return NewError(InvalidRequest)
	}
	switch a.Operation {
	case "create", "set_details", "rename", "assign", "unassign", "set_status", "set_estimate", "link", "unlink", "check_item", "uncheck_item", "add_checklist_item", "remove_checklist_item", "set_notes", "set_parent", "delete", "import", "put_resource", "delete_resource":
		return nil
	}
	return NewError(InvalidRequest)
}

// NextRevision is called under the actual writer transaction, before the
// callback. It neither acquires a lock nor claims that a transaction exists.
func NextRevision(scope Scope, current, expected Revision) (Revision, error) {
	if err := ValidateScope(scope); err != nil {
		return Revision{}, err
	}
	if current.Scope != scope || expected.Scope != scope {
		return Revision{}, NewError(InvalidRequest)
	}
	if current.Counter != expected.Counter {
		return Revision{}, NewError(RevisionConflict)
	}
	if current.Counter == math.MaxUint64 {
		return Revision{}, NewError(Unavailable)
	}
	return Revision{Scope: scope, Counter: current.Counter + 1}, nil
}
func ValidateResourceInfo(info ResourceInfo) error {
	if err := ValidateDigest(info.SHA256); err != nil {
		return err
	}
	if info.Size < 0 {
		return NewError(InvalidRequest)
	}
	if info.Size > MaxResourceBytes {
		return NewError(PayloadTooLarge)
	}
	return nil
}

// ResourceDigest recognizes the reserved URN namespace before generic URI
// validation. Scheme and namespace are ASCII case insensitive; digest is not.
// A true reserved result with an error must never fall back to an external URN.
func ResourceDigest(uri string) (digest string, reserved bool, err error) {
	parts := strings.SplitN(uri, ":", 3)
	if len(parts) < 2 || !asciiEqualFold(parts[0], "urn") || !asciiEqualFold(parts[1], "sha256") {
		return "", false, nil
	}
	if len(parts) != 3 || !digestID.MatchString(parts[2]) {
		return "", true, NewError(InvalidSnapshot)
	}
	return parts[2], true, nil
}
func asciiEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range len(a) {
		x := a[i]
		y := b[i]
		if x >= 'A' && x <= 'Z' {
			x += 'a' - 'A'
		}
		if y >= 'A' && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

func ValidateProvenance(files []ProvenanceFile) error {
	if len(files) > MaxProvenanceCount {
		return NewError(InvalidSnapshot)
	}
	seen := map[string]bool{}
	for _, f := range files {
		if !provenancePath.MatchString(f.Path) || len(strings.TrimPrefix(f.Path, "provenance/")) > MaxProvenanceNameBytes || seen[f.Path] || !digestID.MatchString(f.SHA256) {
			return NewError(InvalidSnapshot)
		}
		seen[f.Path] = true
		if int64(len(f.Data)) > MaxProvenanceBytes {
			return NewError(PayloadTooLarge)
		}
		h := sha256.Sum256(f.Data)
		if hex.EncodeToString(h[:]) != f.SHA256 {
			return NewError(InvalidSnapshot)
		}
	}
	return nil
}

// ValidateRecordShape preserves native-v1 facts. It does not compute inverse
// edges, status transitions, plans, reference lifecycle or domain permissions.
func ValidateRecordShape(e Element) error {
	if !elementID.MatchString(e.ID) || !strings.HasPrefix(e.ID, strings.ToUpper(e.Type)+"-") || !slices.Contains([]string{"epic", "story", "task", "bug"}, e.Type) || strings.TrimSpace(e.Name) == "" || !slices.Contains([]string{"backlog", "analysis", "to-dev", "development", "to-review", "reviewing", "integrating", "done", "closed", "blocked"}, e.Status) {
		return NewError(InvalidSnapshot)
	}
	for _, s := range []string{e.ID, e.Type, e.Name, e.Title, e.Description, e.Scope, e.Status, e.AcceptanceCriteria, e.AssignedTo, e.Notes, e.ParentID, e.Review, e.TaskClass} {
		if !utf8.ValidString(s) {
			return NewError(InvalidSnapshot)
		}
	}
	for _, edges := range [][]string{e.BlockedBy, e.ManualBlockedBy} {
		for _, id := range edges {
			if !utf8.ValidString(id) {
				return NewError(InvalidSnapshot)
			}
		}
	}
	if e.Review != "" && !slices.Contains([]string{"required", "light", "none"}, e.Review) || e.TaskClass != "" && !slices.Contains([]string{"code", "metadata", "docs", "research"}, e.TaskClass) {
		return NewError(InvalidSnapshot)
	}
	if e.Estimate != nil {
		v := e.Estimate
		if e.Type != "task" && e.Type != "bug" || v.Kind == "notEstimated" && v.Scale != nil || v.Kind == "estimated" && (v.Scale == nil || v.Scale.Kind != "fibonacci" || v.Scale.Number < 0) || v.Kind != "estimated" && v.Kind != "notEstimated" {
			return NewError(InvalidSnapshot)
		}
	}
	if e.CreatedAt != nil {
		_, off := e.CreatedAt.Zone()
		if off != 0 {
			return NewError(InvalidSnapshot)
		}
		if _, err := e.CreatedAt.MarshalJSON(); err != nil {
			return NewError(InvalidSnapshot)
		}
	}
	if e.UpdatedAt != nil {
		_, off := e.UpdatedAt.Zone()
		if off != 0 {
			return NewError(InvalidSnapshot)
		}
		if _, err := e.UpdatedAt.MarshalJSON(); err != nil {
			return NewError(InvalidSnapshot)
		}
	}
	for _, c := range e.Checklist {
		if !utf8.ValidString(c.Text) {
			return NewError(InvalidSnapshot)
		}
	}
	for _, r := range e.ResourceRefs {
		if !utf8.ValidString(r.Name) || !utf8.ValidString(r.URI) || !utf8.ValidString(r.Description) || !utf8.ValidString(r.Type) || r.Type != "precondition" && r.Type != "outcome" {
			return NewError(InvalidSnapshot)
		}
		_, reserved, err := ResourceDigest(r.URI)
		if err != nil {
			return err
		}
		if reserved {
			continue
		}
		u, err := url.Parse(r.URI)
		if err != nil || u == nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(r.URI, "?#\x00\r\n") || u.Scheme != "https" && u.Scheme != "urn" || u.Scheme == "https" && u.Host == "" || u.Scheme == "urn" && u.Opaque == "" {
			return NewError(InvalidSnapshot)
		}
	}
	return nil
}

// ValidateChangeSet checks input structure before consuming any reader. It does
// not replace resulting-state validation, durable staging, or event budgeting.
func ValidateChangeSet(c ChangeSet) error {
	seen := map[string]bool{}
	for _, e := range c.Upserts {
		id := strings.ToLower(e.ID)
		if seen[id] {
			return NewError(InvalidRequest)
		}
		seen[id] = true
		if err := ValidateRecordShape(e); err != nil {
			return err
		}
	}
	for _, id := range c.Deletes {
		key := strings.ToLower(id)
		if seen[key] {
			return NewError(InvalidRequest)
		}
		seen[key] = true
		if !lookupID.MatchString(id) {
			return NewError(InvalidRequest)
		}
	}
	if err := ValidateProvenance(c.Provenance); err != nil {
		return err
	}
	seen = map[string]bool{}
	for _, r := range c.PutResources {
		if seen[r.Info.SHA256] || r.Reader == nil {
			return NewError(InvalidRequest)
		}
		seen[r.Info.SHA256] = true
		if err := ValidateResourceInfo(r.Info); err != nil {
			return err
		}
	}
	for _, d := range c.DeleteResources {
		if seen[d] {
			return NewError(InvalidRequest)
		}
		seen[d] = true
		if err := ValidateDigest(d); err != nil {
			return err
		}
	}
	return nil
}

// ValidateSnapshot validates scoped storage facts and resource references. Task
// graph semantics remain the shared domain's responsibility. Catalog/record
// ordering is checked rather than silently repaired by the parity encoder.
func ValidateSnapshot(s Snapshot) error {
	if err := preflightJSON(s, MaxSnapshotBytes); err != nil {
		return err
	}
	if err := ValidateScope(s.Board.Scope); err != nil {
		return NewError(InvalidSnapshot)
	}
	if s.Revision.Scope != s.Board.Scope || !utf8.ValidString(s.Board.Name) || !utf8.ValidString(s.Board.Description) || len(s.Elements) > MaxElements || len(s.Resources) > MaxResourceCount {
		return NewError(InvalidSnapshot)
	}
	if err := ValidateProvenance(s.Provenance); err != nil {
		return err
	}
	seen := map[string]bool{}
	prev := ""
	for _, e := range s.Elements {
		if e.ID <= prev || seen[strings.ToLower(e.ID)] {
			return NewError(InvalidSnapshot)
		}
		prev = e.ID
		seen[strings.ToLower(e.ID)] = true
		if err := ValidateRecordShape(e); err != nil {
			return err
		}
	}
	prev = ""
	for _, p := range s.Provenance {
		if p.Path <= prev {
			return NewError(InvalidSnapshot)
		}
		prev = p.Path
	}
	resources := map[string]bool{}
	prev = ""
	var total int64
	for _, r := range s.Resources {
		if r.SHA256 <= prev {
			return NewError(InvalidSnapshot)
		}
		prev = r.SHA256
		if err := ValidateResourceInfo(r); err != nil {
			return err
		}
		total += r.Size
		resources[r.SHA256] = true
	}
	if total > MaxScopeResourceBytes {
		return NewError(PayloadTooLarge)
	}
	for _, e := range s.Elements {
		for _, ref := range e.ResourceRefs {
			d, reserved, err := ResourceDigest(ref.URI)
			if err != nil {
				return err
			}
			if reserved && !resources[d] {
				return NewError(InvalidSnapshot)
			}
		}
	}
	_, err := EncodeSnapshot(io.Discard, s)
	return err
}
