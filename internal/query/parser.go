// Package query implements the frozen product DSL, without runtime registration.
package query

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"

	aq "github.com/relux-works/skill-agent-facing-api/agentquery"
	"github.com/relux-works/swarma-board/internal/contract"
)

var Statuses = []string{"backlog", "analysis", "to-dev", "development", "to-review", "reviewing", "integrating", "done", "closed", "blocked"}
var Types = []string{"epic", "story", "task", "bug"}
var Fields = []string{"id", "type", "name", "title", "status", "review", "task_class", "estimate", "parent", "assignee", "description", "scope", "ac", "blockedBy", "blocks", "derivedBlockedBy", "derivedBlocks", "checklist", "notes", "created", "updated", "isBlocked", "isAdvisoryBlocked", "children", "preconditionResources", "outcomeResources"}
var presets = map[string][]string{
	"minimal": {"id", "status"}, "default": {"id", "name", "status"}, "overview": {"id", "name", "status", "assignee", "parent"},
	"planning": {"id", "name", "status", "blockedBy", "blocks", "derivedBlockedBy", "derivedBlocks", "parent"},
	"progress": {"id", "status", "review", "task_class", "estimate", "checklist", "notes", "blockedBy", "derivedBlockedBy", "isBlocked", "isAdvisoryBlocked"},
	"full":     Fields,
}

type spec struct {
	first           string
	args            map[string]string
	required        []string
	read, selectors bool
}

func args(s string) map[string]string {
	r := map[string]string{}
	for _, p := range strings.Fields(s) {
		kv := strings.Split(p, ":")
		r[kv[0]] = kv[1]
	}
	return r
}

var specs = map[string]spec{
	"get":                   {"id", args("id:string"), []string{"id"}, true, true},
	"list":                  {"", args("type:type status:status parent:string assignee:string blocked:bool skip:int take:int"), nil, true, true},
	"count":                 {"", args("type:type status:status parent:string assignee:string blocked:bool"), nil, true, false},
	"summary":               {"", args(""), nil, true, false},
	"plan":                  {"scope", args("scope:scope mode:mode active:bool"), nil, true, true},
	"create":                {"type", args("type:type name:string parent:string description:string scope:string ac:string review:review task_class:class"), []string{"type", "name"}, false, false},
	"set_details":           {"id", args("id:string title:string description:string scope:string ac:string review:review task_class:class"), []string{"id"}, false, false},
	"rename":                {"id", args("id:string name:string"), []string{"id", "name"}, false, false},
	"assign":                {"id", args("id:string agent:string"), []string{"id", "agent"}, false, false},
	"unassign":              {"id", args("id:string"), []string{"id"}, false, false},
	"set_status":            {"id", args("id:string status:status"), []string{"id", "status"}, false, false},
	"set_estimate":          {"id", args("id:string estimate:estimate scale:scale number:int"), []string{"id", "estimate"}, false, false},
	"link":                  {"id", args("id:string blocked_by:string"), []string{"id", "blocked_by"}, false, false},
	"unlink":                {"id", args("id:string blocked_by:string"), []string{"id", "blocked_by"}, false, false},
	"check_item":            {"id", args("id:string item:int"), []string{"id", "item"}, false, false},
	"uncheck_item":          {"id", args("id:string item:int"), []string{"id", "item"}, false, false},
	"add_checklist_item":    {"id", args("id:string text:string"), []string{"id", "text"}, false, false},
	"remove_checklist_item": {"id", args("id:string item:int"), []string{"id", "item"}, false, false},
	"set_notes":             {"id", args("id:string text:string set:bool"), []string{"id", "text"}, false, false},
	"set_parent":            {"id", args("id:string parent:string"), []string{"id", "parent"}, false, false},
	"delete":                {"id", args("id:string confirm:bool"), []string{"id", "confirm"}, false, false},
}

func Error(code, message string) error { return &contract.Error{Code: code, Message: message} }
func Contains(a []string, s string) bool {
	for _, v := range a {
		if v == s {
			return true
		}
	}
	return false
}
func IsRead(name string) bool { return specs[name].read }
func Known(name string) bool  { _, ok := specs[name]; return ok }

type Parser struct{}

func New() *Parser { return &Parser{} }

var _ contract.Parser = (*Parser)(nil)

