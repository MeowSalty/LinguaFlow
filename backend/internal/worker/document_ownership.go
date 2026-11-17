package worker

import (
	"context"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

// Each resource has its own document lease even when an Admission is shared by
// several resources. The callback resolves the current canonical lease after a
// reload, and the Admission moves only that slot's final private backing.
func withWorkerDocumentOwnership(ctx context.Context) context.Context {
	return pipeline.WithDocumentAllocationOwner(ctx, func(buildCtx context.Context, slot int, owner string, owned pipeline.DocumentAllocationPlan, borrowed uint8, install func()) error {
		lease := service.ExecutionDocumentLease(buildCtx)
		runtime := pipeline.ExecutionRuntimeFromContext(buildCtx)
		if lease == nil || runtime == nil || runtime.Admission == nil {
			return backend.ErrBuildCapacity
		}
		return lease.ReplaceOwnedSlot(slot, service.ExecutionDocumentAllocation{
			TargetBytes: owned.TargetBytes, IssuesBytes: owned.IssuesBytes,
			MetaBytes: owned.MetaBytes, ProtectedBytes: owned.ProtectedBytes,
		}, borrowed, runtime.Admission, owner, install)
	})
}
