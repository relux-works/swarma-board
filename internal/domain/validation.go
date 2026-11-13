package domain

import (
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/relux-works/swarma-board/internal/contract"
	"github.com/relux-works/swarma-board/internal/query"
)

var legacyID = regexp.MustCompile(`^(EPIC|STORY|TASK|BUG)-([0-9]+|[0-9]{6}-[a-z0-9]{6})$`)
var newID = regexp.MustCompile(`^(EPIC|STORY|TASK|BUG)-[0-9]{6}-[a-z0-9]{6}$`)
var digestRE = regexp.MustCompile(`^[a-f0-9]{64}$`)

func errCode(code, message string) error { return query.Error(code, message) }
func normalizeName(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), " ", "-"), "_", "-")
	var out strings.Builder
	for _, c := range s {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' {
			out.WriteRune(c)
		}
	}
	s = out.String()
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.Trim(s, "-")
}
func statusGuarded(s string) bool {
	return query.Contains([]string{"development", "to-review", "reviewing", "integrating", "done"}, s)
}
func validateElements(elements []contract.Element) error {
	if len(elements) > 10000 {
		return errCode("invalid_snapshot", "Element limit exceeded")
	}
	seen := map[string]bool{}
	for _, e := range elements {
		lower := strings.ToLower(e.ID)
		if seen[lower] || !legacyID.MatchString(e.ID) || !strings.HasPrefix(e.ID, strings.ToUpper(e.Type)+"-") {
			return errCode("invalid_snapshot", "Invalid or duplicate element ID")
		}
		seen[lower] = true
		if !query.Contains(query.Types, e.Type) || !query.Contains(query.Statuses, e.Status) || strings.TrimSpace(e.Name) == "" {
			return errCode("invalid_snapshot", "Invalid element fields")
		}
		for _, s := range []string{e.Name, e.Title, e.Description, e.Scope, e.AcceptanceCriteria, e.AssignedTo, e.Notes} {
			if !utf8.ValidString(s) {
				return errCode("invalid_snapshot", "Invalid text encoding")
			}
		}
		if e.Review != "" && !query.Contains([]string{"required", "light", "none"}, e.Review) || e.TaskClass != "" && !query.Contains([]string{"code", "metadata", "docs", "research"}, e.TaskClass) {
			return errCode("invalid_snapshot", "Invalid descriptive policy")
		}
		for _, t := range []*contract.Estimate{e.Estimate} {
			if t == nil {
				continue
			}
			if !query.Leaf(e.Type) {
				return errCode("invalid_snapshot", "Container estimate must be null")
			}
			switch t.Kind {
			case "notEstimated":
				if t.Scale != nil {
					return errCode("invalid_snapshot", "Invalid estimate")
				}
			case "estimated":
				if t.Scale == nil || t.Scale.Kind != "fibonacci" || t.Scale.Number < 0 {
					return errCode("invalid_snapshot", "Invalid estimate")
				}
			default:
				return errCode("invalid_snapshot", "Invalid estimate")
			}
		}
		for _, t := range []*time.Time{e.CreatedAt, e.UpdatedAt} {
			if t != nil {
				_, offset := t.Zone()
				if offset != 0 {
					return errCode("invalid_snapshot", "Timestamp must be UTC")
				}
			}
		}
		for _, c := range e.Checklist {
			if !utf8.ValidString(c.Text) {
				return errCode("invalid_snapshot", "Invalid checklist text")
			}
		}
		for _, r := range e.ResourceRefs {
			u, err := url.Parse(r.URI)
			if err != nil || u == nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(r.URI, "?#\x00\r\n") || r.Type != "precondition" && r.Type != "outcome" || u.Scheme != "https" && u.Scheme != "urn" || u.Scheme == "https" && u.Host == "" || u.Scheme == "urn" && u.Opaque == "" || !utf8.ValidString(r.Name+r.Description+r.URI) {
				return errCode("invalid_snapshot", "Invalid resource reference")
			}
		}
	}
	b := query.Index(elements)
	graph := map[string][]string{}
	for _, id := range b.IDs {
		e := b.Elements[id]
		expected := ""
		switch e.Type {
		case "story":
			expected = "epic"
		case "task", "bug":
			expected = "story"
		}
		if expected == "" {
			if e.ParentID != "" {
				return errCode("invalid_parent", "Epic cannot have parent")
			}
		} else {
			p, ok := b.Find(e.ParentID)
			if !ok || p.Type != expected || p.ID == e.ID {
				return errCode("invalid_parent", "Invalid parent")
			}
		}
		edges := e.BlockedBy
		if !query.Leaf(e.Type) {
			if len(e.BlockedBy) > 0 {
				return errCode("dependency_conflict", "Container task edges are not writable")
			}
			edges = e.ManualBlockedBy
		} else if len(e.ManualBlockedBy) > 0 {
			return errCode("dependency_conflict", "Manual advisory edges require containers")
		}
		edgeSeen := map[string]bool{}
		for _, ref := range edges {
			target, ok := b.Find(ref)
			key := strings.ToLower(ref)
			if !ok || edgeSeen[key] || target.ID == e.ID || query.Leaf(target.Type) != query.Leaf(e.Type) || query.Contains(b.Ancestors(id), target.ID) || query.Contains(b.Ancestors(target.ID), id) {
				return errCode("dependency_conflict", "Invalid dependency reference")
			}
			edgeSeen[key] = true
			if query.Leaf(e.Type) {
				graph[id] = append(graph[id], target.ID)
			}
		}
		if query.Leaf(e.Type) && statusGuarded(e.Status) && b.Blocked(e) {
			return errCode("blocked", "Active or missing blocker")
		}
		if query.Leaf(e.Type) && query.Contains([]string{"development", "to-review"}, e.Status) && strings.TrimSpace(e.AssignedTo) == "" {
			return errCode("not_assigned", "Assignee required")
		}
		if !query.Terminal(e.Status) {
			for _, a := range b.Ancestors(id) {
				if query.Terminal(b.Elements[a].Status) {
					return errCode("dependency_conflict", "Explicitly reopen terminal ancestors first")
				}
			}
		}
	}
	if len(cycles(b.IDs, graph)) > 0 {
		return errCode("dependency_cycle", "Task dependency cycle")
	}
	return nil
}
