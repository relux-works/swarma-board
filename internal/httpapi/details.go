package httpapi

import (
	"encoding/json"
	"regexp"
)

var safeElementID = regexp.MustCompile(`(?i)^(epic|story|task|bug)-[a-z0-9][a-z0-9-]{0,95}$`)
var detailKeys = map[string]bool{
	"id": true, "element_id": true, "elementId": true, "project_id": true, "parent_id": true,
	"blocked_id": true, "blocker_id": true, "blockerId": true, "ids": true, "members": true,
	"cycle": true, "cycles": true, "chain": true, "links": true, "task_cycles": true,
	"projection_cycles": true, "dependency_problems": true, "reason": true,
}

// Preserve small graph diagnostics without reflecting free-form text or arbitrary
// adapter details. JSON normalization also handles typed slices/structs. Large
// diagnostics remain available through the bounded plan read, not error reflection.
func safeDetails(in map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range in {
		if !detailKeys[key] {
			continue
		}
		b, e := json.Marshal(value)
		if e != nil || len(b) > 64<<10 {
			continue
		}
		var normalized any
		if json.Unmarshal(b, &normalized) != nil {
			continue
		}
		if v, ok := safeDetailValue(key, normalized, 0); ok {
			out[key] = v
		}
	}
	return out
}
func safeDetailValue(key string, value any, depth int) (any, bool) {
	if depth > 6 {
		return nil, false
	}
	switch v := value.(type) {
	case string:
		if key == "reason" {
			switch v {
			case "out-of-scope", "dangling", "resolved-element-has-active-blocker":
				return v, true
			}
			return nil, false
		}
		if key == "project_id" {
			return v, boardID.MatchString(v)
		}
		return v, safeElementID.MatchString(v)
	case []any:
		if len(v) > 128 {
			return nil, false
		}
		out := []any{}
		for _, item := range v {
			sanitized, ok := safeDetailValue(key, item, depth+1)
			if !ok {
				return nil, false
			}
			out = append(out, sanitized)
		}
		return out, true
	case map[string]any:
		out := map[string]any{}
		for k, item := range v {
			if !detailKeys[k] {
				continue
			}
			if sanitized, ok := safeDetailValue(k, item, depth+1); ok {
				out[k] = sanitized
			}
		}
		return out, len(out) > 0
	}
	return nil, false
}
