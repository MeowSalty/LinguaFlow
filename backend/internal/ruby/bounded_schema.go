package ruby

import (
	"errors"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

var alignmentSchemaSnapshot = AlignmentJSONSchema()
var alignmentBatchSchemaSnapshot = AlignmentBatchJSONSchema()

func AlignmentJSONSchemaBounded(b *backend.BuildBounds) (map[string]any, error) {
	var out map[string]any
	err := b.CloneInto(alignmentSchemaSnapshot, &out)
	return out, err
}
func AlignmentJSONSchemaForProtocolBounded(b *backend.BuildBounds, protocol int) (map[string]any, error) {
	if protocol != ProtocolV2 {
		return nil, errors.New("unsupported final ruby protocol")
	}
	return AlignmentJSONSchemaBounded(b)
}
func AlignmentBatchJSONSchemaBounded(b *backend.BuildBounds) (map[string]any, error) {
	var out map[string]any
	err := b.CloneInto(alignmentBatchSchemaSnapshot, &out)
	return out, err
}
