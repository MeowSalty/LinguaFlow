package repair

import (
	"bytes"
	"encoding/json"
)

// preserveRawRubyFields keeps annotation JSON intact until the Ruby validator
// runs. A map round trip would silently erase duplicate id/base/kind members.
// The surrounding envelope retains its generic shape for structural repair.
func preserveRawRubyFields(body []byte, object map[string]any) {
	if !containsRubyFields(object) {
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return
	}
	fields := make(map[string]json.RawMessage)
	duplicateRuby := false
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return
		}
		if _, exists := fields[key]; exists && key == "ruby_output" {
			duplicateRuby = true
		}
		fields[key] = value
	}
	for key, value := range fields {
		if key == "ruby_output" {
			if duplicateRuby {
				// Optional annotations cannot invalidate an otherwise usable main
				// response, but an ambiguous envelope cannot establish any facts.
				object[key] = nil
			} else {
				object[key] = rawRubyValue(value)
			}
		} else if nested, ok := object[key].(map[string]any); ok {
			preserveRawRubyFields(value, nested)
		}
	}
}

func containsRubyFields(object map[string]any) bool {
	for key, value := range object {
		if key == "ruby_output" {
			return true
		}
		if nested, ok := value.(map[string]any); ok && containsRubyFields(nested) {
			return true
		}
	}
	return false
}

func rawRubyValue(value json.RawMessage) any {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var rows []json.RawMessage
		if json.Unmarshal(trimmed, &rows) == nil {
			// Array envelope shape checks still see []any. Each row marshals as
			// its original JSON, including malformed duplicate object members.
			entries := make([]any, len(rows))
			for i, row := range rows {
				entries[i] = row
			}
			return entries
		}
	}
	return value
}
