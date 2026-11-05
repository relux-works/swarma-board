package transfer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/relux-works/swarma-board/internal/contract"
	"io"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Decode in three steps: exact UTF-8/Unicode and duplicate-key validation,
// exact case-sensitive schema/required-field validation, then typed decoding.
func strictJSON(data []byte, dst any) error {
	if !utf8.Valid(data) || !validUnicodeEscapes(data) {
		return invalid("invalid JSON Unicode")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := jsonValueWithLimits(dec, 0, true); err != nil {
		return invalid("invalid or duplicate JSON value")
	}
	if _, err := dec.Token(); err != io.EOF {
		return invalid("trailing JSON data")
	}
	if err := jsonShape(data, reflect.TypeOf(dst).Elem()); err != nil {
		return invalid("JSON schema: " + err.Error())
	}
	dec = json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return invalid("invalid typed JSON value")
	}
	return nil
}
func jsonValue(d *json.Decoder, depth int) error { return jsonValueWithLimits(d, depth, false) }
func jsonValueWithLimits(d *json.Decoder, depth int, boundedRootArrays bool) error {
	if depth > 64 {
		return fmt.Errorf("JSON nesting limit")
	}
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
		keys := map[string]bool{}
		for d.More() {
			k, e := d.Token()
			if e != nil {
				return e
			}
			s, ok := k.(string)
			if !ok || keys[s] {
				return fmt.Errorf("duplicate key")
			}
			keys[s] = true
			if e = jsonValueWithLimits(d, depth+1, boundedRootArrays); e != nil {
				return e
			}
		}
		t, e = d.Token()
		if e != nil || t != json.Delim('}') {
			return fmt.Errorf("object end")
		}
	case '[':
		count := 0
		for d.More() {
			count++
			if boundedRootArrays && depth == 1 && count > MaxElements {
				return fmt.Errorf("JSON array count limit")
			}
			if e = jsonValueWithLimits(d, depth+1, boundedRootArrays); e != nil {
				return e
			}
		}
		t, e = d.Token()
		if e != nil || t != json.Delim(']') {
			return fmt.Errorf("array end")
		}
	default:
		return fmt.Errorf("unexpected delimiter")
	}
	return nil
}
func jsonShape(data []byte, t reflect.Type) error {
	data = bytes.TrimSpace(data)
	if t.Kind() == reflect.Pointer {
		if bytes.Equal(data, []byte("null")) {
			return nil
		}
		return jsonShape(data, t.Elem())
	}
	if bytes.Equal(data, []byte("null")) {
		return fmt.Errorf("null nonnullable value")
	}
	if t == reflect.TypeOf(time.Time{}) {
		var s string
		if json.Unmarshal(data, &s) != nil || !strings.HasSuffix(s, "Z") {
			return fmt.Errorf("timestamp must be UTC RFC3339")
		}
		if _, e := time.Parse(time.RFC3339, s); e != nil {
			return fmt.Errorf("invalid timestamp")
		}
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		var m map[string]json.RawMessage
		if len(data) == 0 || data[0] != '{' || json.Unmarshal(data, &m) != nil {
			return fmt.Errorf("expected object")
		}
		fields := map[string]reflect.StructField{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := f.Tag.Get("json")
			name := strings.Split(tag, ",")[0]
			if name == "" {
				name = f.Name
			}
			fields[name] = f
			v, ok := m[name]
			if !ok {
				if !strings.Contains(tag, ",omitempty") {
					return fmt.Errorf("missing required field %s", name)
				}
				continue
			}
			if e := jsonShape(v, f.Type); e != nil {
				return e
			}
		}
		if t == reflect.TypeOf(contract.Estimate{}) {
			var kind string
			_ = json.Unmarshal(m["kind"], &kind)
			if kind == "notEstimated" {
				if _, exists := m["scale"]; exists {
					return fmt.Errorf("notEstimated forbids scale")
				}
			}
		}
		if t == reflect.TypeOf(contract.Element{}) {
			for _, key := range []string{"review", "task_class"} {
				if raw, exists := m[key]; exists {
					var value string
					if json.Unmarshal(raw, &value) != nil || value == "" {
						return fmt.Errorf("invalid optional descriptive enum")
					}
				}
			}
		}
		for key := range m {
			if _, ok := fields[key]; !ok {
				return fmt.Errorf("unknown field")
			}
		}
	case reflect.Slice:
		var a []json.RawMessage
		if len(data) == 0 || data[0] != '[' || json.Unmarshal(data, &a) != nil {
			return fmt.Errorf("expected array")
		}

		for _, v := range a {
			if e := jsonShape(v, t.Elem()); e != nil {
				return e
			}
		}
	}
	return nil
}
func validUnicodeEscapes(data []byte) bool {
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		i++
		if i >= len(data) {
			return false
		}
		if data[i] != 'u' {
			continue
		}
		if i+4 >= len(data) {
			return false
		}
		n, e := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		if e != nil {
			return false
		}
		i += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return false
		}
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
				return false
			}
			lo, e := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
			if e != nil || lo < 0xdc00 || lo > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}
