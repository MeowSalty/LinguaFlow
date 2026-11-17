package worker

import (
	"encoding/json"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"unsafe"
)

// Source metadata keeps encoding/json's historical float64 representation;
// the bounded decoder initially uses json.Number to avoid premature expansion.
func canonicalMetadataNumbers(b *backend.BuildBounds, v any) (any, error) {
	switch value := v.(type) {
	case json.Number:
		// A float64 stored in an interface may allocate its own scalar backing.
		if err := b.ReserveAllocation(1, int64(unsafe.Sizeof(float64(0)))); err != nil {
			return nil, err
		}
		n, err := value.Float64()
		return n, err
	case map[string]any:
		for k, child := range value {
			next, err := canonicalMetadataNumbers(b, child)
			if err != nil {
				return nil, err
			}
			value[k] = next
		}
	case []any:
		for i, child := range value {
			next, err := canonicalMetadataNumbers(b, child)
			if err != nil {
				return nil, err
			}
			value[i] = next
		}
	}
	return v, nil
}
