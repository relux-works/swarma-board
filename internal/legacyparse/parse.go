// Package legacyparse reads only the inspected product Markdown shape. It has
// no filesystem, repository, activity-authority or execution integrations.
// Field/escape grammar adapted from pkg/board/markdown/{readme,progress}.go
// at skill-project-management 09bac3d89822c9b83c04dff6483d7b2074097a82;
// Apache-2.0, see repository LICENSE and NOTICE.
package legacyparse

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/relux-works/swarma-board/internal/contract"
)

// Parsed retains supplied reciprocal edges separately for transfer validation.
// Exclusions are canonical removed metadata key names, qualified by transfer.
type Parsed struct {
	Element    contract.Element
	Blocks     []string
	Exclusions []string
}

var bulletPattern = regexp.MustCompile(`^[ \t]*[-*+][ \t](.*)$`)
var checklistPattern = regexp.MustCompile(`^[ \t]*[-*+][ \t]+\[([ xX])\][ \t](.*)$`)
var estimatePattern = regexp.MustCompile(`^estimated\(fibonacci\(([0-9]+)\)\)$`)

var progressFields = map[string]string{
	"status": "status", "review": "review", "task class": "task_class", "task_class": "task_class", "taskclass": "task_class",
	"estimate": "estimate", "assigned to": "assigned_to", "created": "created", "last update": "last_update",
	"blocked by": "blocked_by", "blockedby": "blocked_by", "blocks": "blocks", "checklist": "checklist", "notes": "notes",
	"precondition resources": "precondition_resources", "outcome resources": "outcome_resources",
}

var removed = map[string]string{
	"integrationcheckpointed": "integrationCheckpointed",
	"change_request":          "change_request", "change request": "change_request",
	"change_request lifecycle": "change_request.lifecycle", "change_request.lifecycle": "change_request.lifecycle",
	"change_request revision": "change_request.revision", "change_request.revision": "change_request.revision",
	"change_request state": "change_request.state", "change_request.state": "change_request.state",
	"change_request evidence": "change_request.evidence", "change_request.evidence": "change_request.evidence",
	"run_id": "run_id", "session_id": "session_id", "provider": "provider", "model": "model", "commit_ack": "commit_ack", "txn_id": "txn_id",
}

