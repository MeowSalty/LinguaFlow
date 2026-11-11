package workstate

import (
	"context"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
)

type OwnedMemberHeader struct {
	DatabaseID, SegmentID int
	Identity              string
	Scope                 Scope
}

// LoadOwnedMemberHeaders excludes S already represented by raw/candidate headers.
// Unknown sent intents and member backoff keep one fixed ownership identity.
func (s *Store) LoadOwnedMemberHeaders(ctx context.Context, jobID, after, limit int) ([]OwnedMemberHeader, int, error) {
	if err := guardExecution(ctx, s.client, jobID); err != nil {
		return nil, after, err
	}
	limit = min(max(1, limit), 16)
	rows, err := s.client.WorkItem.Query().Where(workitem.JobIDEQ(jobID), workitem.IDGT(after), workitem.MemberOwnedEQ(true), workitem.StateNotIn("candidate", "response_pending", "resolved", "unresolved", "stale", "rejected", "cancelled")).Select(workitem.FieldID, workitem.FieldJobID, workitem.FieldResourceID, workitem.FieldJobRoundID, workitem.FieldSegmentID, workitem.FieldRetryEpoch).Order(ent.Asc(workitem.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return nil, after, err
	}
	out := make([]OwnedMemberHeader, 0, len(rows))
	for _, r := range rows {
		scope := Scope{JobID: r.JobID, ResourceID: r.ResourceID, RoundID: r.JobRoundID, RetryEpoch: r.RetryEpoch}
		out = append(out, OwnedMemberHeader{DatabaseID: r.ID, SegmentID: r.SegmentID, Identity: "member:" + WorkIdentity(scope, r.SegmentID), Scope: scope})
		after = r.ID
	}
	return out, after, nil
}