// Parse uses agentquery's single-statement AST. The pinned parser lacks square
// list tokens, so a narrow lexical bridge maps bracket lists to AST values.
// No operations or selectors are registered from the legacy product.
func (*Parser) Parse(input string) (contract.Operation, error) {
	fail := func(code string) (contract.Operation, error) {
		return contract.Operation{}, Error(code, "Query rejected")
	}
	if len(input) > 8<<20 || !utf8.ValidString(input) {
		return fail("query_error")
	}
	normalized, lists, projection, batch, err := bridge(input)
	if err != nil {
		return fail("query_error")
	}
	if batch {
		return fail("unsupported_batch")
	}
	tree, err := aq.Parse(normalized, nil)
	if err != nil {
		return fail("query_error")
	}
	if len(tree.Statements) != 1 {
		return fail("unsupported_batch")
	}
	stmt := tree.Statements[0]
	sp, ok := specs[stmt.Operation]
	if !ok {
		return fail("unsupported_operation")
	}
	if projection && !sp.selectors {
		return fail("query_error")
	}
	op := contract.Operation{Name: stmt.Operation, Args: map[string]json.RawMessage{}, Fields: stmt.Fields}
	positional := false
	for i, a := range stmt.Args {
		key := a.Key
		if key == "" {
			if positional || sp.first == "" || i != 0 {
				return fail("query_error")
			}
			key = sp.first
			positional = true
		}
		if _, ok := op.Args[key]; ok {
			return fail("query_error")
		}
		kind, ok := sp.args[key]
		if !ok {
			return fail("query_error")
		}
		var value any = a.Value
		if list, ok := lists[a.Value]; ok {
			if kind != "scope" {
				return fail("query_error")
			}
			value = list
		} else {
			switch kind {
			case "bool":
				if a.Value != "true" && a.Value != "false" {
					return fail("query_error")
				}
				value = a.Value == "true"
			case "int":
				n, e := strconv.Atoi(a.Value)
				if e != nil {
					return fail("query_error")
				}
				value = n
			}
		}
		op.Args[key], _ = json.Marshal(value)
	}
	return Validate(op)
}