// Parse preserves the inspected field/escape/framing semantics, rejecting
// meaningful text that cannot be represented by the frozen product record.
func Parse(readme, progress []byte) (Parsed, error) {
	p := Parsed{Element: contract.Element{Status: "backlog", Review: "required", TaskClass: "code", BlockedBy: []string{}, ManualBlockedBy: []string{}, Checklist: []contract.ChecklistItem{}, ResourceRefs: []contract.ResourceRef{}}, Blocks: []string{}, Exclusions: []string{}}
	if len(readme) > 8<<20 || len(progress) > 8<<20 || !utf8.Valid(readme) || !utf8.Valid(progress) || strings.ContainsRune(string(readme), 0) || strings.ContainsRune(string(progress), 0) {
		return p, fmt.Errorf("invalid Markdown encoding or size")
	}
	rd, err := parseMarkdownDocument(string(readme), true)
	if err != nil {
		return p, err
	}
	first := len(rd.source)
	if len(rd.sections) > 0 {
		first = rd.sections[0].headingStart
	}
	if rd.title == nil {
		// The inspected reader permits an absent historical title. Preserve
		// it as empty, but never discard a free-text preamble.
		if strings.TrimSpace(rd.source[:first]) != "" {
			return p, fmt.Errorf("unrepresented README preamble")
		}
	} else {
		p.Element.Title = decodeInlineBoundaryWhitespace(rd.source[rd.title.valueStart:rd.title.valueEnd])
		titleEnd := strings.IndexByte(rd.source[rd.title.valueEnd:], '\n')
		if titleEnd < 0 {
			titleEnd = len(rd.source)
		} else {
			titleEnd += rd.title.valueEnd + 1
		}
		titleStart := strings.LastIndex(rd.source[:rd.title.valueStart], "\n") + 1
		if strings.TrimSpace(rd.source[:titleStart]) != "" || titleEnd > first || strings.TrimSpace(rd.source[titleEnd:first]) != "" {
			return p, fmt.Errorf("unrepresented README preamble")
		}
	}
	seen := map[string]bool{}
	for _, s := range rd.sections {
		key := s.normalized
		if key != "description" && key != "scope" && key != "acceptance criteria" {
			return p, fmt.Errorf("unknown README section")
		}
		if seen[key] {
			return p, fmt.Errorf("duplicate README section")
		}
		seen[key] = true
		body := decodeMarkdownBlock(rd.sectionBody(&s))
		switch key {
		case "description":
			p.Element.Description = body
		case "scope":
			p.Element.Scope = body
		case "acceptance criteria":
			p.Element.AcceptanceCriteria = body
		}
	}
	pd, err := parseMarkdownDocument(string(progress), false)
	if err != nil {
		return p, err
	}
	first = len(pd.source)
	if len(pd.sections) > 0 {
		first = pd.sections[0].headingStart
	}
	if strings.TrimSpace(pd.source[:first]) != "" {
		return p, fmt.Errorf("unrepresented progress preamble")
	}
	seen = map[string]bool{}
	for _, s := range pd.sections {
		key := progressFields[s.normalized]
		exclusion := removed[s.normalized]
		if key == "" && exclusion == "" {
			return p, fmt.Errorf("unknown progress section")
		}
		if exclusion != "" {
			key = "excluded:" + exclusion
		}
		if seen[key] {
			return p, fmt.Errorf("duplicate progress field")
		}
		seen[key] = true
		body := pd.sectionBody(&s)
		trim := strings.TrimSpace(body)
		if exclusion != "" {
			keys, err := removedKeys(exclusion, trim)
			if err != nil {
				return p, err
			}
			p.Exclusions = append(p.Exclusions, keys...)
			continue
		}
		switch key {
		case "status":
			if trim != "" {
				p.Element.Status = trim
			}
		case "review":
			if trim != "" {
				p.Element.Review = trim
			}
		case "task_class":
			if trim != "" {
				p.Element.TaskClass = trim
			}
		case "assigned_to":
			if body != "(none)" {
				p.Element.AssignedTo = decodeMarkdownBlock(body)
			}
		case "created", "last_update":
			if trim != "" {
				t, e := time.Parse(time.RFC3339, trim)
				if e != nil {
					return p, fmt.Errorf("invalid timestamp")
				}
				t = t.UTC()
				if key == "created" {
					p.Element.CreatedAt = &t
				} else {
					p.Element.UpdatedAt = &t
				}
			}
		case "estimate":
			if trim == "" {
				continue
			}
			if trim == "notEstimated" {
				p.Element.Estimate = &contract.Estimate{Kind: trim}
				continue
			}
			m := estimatePattern.FindStringSubmatch(trim)
			if m == nil {
				return p, fmt.Errorf("invalid persisted estimate")
			}
			n, e := strconv.Atoi(m[1])
			if e != nil {
				return p, fmt.Errorf("estimate overflow")
			}
			p.Element.Estimate = &contract.Estimate{Kind: "estimated", Scale: &contract.EstimateScale{Kind: "fibonacci", Number: n}}
		case "blocked_by", "blocks":
			list, e := parseList(body)
			if e != nil {
				return p, e
			}
			if key == "blocks" {
				p.Blocks = list
			} else {
				p.Element.BlockedBy = list
			}
		case "checklist":
			if strings.TrimSpace(body) == "(empty)" {
				continue
			}
			for line := range strings.SplitSeq(body, "\n") {
				line = strings.TrimSuffix(line, "\r")
				if strings.TrimSpace(line) == "" {
					continue
				}
				m := checklistPattern.FindStringSubmatch(line)
				if m == nil {
					return p, fmt.Errorf("unrepresented checklist content")
				}
				p.Element.Checklist = append(p.Element.Checklist, contract.ChecklistItem{Text: decodeInline(m[2]), Checked: m[1] != " "})
			}
		case "notes":
			p.Element.Notes = decodeMarkdownBlock(body)
			if ReservedAuthority(p.Element.Notes) {
				return p, fmt.Errorf("reserved authority notes require upstream sanitization")
			}
		case "precondition_resources", "outcome_resources":
			typ := "precondition"
			if key == "outcome_resources" {
				typ = "outcome"
			}
			if trim == "(none)" || trim == "- (none)" {
				continue
			}
			for line := range strings.SplitSeq(body, "\n") {
				line = strings.TrimSuffix(line, "\r")
				trim := strings.TrimSpace(line)
				if trim == "" {
					continue
				}
				ref, e := parseResource(strings.TrimLeft(line, " \t"), typ)
				if e != nil {
					return p, e
				}
				if strings.HasSuffix(strings.TrimSpace(ref.Name), "_manual-landing.md") {
					return p, fmt.Errorf("reserved marker resource requires upstream sanitization")
				}
				p.Element.ResourceRefs = append(p.Element.ResourceRefs, ref)
			}
		}
	}
	seenExclusions := map[string]bool{}
	for _, key := range p.Exclusions {
		if seenExclusions[key] {
			return p, fmt.Errorf("duplicate removed metadata field")
		}
		seenExclusions[key] = true
	}
	return p, nil
}

