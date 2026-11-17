package worker

import (
	"context"
	"errors"
	"unsafe"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func isExecutionCapacity(err error) bool {
	return errors.Is(err, backend.ErrBuildCapacity) || errors.Is(err, resourcebudget.ErrCapacity) || errors.Is(err, backend.ErrNoCapacityDrain)
}

func (r *PreviewRunner) ExecutionInputBudget() (*resourcebudget.RootBudget, func(context.Context, int) error, int64) {
	return r.factory.rootBudget, r.factory.prepareActor, r.factory.responseHandoffBytes
}
func (r *RevisionPreviewRunner) ExecutionInputBudget() (*resourcebudget.RootBudget, func(context.Context, int) error, int64) {
	return r.factory.rootBudget, r.factory.prepareActor, r.factory.responseHandoffBytes
}
func (r *QuickTranslateRunner) ExecutionInputBudget() (*resourcebudget.RootBudget, func(context.Context, int) error, int64) {
	return r.factory.rootBudget, r.factory.prepareActor, r.factory.responseHandoffBytes
}

// Direct runner callers get the same input envelope and optional allocation policy.
// The service's snapshot lease already funds the row/document overlap; direct
// fixtures need their own input lease before building the first Document.
func prepareWorkerInputs(ctx context.Context, provider any, rows []*ent.Segment, source string, rounds int, response int64) (context.Context, *service.ExecutionInputLease, error) {
	ctx, err := service.PrepareExecutionInputs(ctx, provider)
	if err != nil {
		return nil, nil, err
	}
	ctx = withWorkerDocumentOwnership(ctx)
	if rounds < 0 || response <= 0 {
		return nil, nil, backend.ErrBuildCapacity
	}
	known := int64(len(source))
	if known > service.DefaultExecutionInputBytes {
		return nil, nil, backend.ErrBuildCapacity
	}
	for _, row := range rows {
		if row == nil {
			return nil, nil, backend.ErrBuildCapacity
		}
		n := int64(len(row.SourceText)) + int64(len(row.Status))
		if row.TargetText != nil {
			n += int64(len(*row.TargetText))
		}
		if row.Meta != nil {
			n += int64(len(*row.Meta))
		}
		for _, issue := range row.QualityIssues {
			n += int64(len(issue.Code)) + int64(len(issue.Message)) + int64(len(issue.Note)) + int64(len(issue.Severity)) + int64(len(issue.Disposition))
			if issue.Span != nil {
				n += int64(len(issue.Span.MatchedText))
			}
		}
		if n > service.DefaultExecutionInputBytes-known {
			return nil, nil, backend.ErrBuildCapacity
		}
		known += n
	}
	creditKnown := known
	containers := int64(rounds + 1 + len(rows))
	if service.ExecutionRowsOwned(ctx, rows) || service.ExecutionBodyOwned(ctx) {
		creditKnown = 0
		containers = int64(rounds + 1)
	}
	credit, err := service.ExecutionInputAllocationBound(creditKnown, containers)
	if err != nil {
		return nil, nil, err
	}
	// These are the first explicit arrays the runner constructs. A later rebuild
	// is accounted when it actually happens, not as a possible second document.
	count := int64(max(1, len(rows)))
	for _, item := range []struct{ count, size int64 }{
		{count, int64(unsafe.Sizeof(pipeline.SegmentInput{}))},
		{count, int64(unsafe.Sizeof(pipeline.Segment{}))},
		{1, int64(unsafe.Sizeof(pipeline.Document{}))},
	} {
		n, err := backend.AllocationBytes(item.count, item.size)
		if err != nil {
			return nil, nil, err
		}
		credit, err = backend.CheckedAdd(credit, n)
		if err != nil {
			return nil, nil, err
		}
	}
	if !service.ExecutionRowsOwned(ctx, rows) {
		n, err := backend.AllocationBytes(int64(cap(rows)), int64(unsafe.Sizeof(*new(*ent.Segment))))
		if err != nil {
			return nil, nil, err
		}
		credit, err = backend.CheckedAdd(credit, n)
		if err != nil {
			return nil, nil, err
		}
	}
	l, err := service.NewExecutionInputLease(ctx, credit)
	if err != nil {
		return nil, nil, err
	}
	if err = l.RunBuild(ctx, func() error { return service.PrepareExecutionRowMetadata(ctx, rows, l) }); err != nil {
		l.Close()
		return nil, nil, err
	}
	if err = l.InitializeBorrowedDocumentSlots(ctx, max(1, len(rows))); err != nil {
		l.Close()
		return nil, nil, err
	}
	service.ReplaceExecutionDocumentLease(ctx, l)
	return ctx, l, nil
}
