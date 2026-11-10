package worker

import (
	"context"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

// applyStoredMemoryResult decodes and applies a sealed stored response within
// the short permit. The resulting issue graph belongs to Document before the
// permit is released; database callbacks continue to consume the opaque bytes.
func applyStoredMemoryResult(ctx context.Context, result pipeline.BatchResult, apply func(*backend.BuildBounds, pipeline.BatchResult) error) error {
	if result.PreparedPayload == nil {
		return apply(nil, result)
	}
	runtime := pipeline.ExecutionRuntimeFromContext(ctx)
	limit, input := backend.DefaultMaxResponseBytes, int64(len(result.PreparedPayload))
	if runtime != nil {
		limit = runtime.ResponseBytes
		input = runtime.HandoffBytes
	}
	build := func() error {
		b, err := backend.NewBuildBounds(limit, input)
		if err != nil {
			return err
		}
		var full pipeline.BatchResult
		if err = b.DecodeJSONLimited(result.PreparedPayload, &full, limit); err != nil {
			return err
		}
		for i := range full.Segments {
			for j := range full.Segments[i].Issues {
				if full.Segments[i].Issues[j].Disposition == "" {
					full.Segments[i].Issues[j].Disposition = qa.DispositionPending
				}
			}
		}
		return apply(b, full)
	}
	if runtime != nil {
		return runtime.Run(ctx, build)
	}
	return build()
}
