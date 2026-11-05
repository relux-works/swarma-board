package transfer

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/relux-works/swarma-board/internal/contract"
)

var projectPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var idPattern = regexp.MustCompile(`(?i)^(EPIC|STORY|TASK|BUG)-(?:[0-9]+|[0-9]{6}-[0-9a-z]{6})$`)
var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var provenancePattern = regexp.MustCompile(`^provenance/[A-Za-z0-9][A-Za-z0-9._-]*$`)

func invalid(message string) error {
	return &contract.Error{Code: "invalid_snapshot", Message: message}
}
func leaf(e contract.Element) bool     { return e.Type == "task" || e.Type == "bug" }
func terminal(e contract.Element) bool { return e.Status == "done" || e.Status == "closed" }
func oneOf(v string, values ...string) bool {
	for _, s := range values {
		if v == s {
			return true
		}
	}
	return false
}
func validID(id, typ string) bool {
	if len(id) > 128 || !idPattern.MatchString(id) || !strings.EqualFold(strings.SplitN(id, "-", 2)[0], typ) {
		return false
	}
	parts := strings.Split(id, "-")
	if len(parts) == 2 {
		_, err := strconv.Atoi(parts[1])
		return err == nil
	}
	return true
}
func validateElements(elements []contract.Element) error {
	if len(elements) > MaxElements {
		return invalid("element count limit")
	}
	byID := make(map[string]int, len(elements))
	for i, e := range elements {
		if !oneOf(e.Type, "epic", "story", "task", "bug") || !validID(e.ID, e.Type) {
			return invalid("invalid element ID/type")
		}
		key := strings.ToLower(e.ID)
		if _, ok := byID[key]; ok {
			return invalid("duplicate element ID")
		}
		byID[key] = i
		if e.Name == "" || !utf8.ValidString(e.Name) || strings.ContainsAny(e.Name, "/\\\x00\r\n") {
			return invalid("invalid element name")
		}
		if !oneOf(e.Status, "backlog", "analysis", "to-dev", "development", "to-review", "reviewing", "integrating", "done", "closed", "blocked") {
			return invalid("invalid status")
		}
		if e.Review != "" && !oneOf(e.Review, "required", "light", "none") {
			return invalid("invalid review")
		}
		if e.TaskClass != "" && !oneOf(e.TaskClass, "code", "metadata", "docs", "research") {
			return invalid("invalid task class")
		}
		if e.BlockedBy == nil || e.ManualBlockedBy == nil || e.Checklist == nil || e.ResourceRefs == nil {
			return invalid("element arrays must be explicit")
		}
		if !leaf(e) && (e.Estimate != nil || len(e.BlockedBy) > 0) || leaf(e) && len(e.ManualBlockedBy) > 0 {
			return invalid("invalid container/leaf fields")
		}
		if est := e.Estimate; est != nil {
			switch est.Kind {
			case "notEstimated":
				if est.Scale != nil {
					return invalid("notEstimated carries scale")
				}
			case "estimated":
				if est.Scale == nil || est.Scale.Kind != "fibonacci" || est.Scale.Number < 0 {
					return invalid("invalid estimate scale")
				}
			default:
				return invalid("invalid estimate kind")
			}
		}
		resources := map[string]bool{}
		for _, ref := range e.ResourceRefs {
			if ref.Name == "" || resources[ref.Name] || !oneOf(ref.Type, "precondition", "outcome") || !safeURI(ref.URI) {
				return invalid("unsupported or unsafe resource reference")
			}
			resources[ref.Name] = true
		}
	}
	for _, e := range elements {
		if e.Type == "epic" {
			if e.ParentID != "" {
				return invalid("epic has parent")
			}
		} else {
			pi, ok := byID[strings.ToLower(e.ParentID)]
			if !ok {
				return invalid("missing parent")
			}
			parent := elements[pi]
			if e.Type == "story" && parent.Type != "epic" || leaf(e) && parent.Type != "story" {
				return invalid("invalid parent type")
			}
		}
	}
	for _, e := range elements {
		// Hierarchy depth is at most three after the parent type check.
		for p := e.ParentID; p != ""; {
			pi, ok := byID[strings.ToLower(p)]
			if !ok {
				return invalid("missing ancestor")
			}
			a := elements[pi]
			if strings.EqualFold(a.ID, e.ID) {
				return invalid("hierarchy cycle")
			}
			if terminal(a) && !terminal(e) {
				return invalid("active descendant under terminal ancestor")
			}
			p = a.ParentID
		}
		edges := e.BlockedBy
		if !leaf(e) {
			edges = e.ManualBlockedBy
		}
		seen := map[string]bool{}
		for _, id := range edges {
			key := strings.ToLower(id)
			bi, ok := byID[key]
			if !ok {
				return invalid("dangling dependency")
			}
			b := elements[bi]
			if seen[key] || strings.EqualFold(id, e.ID) {
				return invalid("duplicate or self dependency")
			}
			seen[key] = true
			if leaf(e) != leaf(b) {
				return invalid("mixed leaf/container dependency")
			}
			if ancestor(elements, byID, e.ID, b.ID) || ancestor(elements, byID, b.ID, e.ID) {
				return invalid("ancestor dependency")
			}
			if leaf(e) && e.Status == "done" && !terminal(b) {
				return invalid("done leaf has active blocker")
			}
		}
	}
	// Iterative Kahn check; advisory container cycles remain legal diagnostics.
	degree := make(map[string]int)
	dependents := make(map[string][]string)
	for _, e := range elements {
		if !leaf(e) {
			continue
		}
		key := strings.ToLower(e.ID)
		degree[key] = len(e.BlockedBy)
		for _, b := range e.BlockedBy {
			bk := strings.ToLower(b)
			dependents[bk] = append(dependents[bk], key)
		}
	}
	queue := make([]string, 0, len(degree))
	for id, n := range degree {
		if n == 0 {
			queue = append(queue, id)
		}
	}
	for i := 0; i < len(queue); i++ {
		for _, id := range dependents[queue[i]] {
			degree[id]--
			if degree[id] == 0 {
				queue = append(queue, id)
			}
		}
	}
	if len(queue) != len(degree) {
		return invalid("task dependency cycle")
	}
	return nil
}
func ancestor(elements []contract.Element, index map[string]int, a, b string) bool {
	for p := elements[index[strings.ToLower(b)]].ParentID; p != ""; p = elements[index[strings.ToLower(p)]].ParentID {
		if strings.EqualFold(a, p) {
			return true
		}
	}
	return false
}
func safeURI(value string) bool {
	if value == "" || !utf8.ValidString(value) || strings.ContainsAny(value, "\\\x00\r\n\t ") {
		return false
	}
	u, e := url.Parse(value)
	if e != nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(value, "?#") {
		return false
	}
	switch u.Scheme {
	case "https":
		return u.Host != "" && u.Hostname() != "" && u.Opaque == ""
	case "urn":
		return u.Host == "" && u.Opaque != "" && strings.Contains(u.Opaque, ":")
	}
	return false
}
func validProvenancePath(p string) bool {
	return len(p) <= 255 && provenancePattern.MatchString(p) && safePath(p, false)
}
func validateText(e contract.Element) error {
	// JSON encoding would replace invalid UTF-8 on Encode, so reject before it.
	values := []string{e.ID, e.Type, e.Name, e.ParentID, e.Title, e.Description, e.Scope, e.AcceptanceCriteria, e.Status, e.AssignedTo, e.Notes, e.Review, e.TaskClass}
	values = append(values, e.BlockedBy...)
	values = append(values, e.ManualBlockedBy...)
	for _, c := range e.Checklist {
		values = append(values, c.Text)
	}
	for _, r := range e.ResourceRefs {
		values = append(values, r.Name, r.URI, r.Description, r.Type)
	}
	for _, v := range values {
		if !utf8.ValidString(v) || strings.ContainsRune(v, 0) {
			return invalid("invalid product text encoding")
		}
	}
	for _, t := range []*time.Time{e.CreatedAt, e.UpdatedAt} {
		if t != nil {
			_, offset := t.Zone()
			if offset != 0 || t.Year() < 0 || t.Year() > 9999 {
				return invalid("timestamp must be UTC RFC3339")
			}
		}
	}
	return nil
}
func digestBytes(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }

// A cheap preflight bound keeps encoding arbitrarily large caller-owned text
// from allocating an unbounded JSON buffer. Encoded size is checked as well.
func elementTextBytes(e contract.Element) int64 {
	var n int64
	for _, s := range []string{e.ID, e.Type, e.Name, e.ParentID, e.Title, e.Description, e.Scope, e.AcceptanceCriteria, e.Status, e.AssignedTo, e.Notes, e.Review, e.TaskClass} {
		n += int64(len(s))
	}
	for _, s := range e.BlockedBy {
		n += int64(len(s))
	}
	for _, s := range e.ManualBlockedBy {
		n += int64(len(s))
	}
	for _, c := range e.Checklist {
		n += int64(len(c.Text))
	}
	for _, r := range e.ResourceRefs {
		n += int64(len(r.Name)) + int64(len(r.URI)) + int64(len(r.Description)) + int64(len(r.Type))
	}
	return n
}
