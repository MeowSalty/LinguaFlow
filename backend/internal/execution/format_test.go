package execution

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"
)

func TestExecutionFormatHeadersNeverFillDefaults(t *testing.T) {
	for _, values := range [][3]int{{0, 0, 0}, {1, 1, 1}, {2, 2, 1}, {3, 3, 1}, {4, 4, 0}, {4, 4, 2}, {4, 3, 1}, {-1, 4, 1}, {math.MaxInt, 4, 1}} {
		if err := ValidateExecutionFormat(values[0], values[1], values[2]); !errors.Is(err, ErrUnsupportedExecutionFormat) {
			t.Fatalf("accepted headers %v: %v", values, err)
		}
	}
	if err := ValidateExecutionFormat(4, 4, 1); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []any{nil, "4", 4.5, math.Inf(1), math.NaN(), json.Number("4.0"), []int{4}} {
		config := map[string]any{"schema_version": bad, "defaults_version": 4, "response_format_version": 1}
		if err := ValidateExecutionConfigFormat(config); !errors.Is(err, ErrUnsupportedExecutionFormat) {
			t.Fatalf("accepted malformed schema %v: %v", bad, err)
		}
	}
	config := map[string]any{"schema_version": json.Number("4"), "defaults_version": int64(4), "response_format_version": float64(1), "history": "retained"}
	before := map[string]any{"schema_version": json.Number("4"), "defaults_version": int64(4), "response_format_version": float64(1), "history": "retained"}
	if err := ValidateExecutionConfigFormat(config); err != nil || !reflect.DeepEqual(config, before) {
		t.Fatalf("format check changed data: %v", err)
	}
}

func TestResolveCannotConvertAnOldFrozenExecution(t *testing.T) {
	for _, version := range []int{1, 2, 3} {
		input := validExecutionInput()
		input.SchemaVersion, input.DefaultsVersion = version, version
		if _, err := Resolve(input); !errors.Is(err, ErrUnsupportedExecutionFormat) {
			t.Fatalf("converted old snapshot %d: %v", version, err)
		}
	}
	input := validExecutionInput()
	input.ResponseFormatVersion = 99
	if _, err := Resolve(input); !errors.Is(err, ErrUnsupportedExecutionFormat) {
		t.Fatalf("unknown response format overwritten: %v", err)
	}
}
