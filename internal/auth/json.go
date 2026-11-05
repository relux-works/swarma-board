package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/relux-works/swarma-board/internal/contract"
	"io"
	"sort"
)

func sortProjects(p []contract.Project) {
	sort.Slice(p, func(i, j int) bool { return p[i].ID < p[j].ID })
}

// Encoding/json alone accepts duplicate keys, including escaped equivalents.
func uniqueJSON(b []byte) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var value func() error
	value = func() error {
		t, e := d.Token()
		if e != nil {
			return e
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return e
				}
				s, ok := k.(string)
				if !ok || seen[s] {
					return fmt.Errorf("duplicate key")
				}
				seen[s] = true
				if e = value(); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if e = value(); e != nil {
					return e
				}
			}
		default:
			return fmt.Errorf("invalid delimiter")
		}
		_, e = d.Token()
		return e
	}
	if e := value(); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}

func exactObject(raw json.RawMessage, keys ...string) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m == nil || len(m) != len(keys) {
		return nil, fmt.Errorf("invalid fields")
	}
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			return nil, fmt.Errorf("missing field")
		}
	}
	return m, nil
}
func validateFixtureJSON(data []byte) error {
	if e := uniqueJSON(data); e != nil {
		return e
	}
	root, e := exactObject(data, "schema_version", "synthetic", "projects", "accounts", "tokens", "grants")
	if e != nil {
		return e
	}
	for _, key := range []string{"schema_version", "synthetic"} {
		if bytes.Equal(bytes.TrimSpace(root[key]), []byte("null")) {
			return fmt.Errorf("null scalar")
		}
	}
	shapes := map[string][]string{"projects": {"id", "name", "description"}, "accounts": {"id", "disabled"}, "tokens": {"sha256", "account_id", "revoked", "expires_at"}, "grants": {"account_id", "project_id", "role"}}
	for collection, keys := range shapes {
		var rows []json.RawMessage
		if json.Unmarshal(root[collection], &rows) != nil || rows == nil {
			return fmt.Errorf("invalid array")
		}
		for _, row := range rows {
			m, e := exactObject(row, keys...)
			if e != nil {
				return e
			}
			for k, v := range m {
				if k != "expires_at" && bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
					return fmt.Errorf("null scalar")
				}
			}
		}
	}
	return nil
}