// Validate also protects trusted callers that construct Operation directly.
// It returns fresh normalized arguments so a caller cannot mutate a callback's input.
func Validate(op contract.Operation) (contract.Operation, error) {
	bad := func() (contract.Operation, error) {
		return contract.Operation{}, Error("query_error", "Invalid operation arguments or selectors")
	}
	sp, ok := specs[op.Name]
	if !ok {
		return contract.Operation{}, Error("unsupported_operation", "Unsupported operation")
	}
	out := contract.Operation{Name: op.Name, Args: map[string]json.RawMessage{}}
	for k, raw := range op.Args {
		kind, ok := sp.args[k]
		if !ok {
			return bad()
		}
		var v any
		switch kind {
		case "bool":
			var b bool
			if string(raw) == "null" || json.Unmarshal(raw, &b) != nil {
				return bad()
			}
			v = b
		case "int":
			var n int
			if string(raw) == "null" || json.Unmarshal(raw, &n) != nil {
				return bad()
			}
			v = n
		case "scope":
			var s string
			if json.Unmarshal(raw, &s) == nil && string(raw) != "null" {
				if strings.TrimSpace(s) == "" {
					return bad()
				}
				v = s
			} else {
				var ids []string
				if json.Unmarshal(raw, &ids) != nil || len(ids) == 0 {
					return bad()
				}
				for _, id := range ids {
					if strings.TrimSpace(id) == "" {
						return bad()
					}
				}
				v = ids
			}
		default:
			var s string
			if string(raw) == "null" || json.Unmarshal(raw, &s) != nil {
				return bad()
			}
			v = s
			var allowed []string
			switch kind {
			case "type":
				allowed = Types
			case "status":
				allowed = Statuses
			case "mode":
				allowed = []string{"children", "related"}
			case "review":
				allowed = []string{"required", "light", "none"}
			case "class":
				allowed = []string{"code", "metadata", "docs", "research"}
			case "estimate":
				allowed = []string{"estimated", "notEstimated"}
			case "scale":
				allowed = []string{"fibonacci"}
			}
			if allowed != nil && !Contains(allowed, s) {
				return bad()
			}
			if Contains([]string{"id", "name", "agent", "blocked_by", "parent"}, k) && strings.TrimSpace(s) == "" {
				return bad()
			}
		}
		out.Args[k], _ = json.Marshal(v)
	}
	for _, k := range sp.required {
		if _, ok := out.Args[k]; !ok {
			return bad()
		}
	}
	if op.Name == "set_details" && len(out.Args) < 2 {
		return bad()
	}
	if op.Name == "list" {
		if _, ok := out.Args["skip"]; !ok {
			out.Args["skip"] = json.RawMessage("0")
		}
		if _, ok := out.Args["take"]; !ok {
			out.Args["take"] = json.RawMessage("50")
		}
		if Int(out, "skip") < 0 || Int(out, "take") < 1 || Int(out, "take") > 1000 {
			return bad()
		}
	}
	if _, ok := out.Args["item"]; ok && Int(out, "item") < 1 {
		return bad()
	}
	if op.Name == "delete" && !Bool(out, "confirm") {
		return bad()
	}
	if op.Name == "set_estimate" {
		if String(out, "estimate") == "estimated" {
			if String(out, "scale") != "fibonacci" {
				return bad()
			}
			if _, ok := out.Args["number"]; !ok || Int(out, "number") < 0 {
				return bad()
			}
		} else {
			if _, ok := out.Args["scale"]; ok {
				return bad()
			}
			if _, ok := out.Args["number"]; ok {
				return bad()
			}
		}
	}
	if !sp.selectors && len(op.Fields) > 0 {
		return bad()
	}
	if sp.selectors {
		f, e := Expand(op.Fields)
		if e != nil {
			return bad()
		}
		out.Fields = f
	}
	return out, nil
}
func String(op contract.Operation, key string) string {
	var v string
	_ = json.Unmarshal(op.Args[key], &v)
	return v
}
func Int(op contract.Operation, key string) int {
	var v int
	_ = json.Unmarshal(op.Args[key], &v)
	return v
}
func Bool(op contract.Operation, key string) bool {
	var v bool
	_ = json.Unmarshal(op.Args[key], &v)
	return v
}
func Expand(names []string) ([]string, error) {
	if len(names) == 0 {
		names = []string{"default"}
	}
	out := []string{}
	seen := map[string]bool{}
	for _, name := range names {
		values, ok := presets[name]
		if !ok {
			if !Contains(Fields, name) {
				return nil, Error("query_error", "Unknown selector")
			}
			values = []string{name}
		}
		for _, v := range values {
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	return out, nil
}

func bridge(input string) (string, map[string][]string, bool, bool, error) {
	lists := map[string][]string{}
	var b strings.Builder
	quote, escape, projection := false, false, false
	semicolon := -1
	for i := 0; i < len(input); i++ {
		c := input[i]
		if quote {
			b.WriteByte(c)
			if escape {
				escape = false
			} else if c == '\\' {
				escape = true
			} else if c == '"' {
				quote = false
			}
			continue
		}
		if c == '"' {
			quote = true
			b.WriteByte(c)
			continue
		}
		if c == '{' {
			projection = true
		}
		if c == ';' {
			if semicolon < 0 {
				semicolon = i
			}
		} else if semicolon >= 0 && c != ' ' && c != '\n' && c != '\t' && c != '\r' {
			return "", lists, projection, true, nil
		}
		if c != '[' {
			b.WriteByte(c)
			continue
		}
		j := i + 1
		q, e := false, false
		for ; j < len(input); j++ {
			d := input[j]
			if q {
				if e {
					e = false
				} else if d == '\\' {
					e = true
				} else if d == '"' {
					q = false
				}
			} else if d == '"' {
				q = true
			} else if d == ']' {
				break
			} else if d == '[' {
				return "", nil, false, false, Error("query_error", "Invalid list")
			}
		}
		if j == len(input) {
			return "", nil, false, false, Error("query_error", "Invalid list")
		}
		// Reuse the same AST grammar for the list's scalar members.
		t, err := aq.Parse("items("+input[i+1:j]+")", nil)
		if err != nil || len(t.Statements) != 1 || len(t.Statements[0].Args) == 0 {
			return "", nil, false, false, Error("query_error", "Invalid list")
		}
		values := []string{}
		for _, a := range t.Statements[0].Args {
			if a.Key != "" || strings.TrimSpace(a.Value) == "" {
				return "", nil, false, false, Error("query_error", "Invalid list")
			}
			values = append(values, a.Value)
		}
		marker := "__swarma_list_" + strconv.Itoa(len(lists))
		if strings.Contains(input, marker) {
			return "", nil, false, false, Error("query_error", "Reserved list token")
		}
		lists[marker] = values
		b.WriteString(marker)
		i = j
	}
	return b.String(), lists, projection, false, nil
}
