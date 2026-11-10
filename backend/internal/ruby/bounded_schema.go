package ruby

import (
	"errors"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func AlignmentJSONSchemaBounded(b *backend.BuildBounds) (map[string]any, error) {
	if e := b.ReserveNodes(64, 1024); e != nil {
		return nil, e
	}
	return AlignmentJSONSchema(), nil
}
func AlignmentJSONSchemaForProtocolBounded(b *backend.BuildBounds, protocol int) (map[string]any, error) {
	if protocol != ProtocolV2 {
		return nil, errors.New("unsupported final ruby protocol")
	}
	return AlignmentJSONSchemaBounded(b)
}
func AlignmentBatchJSONSchemaBounded(b *backend.BuildBounds) (map[string]any, error) {
	if e := b.ReserveNodes(128, 1024); e != nil {
		return nil, e
	}
	return AlignmentBatchJSONSchema(), nil
}
