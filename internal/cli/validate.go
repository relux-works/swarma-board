// Package cli exposes only the frozen swarma-board product commands.
package cli

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	aq "github.com/relux-works/skill-agent-facing-api/agentquery"
	"github.com/relux-works/swarma-board/internal/client"
	"github.com/relux-works/swarma-board/internal/contract"
)

var signatures = map[string]string{
	"get": "id", "list": "type? status? parent? assignee? blocked? skip? take?", "count": "type? status? parent? assignee? blocked?", "summary": "", "plan": "scope? mode? active?",
	"create": "type name parent? description? scope? ac? review? task_class?", "set_details": "id title? description? scope? ac? review? task_class?", "rename": "id name", "assign": "id agent", "unassign": "id", "set_status": "id status", "set_estimate": "id estimate scale? number?", "link": "id blocked_by", "unlink": "id blocked_by", "check_item": "id item", "uncheck_item": "id item", "add_checklist_item": "id text", "remove_checklist_item": "id item", "set_notes": "id text set?", "set_parent": "id parent", "delete": "id confirm",
}
var fields = strings.Fields("id type name title status review task_class estimate parent assignee description scope ac blockedBy blocks derivedBlockedBy derivedBlocks checklist notes created updated isBlocked isAdvisoryBlocked children preconditionResources outcomeResources")
var presets = map[string]string{"minimal": "id status", "default": "id name status", "overview": "id name status assignee parent", "planning": "id name status blockedBy blocks derivedBlockedBy derivedBlocks parent", "progress": "id status review task_class estimate checklist notes blockedBy derivedBlockedBy isBlocked isAdvisoryBlocked", "full": strings.Join(fields, " ")}

func readOperation(name string) bool {
	switch name {
	case "get", "list", "count", "summary", "plan":
		return true
	}
	return false
}
func badQuery(code string) error {
	return client.LocalError(code, "query does not match the frozen command grammar")
}

