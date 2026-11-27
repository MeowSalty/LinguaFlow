package worker

import (
	"context"
	"errors"
	"fmt"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

type stagedRoundResidual struct {
	rowID       int
	hasResidual bool
}

func (r *JobRunner) reconcileStagedResiduals(ctx context.Context, jobID, resourceID int, staged map[string][]stagedRoundResidual, residualByMode map[string]roundResidual) error {
	for _, mode := range []string{pipeline.RoundModeTranslate, pipeline.RoundModeRevise} {
		rounds := staged[mode]
		if len(rounds) == 0 {
			continue
		}
		var combined roundResidual
		var successors []int
		for i := len(rounds) - 1; i >= 0; i-- {
			round := rounds[i]
			if round.hasResidual {
				scope := workstate.Scope{JobID: jobID, ResourceID: resourceID, RoundID: round.rowID}
				residual, err := r.loadStagedResidual(ctx, scope, successors)
				if err != nil {
					return fmt.Errorf("load unresolved round members: %w", err)
				}
				if residual.total() > 0 && len(successors) > 0 {
					if err := r.jobs.MarkJobRoundUnresolved(ctx, jobID, round.rowID, residual.roundFailure()); err != nil {
						return fmt.Errorf("persist unresolved predecessor round: %w", err)
					}
				}
				combined.unresolved += residual.unresolved
				combined.failed += residual.failed
				if combined.cause == "" {
					combined.cause = residual.cause
				}
			}
			successors = append(successors, round.rowID)
		}
		residualByMode[mode] = combined
	}
	return nil
}

func usesStagedRound(mode string) bool {
	return mode == pipeline.RoundModeTranslate || mode == pipeline.RoundModeRevise
}

// A later round can take over only the members in its own sealed manifest.
// Earlier translated segments excluded by a later pending_only filter retain
// their original debt, including when the earlier round was closed before a restart.
func (r *JobRunner) loadStagedResidual(ctx context.Context, scope workstate.Scope, successorIDs []int) (roundResidual, error) {
	query := r.client.WorkItem.Query().Where(workitem.JobIDEQ(scope.JobID), workitem.ResourceIDEQ(scope.ResourceID),
		workitem.JobRoundIDEQ(scope.RoundID), workitem.StateNEQ("resolved"))
	if len(successorIDs) > 0 {
		query.Where(func(s *sql.Selector) {
			later := sql.Table(workitem.Table).As("later_member")
			members := sql.Dialect(s.Dialect()).Select(later.C(workitem.FieldID)).From(later).Where(sql.And(
				sql.InInts(later.C(workitem.FieldJobRoundID), successorIDs...),
				sql.ColumnsEQ(later.C(workitem.FieldSegmentID), s.C(workitem.FieldSegmentID)),
			))
			s.Where(sql.Not(sql.Exists(members)))
		})
	}
	count, err := query.Clone().Count(ctx)
	if err != nil || count == 0 {
		return roundResidual{}, err
	}
	failed, err := query.Clone().Where(workitem.PromptPhaseIn("terminal_failure", "terminal_failure_batch")).Count(ctx)
	if err != nil {
		return roundResidual{}, err
	}
	residual := roundResidual{unresolved: count - failed, failed: failed}
	row, err := query.Where(workitem.LastErrorNEQ(""), func(s *sql.Selector) {
		value := s.C(workitem.FieldLastError)
		bytes := "length(CAST(" + value + " AS BLOB))"
		if s.Dialect() == dialect.Postgres {
			bytes = "octet_length(" + value + ")"
		}
		s.Where(sql.P(func(b *sql.Builder) { b.WriteString(bytes).WriteOp(sql.OpLTE).Arg(2048) }))
	}).Select(workitem.FieldLastError).Order(ent.Desc(workitem.FieldUpdatedAt, workitem.FieldID)).First(ctx)
	if ent.IsNotFound(err) {
		return residual, nil
	}
	if err != nil {
		return roundResidual{}, err
	}
	residual.cause = row.LastError
	return residual, nil
}

func (residual roundResidual) roundFailure() error {
	message := fmt.Sprintf("轮次有 %d 个未解决段、%d 个终态失败段，无后续同模式轮次承接", residual.unresolved, residual.failed)
	if residual.cause != "" {
		return fmt.Errorf("%s：%s", message, residual.cause)
	}
	return errors.New(message)
}