func parseList(body string) ([]string, error) {
	result := []string{}
	trim := strings.TrimSpace(body)
	if trim == "(none)" || trim == "- (none)" {
		return result, nil
	}
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSuffix(line, "\r")
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		m := bulletPattern.FindStringSubmatch(line)
		if m == nil {
			return nil, fmt.Errorf("unrepresented dependency content")
		}
		v := decodeInline(m[1])
		if v == "" {
			return nil, fmt.Errorf("empty dependency")
		}
		result = append(result, v)
	}
	return result, nil
}

// Resource decoding follows the inspected escape-aware Markdown link grammar.
func parseResource(line, typ string) (contract.ResourceRef, error) {
	if !strings.HasPrefix(line, "- [") {
		return contract.ResourceRef{}, fmt.Errorf("invalid resource reference")
	}
	name, next, ok := consumeEscapedUntil(line, 3, ']')
	if !ok || next >= len(line) || line[next] != '(' {
		return contract.ResourceRef{}, fmt.Errorf("invalid resource link")
	}
	uri, next, ok := consumeEscapedUntil(line, next+1, ')')
	if !ok {
		return contract.ResourceRef{}, fmt.Errorf("invalid resource URI")
	}
	rest := strings.TrimLeft(line[next:], " \t")
	desc := ""
	if rest != "" {
		if strings.HasPrefix(rest, "—") {
			desc = strings.TrimPrefix(rest, "—")
		} else if strings.HasPrefix(rest, "-") {
			desc = rest[1:]
		} else {
			return contract.ResourceRef{}, fmt.Errorf("invalid resource description")
		}
		if len(desc) > 0 && (desc[0] == ' ' || desc[0] == '\t') {
			desc = desc[1:]
		}
	}
	return contract.ResourceRef{Name: decodeInline(name), URI: decodeInline(uri), Description: decodeInlineBoundaryWhitespace(desc), Type: typ}, nil
}
func consumeEscapedUntil(v string, start int, d byte) (string, int, bool) {
	for i := start; i < len(v); i++ {
		if v[i] == '\\' && i+1 < len(v) {
			i++
			continue
		}
		if v[i] == d {
			return v[start:i], i + 1, true
		}
	}
	return "", 0, false
}

// Only recognized structured scalar metadata is excludable. Prose, nested
// documents and credential-bearing payloads must be sanitized upstream.
func validateRemoved(key, value string) error {
	if value == "" {
		return nil
	}
	if strings.ContainsAny(value, "\r\n\x00") || strings.Contains(value, "```") || strings.ContainsAny(value, "{}[]") {
		return fmt.Errorf("removed metadata requires upstream sanitization")
	}
	if key == "integrationCheckpointed" && value != "true" && value != "false" {
		return fmt.Errorf("invalid removed checkpoint metadata")
	}
	return nil
}

func ReservedAuthority(notes string) bool {
	n := strings.ToLower(notes)
	for _, marker := range []string{"manual-landing", "manual_landing", "integrationcheckpointed", "commit_ack", "txn_id", "<!-- cr:", "<!--cr:", "[cr-accepted]", "[review-accept]"} {
		if strings.Contains(n, marker) {
			return true
		}
	}
	return false
}

// Change-request metadata may be a flat JSON object of exactly the frozen
// lifecycle/revision/state/evidence keys. Duplicate/unknown/nested payloads
// are not treated as an excuse to discard arbitrary content.
func removedKeys(key, value string) ([]string, error) {
	if key != "change_request" || !strings.HasPrefix(value, "{") {
		if err := validateRemoved(key, value); err != nil {
			return nil, err
		}
		return []string{key}, nil
	}
	d := json.NewDecoder(strings.NewReader(value))
	d.UseNumber()
	tok, err := d.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("invalid removed metadata")
	}
	seen := map[string]bool{}
	keys := []string{}
	for d.More() {
		tok, err = d.Token()
		if err != nil {
			return nil, fmt.Errorf("invalid removed metadata")
		}
		k, ok := tok.(string)
		if !ok || seen[k] || (k != "lifecycle" && k != "revision" && k != "state" && k != "evidence") {
			return nil, fmt.Errorf("unknown or duplicate removed metadata")
		}
		seen[k] = true
		var raw json.RawMessage
		if d.Decode(&raw) != nil {
			return nil, fmt.Errorf("invalid removed metadata value")
		}
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 || raw[0] == '{' || raw[0] == '[' {
			return nil, fmt.Errorf("removed metadata payload requires upstream sanitization")
		}
		var text string
		if raw[0] == '"' {
			if json.Unmarshal(raw, &text) != nil || validateRemoved(k, text) != nil {
				return nil, fmt.Errorf("invalid removed metadata scalar")
			}
		}
		keys = append(keys, "change_request."+k)
	}
	if tok, err = d.Token(); err != nil || tok != json.Delim('}') {
		return nil, fmt.Errorf("invalid removed metadata")
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing removed metadata")
	}
	if len(keys) == 0 {
		keys = append(keys, "change_request")
	}
	return keys, nil
}
