package workstate

import (
	"context"

	"entgo.io/ent/dialect"
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
	q := s.client.WorkItem.Query().Where(func(sel *sql.Selector) {
		page := sql.Table(workitem.Table).As("member_page")
		ids := sql.Dialect(sel.Dialect()).Select(page.C(workitem.FieldID)).From(page).
			Where(sql.And(sql.EQ(page.C(workitem.FieldJobID), scope.JobID), sql.EQ(page.C(workitem.FieldResourceID), scope.ResourceID), sql.EQ(page.C(workitem.FieldJobRoundID), scope.RoundID), sql.GT(page.C(workitem.FieldSegmentID), after))).
			OrderBy(page.C(workitem.FieldSegmentID)).Limit(limit)
		sel.Where(sql.In(sel.C(workitem.FieldID), ids))
	})
	var sizes []struct {
		Bytes int64 `json:"bytes"`
		Count int   `json:"count"`
	}
	if err := q.Clone().Aggregate(func(sel *sql.Selector) string {
		return sql.As("COALESCE(MAX("+roundMemberCursorBytes(sel)+"),0)", "bytes")
	}, func(*sql.Selector) string { return sql.As(sql.Count("*"), "count") }).Scan(ctx, &sizes); err != nil {
		return nil, after, err
	}
	if len(sizes) != 1 || sizes[0].Bytes > 128 {
		return nil, after, ErrManifest
	}
	// Recheck in the SELECT as well: a scalar growing after the preflight must
	// never enter Go, and a changed page must fail rather than omit a member.
	rows, err := q.Where(func(sel *sql.Selector) {
		sel.Where(sql.P(func(builder *sql.Builder) {
			builder.WriteString(roundMemberCursorBytes(sel)).WriteOp(sql.OpLTE).Arg(128)
		}))
	}).Select(workitem.FieldID, workitem.FieldSegmentID, workitem.FieldRetryEpoch, workitem.FieldState, workitem.FieldPoolIndex, workitem.FieldMainAttempts, workitem.FieldAlignmentAttempts, workitem.FieldAlignmentNetworkAttempts, workitem.FieldPromptPhase, workitem.FieldNextAttemptAt).Order(ent.Asc(workitem.FieldSegmentID)).Limit(limit).All(ctx)
	if err != nil {
		return nil, after, err
	}
	if len(rows) != sizes[0].Count {
		return nil, after, ErrManifest
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

func roundMemberCursorBytes(sel *sql.Selector) string {
	state := "COALESCE(" + sel.C(workitem.FieldState) + ",'')"
	phase := "COALESCE(" + sel.C(workitem.FieldPromptPhase) + ",'')"
	if sel.Dialect() == dialect.Postgres {
		return "octet_length(" + state + ")+octet_length(" + phase + ")"
	}
	return "length(CAST(" + state + " AS BLOB))+length(CAST(" + phase + " AS BLOB))"
}
