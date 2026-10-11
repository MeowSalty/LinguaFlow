package workstate

import (
	"context"
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
)

// CandidateHeader contains no source/target or serialized DTO. Recovery can
// rebuild window accounting before decoding any candidate bodies.
type CandidateHeader struct {
	DatabaseID     int
	ID             string
	Version        int64
	Scope          Scope
	SegmentID      int
	DTOVersion     int
	PayloadBytes   int64
	State          string
	SnapshotDigest string
	Mode           string
}

func (s *Store) LoadCandidateHeaders(ctx context.Context, jobID, afterID, limit int) ([]CandidateHeader, int, error) {
	if limit < 1 || limit > 256 {
		return nil, afterID, fmt.Errorf("invalid candidate header page")
	}
	rows, err := s.client.WorkCandidate.Query().Where(workcandidate.IDGT(afterID), workcandidate.StateIn("pending_alignment", "ready_to_commit"), workcandidate.HasWorkItemWith(workitem.JobIDEQ(jobID))).
		Select(workcandidate.FieldID, workcandidate.FieldIdentity, workcandidate.FieldVersion, workcandidate.FieldWorkItemID, workcandidate.FieldDtoVersion, workcandidate.FieldPayloadBytes, workcandidate.FieldState, workcandidate.FieldSnapshotDigest, workcandidate.FieldMode, workcandidate.FieldSourceGeneration, workcandidate.FieldSourceRevisionID).
		Order(ent.Asc(workcandidate.FieldID)).Limit(limit).WithWorkItem(func(q *ent.WorkItemQuery) {
		q.Select(workitem.FieldID, workitem.FieldJobID, workitem.FieldResourceID, workitem.FieldJobRoundID, workitem.FieldSegmentID, workitem.FieldRetryEpoch)
	}).All(ctx)
	if err != nil {
		return nil, afterID, err
	}
	result := make([]CandidateHeader, 0, len(rows))
	for _, row := range rows {
		w := row.Edges.WorkItem
		if w == nil {
			return nil, afterID, ErrManifest
		}
		result = append(result, CandidateHeader{DatabaseID: row.ID, ID: row.Identity, Version: row.Version, Scope: Scope{JobID: w.JobID, ResourceID: w.ResourceID, RoundID: w.JobRoundID, RetryEpoch: w.RetryEpoch, SourceGeneration: row.SourceGeneration, SourceRevisionID: row.SourceRevisionID}, SegmentID: w.SegmentID, DTOVersion: row.DtoVersion, PayloadBytes: row.PayloadBytes, State: row.State, SnapshotDigest: row.SnapshotDigest, Mode: row.Mode})
		afterID = row.ID
	}
	return result, afterID, nil
}

func (s *Store) LoadCursor(ctx context.Context, scope Scope, segmentID int) (Cursor, error) {
	row, err := s.client.WorkItem.Query().Where(workitem.JobIDEQ(scope.JobID), workitem.ResourceIDEQ(scope.ResourceID), workitem.JobRoundIDEQ(scope.RoundID), workitem.SegmentIDEQ(segmentID)).Only(ctx)
	if err != nil {
		return Cursor{}, err
	}
	if row.RetryEpoch != scope.RetryEpoch {
		return Cursor{}, ErrStopped
	}
	return cursorFromRow(row), nil
}

// ResourcesWithCandidates is bounded and read-only. Resource admission can put
// these resources before producers without loading their documents or DTOs.
func (s *Store) ResourcesWithCandidates(ctx context.Context, jobID, afterResourceID, limit int) ([]int, error) {
	if limit < 1 || limit > 256 {
		return nil, fmt.Errorf("invalid recovery resource page")
	}
	return s.client.WorkCandidate.Query().Where(workcandidate.StateIn("pending_alignment", "ready_to_commit"), workcandidate.HasWorkItemWith(workitem.JobIDEQ(jobID), workitem.HasResourceWith(resource.IDGT(afterResourceID)))).QueryWorkItem().Unique(true).Order(ent.Asc(workitem.FieldResourceID)).Limit(limit).Select(workitem.FieldResourceID).Ints(ctx)
}
