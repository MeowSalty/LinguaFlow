package persistence

import "context"

// MetadataFromContext returns the factual operation identity by value. The
// execution writer and its storage receipt use the same request identity.
func MetadataFromContext(ctx context.Context) Metadata {
	m, _ := ctx.Value(metadataKey{}).(Metadata)
	return m
}
