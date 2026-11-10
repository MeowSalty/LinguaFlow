package execution

import (
	"encoding/json"
	"errors"
	"fmt"
)

const ResponseFormatVersion = 1

var ErrUnsupportedExecutionFormat = errors.New("unsupported execution format: preserve this task as history; finish it using the previous version and its pre-upgrade database, or create a new task")

// ValidateExecutionFormat checks execution headers before any claim or business
// write. Reading history and explicit cancellation/deletion do not use this gate.
func ValidateExecutionFormat(schema, defaults, response int) error {
	if schema != SnapshotSchemaVersion || defaults != SnapshotDefaultsVersion || response != ResponseFormatVersion {
		return fmt.Errorf("%w (schema=%d defaults=%d response=%d; required=%d/%d/%d)", ErrUnsupportedExecutionFormat, schema, defaults, response, SnapshotSchemaVersion, SnapshotDefaultsVersion, ResponseFormatVersion)
	}
	return nil
}

// ValidateExecutionConfigFormat reads only fixed headers; it does not clone a
// snapshot, fill defaults, or decode legacy candidate payloads.
func ValidateExecutionConfigFormat(config map[string]any) error {
	maximumHeader := max(SnapshotSchemaVersion, SnapshotDefaultsVersion, ResponseFormatVersion)
	read := func(key string) int {
		switch v := config[key].(type) {
		case int:
			return v
		case int64:
			if v >= 0 && v <= int64(maximumHeader) {
				return int(v)
			}
		case float64:
			if v >= 0 && v <= float64(maximumHeader) && v == float64(int(v)) {
				return int(v)
			}
		case json.Number:
			if n, err := v.Int64(); err == nil && n >= 0 && n <= int64(maximumHeader) {
				return int(n)
			}
		}
		return 0
	}
	return ValidateExecutionFormat(read("schema_version"), read("defaults_version"), read("response_format_version"))
}
