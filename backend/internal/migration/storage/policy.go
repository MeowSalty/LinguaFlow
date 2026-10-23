package storagemigrate

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// CanonicalPolicy fills only the two quota choices absent in a released policy.
// Existing choices and all other policy fields remain part of the baseline.
func CanonicalPolicy(value string, capacity, logical *int64) (string, bool, error) {
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	var fields map[string]any
	if err := decoder.Decode(&fields); err != nil {
		return "", false, err
	}
	if fields == nil {
		return "", false, ErrChanged
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", false, ErrChanged
	}
	changed := false
	if _, ok := fields["default_space_capacity_bytes"]; !ok {
		fields["default_space_capacity_bytes"] = capacity
		changed = true
	}
	if _, ok := fields["logical_limit_bytes"]; !ok {
		fields["logical_limit_bytes"] = logical
		changed = true
	}
	data, err := json.Marshal(fields)
	return string(data), changed, err
}
