package pipeline

import "github.com/MeowSalty/LinguaFlow/backend/internal/backend"

// SealStoredBatchResult is called before leaving the parse/build permit.
func SealStoredBatchResult(b *backend.BuildBounds, r *BatchResult, limit int64) error {
	for i := range r.Segments {
		r.Segments[i].SourceText = ""
		r.Segments[i].TargetText = ""
		r.Segments[i].Meta = nil
		r.Segments[i].Protected = nil
	}
	payload, err := b.EncodeJSON(*r, limit)
	if err != nil {
		return err
	}
	r.Segments = nil
	r.PreparedPayload = payload
	return nil
}
