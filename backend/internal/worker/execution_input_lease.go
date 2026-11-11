package worker

import (
	"context"
	"errors"
	"math"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
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

// Direct runner callers get the same immutable byte/node limits as services.
// The service's snapshot lease already funds the row/document overlap; direct
// fixtures need their own input lease before building the first Document.
func prepareWorkerInputs(ctx context.Context, provider any, rows []*ent.Segment, source string, rounds int, response int64) (context.Context, *service.ExecutionInputLease, error) {
	ctx, err := service.PrepareExecutionInputs(ctx, provider)
	if err != nil {
		return nil, nil, err
	}
	if len(rows) > 65536 || rounds < 0 || response <= 0 || int64(rounds) > math.MaxInt64/response {
		return nil, nil, backend.ErrBuildCapacity
	}
	known := int64(len(source))
	if known > service.DefaultExecutionInputBytes {
		return nil, nil, backend.ErrBuildCapacity
	}
	var issueCount int
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
			issueCount++
			if issueCount > 65536 {
				return nil, nil, backend.ErrBuildCapacity
			}
			n += int64(len(issue.Code)) + int64(len(issue.Message)) + int64(len(issue.Note)) + int64(len(issue.Severity)) + int64(len(issue.Disposition)) + 256
			if issue.Span != nil {
				n += int64(len(issue.Span.MatchedText))
			}
		}
		if n > service.DefaultExecutionInputBytes-known {
			return nil, nil, backend.ErrBuildCapacity
		}
		known += n
	}
	// One finite result per round plus diagnostic fields. The existing codec
	// gives a deterministic worst text/node ceiling, retained through HTTP output.
	resultBytes := int64(rounds) * response
	if known > service.DefaultExecutionInputBytes-resultBytes {
		return nil, nil, backend.ErrBuildCapacity
	}
	creditKnown := known
	containers := int64(rounds + 1 + len(rows))
	if service.ExecutionRowsOwned(ctx, rows) || service.ExecutionBodyOwned(ctx) {
		creditKnown = 0
		containers = int64(rounds + 1)
	}
	credit, err := service.ExecutionInputAllocationBound(creditKnown+resultBytes, containers)
	if err != nil {
		return nil, nil, err
	}
	l, err := service.NewExecutionInputLease(ctx, credit)
	if err != nil {
		return nil, nil, err
	}
	if err = l.RunBuild(ctx, func() error { return service.PrepareExecutionRowMetadata(ctx, rows, known) }); err != nil {
		l.Close()
		return nil, nil, err
	}
	return ctx, l, nil
}
