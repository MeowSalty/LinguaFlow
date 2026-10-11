package worker

import (
	"context"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

type jobWriteScope struct {
	jobID int
	epoch int64
}
type jobWriteScopeKey struct{}

// Legacy local/QA callbacks share the Job arbitration point with candidates.
// A cancellation committed before this transaction rejects even a late callback
// whose Go context has not yet observed the cancellation notification.
func withJobResourceTranslation(ctx context.Context, client *ent.Client, resourceID int, generation int64, write func(*ent.Client) error) error {
	scope, ok := ctx.Value(jobWriteScopeKey{}).(jobWriteScope)
	if !ok {
		return service.WithResourceTranslation(ctx, client, resourceID, generation, write)
	}
	return workstate.Transaction(ctx, client, func(tx *ent.Client) error {
		if err := workstate.LockJob(ctx, tx, scope.jobID); err != nil {
			return err
		}
		row, err := tx.Job.Get(ctx, scope.jobID)
		if err != nil {
			return err
		}
		if row.RetryEpoch != scope.epoch || (row.Status != "running" && row.Status != "pausing" && row.Status != "pending") {
			return workstate.ErrStopped
		}
		if err := service.AdvanceTranslationGeneration(ctx, tx, resourceID, generation); err != nil {
			return err
		}
		return write(tx)
	})
}
