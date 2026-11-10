package workstate

import (
	"context"
	"entgo.io/ent/dialect/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
)

func (s *Store) LoadRoundHeader(ctx context.Context, scope Scope) (RoundState, error) {
	r, err := s.client.JobRound.Query().Where(jobround.IDEQ(scope.RoundID), jobround.JobIDEQ(scope.JobID)).Select(jobround.FieldID, jobround.FieldManifestSealed, jobround.FieldManifestVersion, jobround.FieldSegmentTotal, jobround.FieldPoolIndex).Only(ctx)
	if err != nil {
		return RoundState{}, err
	}
	return RoundState{Sealed: r.ManifestSealed, ManifestVersion: r.ManifestVersion, Total: r.SegmentTotal, PoolIndex: r.PoolIndex}, nil
}

// LoadRoundMembersPage projects fixed cursor facts only. Cursor JSON, arbitrary
// last_error and candidate/raw bodies cannot be reached through this query.
func (s *Store) LoadRoundMembersPage(ctx context.Context, scope Scope, after, limit int) ([]Work, int, error) {
	if limit < 1 || limit > 16 {
		return nil, after, ErrManifest
	}
	q := s.client.WorkItem.Query().Where(workitem.JobIDEQ(scope.JobID), workitem.ResourceIDEQ(scope.ResourceID), workitem.JobRoundIDEQ(scope.RoundID), func(sel *sql.Selector) { sel.Where(sql.GT(sel.C(workitem.FieldSegmentID), after)) })
	var sizes []struct {
		Bytes int64 `json:"bytes"`
	}
	if err := q.Clone().Aggregate(func(sel *sql.Selector) string {
		return sql.As("COALESCE(MAX(length(CAST("+sel.C(workitem.FieldState)+" AS BLOB))+length(CAST("+sel.C(workitem.FieldPromptPhase)+" AS BLOB))),0)", "bytes")
	}).Scan(ctx, &sizes); err != nil {
		return nil, after, err
	}
	if len(sizes) != 1 || sizes[0].Bytes > 128 {
		return nil, after, ErrManifest
	}
	rows, err := q.Select(workitem.FieldID, workitem.FieldSegmentID, workitem.FieldRetryEpoch, workitem.FieldState, workitem.FieldPoolIndex, workitem.FieldMainAttempts, workitem.FieldAlignmentAttempts, workitem.FieldAlignmentNetworkAttempts, workitem.FieldPromptPhase, workitem.FieldNextAttemptAt).Order(ent.Asc(workitem.FieldSegmentID)).Limit(limit).All(ctx)
	if err != nil {
		return nil, after, err
	}
	out := make([]Work, 0, len(rows))
	for _, r := range rows {
		if r.RetryEpoch != scope.RetryEpoch {
			return nil, after, ErrStopped
		}
		out = append(out, Work{SegmentID: r.SegmentID, RetryEpoch: r.RetryEpoch, Cursor: Cursor{State: r.State, PoolIndex: r.PoolIndex, MainAttempts: r.MainAttempts, AlignmentAttempts: r.AlignmentAttempts, AlignmentNetworkAttempts: r.AlignmentNetworkAttempts, PromptPhase: r.PromptPhase, NextAttemptAt: r.NextAttemptAt}})
		after = r.SegmentID
	}
	return out, after, nil
}