// Validate parses independently of P1 using the pinned agentquery AST library.
// The only lexical extension is D3/D4's explicit plan scope list; the baseline
// library lacks bracket tokens, so that value is encoded before AST parsing.
func Validate(query string, mutation bool) (contract.Operation, error) {
	if len(query) > client.MaxQuery {
		return contract.Operation{}, client.LocalError("payload_too_large", "query exceeds 8 MiB")
	}
	if !utf8.ValidString(query) {
		return contract.Operation{}, badQuery("query_error")
	}
	normalized, err := scopeLists(query)
	if err != nil {
		return contract.Operation{}, err
	}
	ast, err := aq.Parse(normalized, nil)
	if err != nil {
		return contract.Operation{}, badQuery("query_error")
	}
	if len(ast.Statements) != 1 {
		return contract.Operation{}, badQuery("unsupported_batch")
	}
	s := ast.Statements[0]
	sig, ok := signatures[s.Operation]
	if !ok {
		return contract.Operation{}, badQuery("unsupported_operation")
	}
	if readOperation(s.Operation) == mutation {
		return contract.Operation{}, client.LocalError("unsupported_operation", "q requires a read; m requires a mutation")
	}
	keys := strings.Fields(sig)
	allowed := map[string]bool{}
	required := map[string]bool{}
	for _, k := range keys {
		key := strings.TrimSuffix(k, "?")
		allowed[key] = true
		if key == k {
			required[key] = true
		}
	}
	vals := map[string]string{}
	op := contract.Operation{Name: s.Operation, Args: map[string]json.RawMessage{}}
	for i, a := range s.Args {
		key := a.Key
		if key == "" {
			if i != 0 || len(keys) == 0 {
				return op, badQuery("query_error")
			}
			key = strings.TrimSuffix(keys[0], "?")
			if key != "id" && s.Operation != "create" && s.Operation != "plan" {
				return op, badQuery("query_error")
			}
		}
		if !allowed[key] {
			return op, badQuery("query_error")
		}
		if _, seen := vals[key]; seen {
			return op, badQuery("query_error")
		}
		vals[key] = a.Value
	}
	for k := range required {
		if _, ok := vals[k]; !ok {
			return op, badQuery("query_error")
		}
	}
	for k, v := range vals {
		var value any = v
		switch k {
		case "active", "blocked", "set", "confirm":
			if v != "true" && v != "false" {
				return op, badQuery("query_error")
			}
			value = v == "true"
		case "skip", "take", "item", "number":
			n, e := strconv.ParseInt(v, 10, 64)
			if e != nil || n < 0 || ((k == "take" || k == "item") && n < 1) || (k == "take" && n > 1000) {
				return op, badQuery("query_error")
			}
			value = n
		case "type":
			if !oneOf(v, "epic story task bug") {
				return op, badQuery("query_error")
			}
		case "status":
			if !oneOf(v, "backlog analysis to-dev development to-review reviewing integrating done closed blocked") {
				return op, badQuery("query_error")
			}
		case "mode":
			if !oneOf(v, "children related") {
				return op, badQuery("query_error")
			}
		case "review":
			if !oneOf(v, "required light none") {
				return op, badQuery("query_error")
			}
		case "task_class":
			if !oneOf(v, "code metadata docs research") {
				return op, badQuery("query_error")
			}
		case "estimate":
			if !oneOf(v, "estimated notEstimated") {
				return op, badQuery("query_error")
			}
		case "scale":
			if v != "fibonacci" {
				return op, badQuery("query_error")
			}
		}
		if k == "scope" && s.Operation == "plan" && strings.HasPrefix(v, "[") {
			if !outsideQuotes(query, '[') {
				return op, badQuery("query_error")
			}
			var ids []string
			if json.Unmarshal([]byte(v), &ids) != nil || len(ids) == 0 {
				return op, badQuery("query_error")
			}
			for _, id := range ids {
				if !validRoot(id) {
					return op, badQuery("query_error")
				}
			}
			value = ids
		} else if (k == "id" || k == "blocked_by" || k == "parent" || (k == "scope" && s.Operation == "plan")) && !validRoot(v) {
			return op, badQuery("query_error")
		}
		if (k == "name" || k == "agent") && strings.TrimSpace(v) == "" {
			return op, badQuery("query_error")
		}
		op.Args[k], _ = json.Marshal(value)
	}
	if s.Operation == "set_details" && len(vals) < 2 {
		return op, badQuery("query_error")
	}
	if s.Operation == "delete" && vals["confirm"] != "true" {
		return op, badQuery("query_error")
	}
	if s.Operation == "set_estimate" {
		_, scale := vals["scale"]
		_, number := vals["number"]
		if (vals["estimate"] == "estimated" && (!scale || !number)) || (vals["estimate"] == "notEstimated" && (scale || number)) {
			return op, badQuery("query_error")
		}
	}
	if projectionPresent(query) && s.Operation != "get" && s.Operation != "list" && s.Operation != "plan" {
		return op, badQuery("query_error")
	}
	for _, field := range s.Fields {
		if preset, ok := presets[field]; ok {
			op.Fields = append(op.Fields, strings.Fields(preset)...)
		} else {
			if !oneOf(field, strings.Join(fields, " ")) {
				return op, badQuery("query_error")
			}
			op.Fields = append(op.Fields, field)
		}
	}
	return op, nil
}
func oneOf(s, values string) bool {
	for _, v := range strings.Fields(values) {
		if s == v {
			return true
		}
	}
	return false
}
func validRoot(s string) bool {
	if s == "" || len(s) > 256 {
		return false
	}
	for _, r := range s {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func scopeLists(s string) (string, error) {
	var out strings.Builder
	quoted, escaped := false, false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if quoted {
			out.WriteByte(ch)
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				quoted = false
			}
			continue
		}
		if ch == '"' {
			quoted = true
			out.WriteByte(ch)
			continue
		}
		if ch != '[' {
			out.WriteByte(ch)
			continue
		}
		prefix := s[:i]
		if !strings.HasPrefix(strings.TrimSpace(s), "plan") || !listPrefix.MatchString(prefix) {
			return "", badQuery("query_error")
		}
		i++
		var ids []string
		for {
			for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n') {
				i++
			}
			if i >= len(s) || s[i] == ']' {
				return "", badQuery("query_error")
			}
			var id string
			if s[i] == '"' {
				begin := i
				i++
				esc := false
				for i < len(s) {
					if !esc && s[i] == '"' {
						break
					}
					if !esc && s[i] == '\\' {
						esc = true
					} else {
						esc = false
					}
					i++
				}
				if i >= len(s) || json.Unmarshal([]byte(s[begin:i+1]), &id) != nil {
					return "", badQuery("query_error")
				}
				i++
			} else {
				begin := i
				for i < len(s) && s[i] != ',' && s[i] != ']' && s[i] != ' ' && s[i] != '\t' && s[i] != '\n' {
					i++
				}
				id = s[begin:i]
			}
			if !validRoot(id) {
				return "", badQuery("query_error")
			}
			ids = append(ids, id)
			for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n') {
				i++
			}
			if i >= len(s) {
				return "", badQuery("query_error")
			}
			if s[i] == ']' {
				break
			}
			if s[i] != ',' {
				return "", badQuery("query_error")
			}
			i++
		}
		b, _ := json.Marshal(ids)
		q, _ := json.Marshal(string(b))
		out.Write(q)
	}
	return out.String(), nil
}

var listPrefix = regexp.MustCompile(`(?:^\s*plan\s*\(\s*|[,(]\s*scope\s*=\s*)$`)

func projectionPresent(s string) bool { return outsideQuotes(s, '{') }
func outsideQuotes(s string, target rune) bool {
	quoted, escaped := false, false
	for _, r := range s {
		if quoted {
			if escaped {
				escaped = false
			} else if r == '\\' {
				escaped = true
			} else if r == '"' {
				quoted = false
			}
			continue
		}
		if r == '"' {
			quoted = true
		} else if r == target {
			return true
		}
	}
	return false
}
