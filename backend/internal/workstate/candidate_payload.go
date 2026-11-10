package workstate

import (
	"context"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
)

func (s *Store) LoadCandidatePayload(ctx context.Context, scope Scope, id string, maxBytes int64) (Candidate, error) {
	trace := backend.NewOwnedTrace(ctx, "db_copy", "candidate-orm")
	defer trace.Clear()
	q := s.client.WorkCandidate.Query().Where(workcandidate.IdentityEQ(id), workcandidate.StateIn("pending_alignment", "ready_to_commit"), workcandidate.HasWorkItemWith(workitem.JobIDEQ(scope.JobID), workitem.ResourceIDEQ(scope.ResourceID), workitem.JobRoundIDEQ(scope.RoundID)))
	header, err := q.Clone().Select(workcandidate.FieldID, workcandidate.FieldPayloadBytes, workcandidate.FieldDtoVersion).Only(ctx)
	if err != nil {
		return Candidate{}, err
	}
	if header.DtoVersion != 2 || header.PayloadBytes <= 0 || header.PayloadBytes > maxBytes {
		return Candidate{}, ErrManifest
	}
	row, err := q.Where(workcandidate.PayloadBytesLTE(maxBytes)).Select(workcandidate.FieldID, workcandidate.FieldIdentity, workcandidate.FieldParentRequestID, workcandidate.FieldVersion, workcandidate.FieldWorkItemID, workcandidate.FieldDtoVersion, workcandidate.FieldMode, workcandidate.FieldSnapshotDigest, workcandidate.FieldBaselineVersion, workcandidate.FieldBaselineDigest, workcandidate.FieldBaselineTargetIsNull, workcandidate.FieldBaselineStatus, workcandidate.FieldState, workcandidate.FieldPayloadBytes, workcandidate.FieldPipelineHeader, workcandidate.FieldPayload).WithWorkItem(func(w *ent.WorkItemQuery) { w.Select(workitem.FieldID, workitem.FieldSegmentID) }).Only(ctx)
	if err != nil {
		return Candidate{}, err
	}
	w := row.Edges.WorkItem
	trace.Set(int64(cap(row.Payload) + cap(row.PipelineHeader)))
	if w == nil || row.PayloadBytes != int64(len(row.Payload)) {
		return Candidate{}, ErrManifest
	}
	return Candidate{PipelineHeader: row.PipelineHeader, ID: row.Identity, ParentRequestID: row.ParentRequestID, Version: row.Version, Scope: scope, SegmentID: w.SegmentID, DTOVersion: row.DtoVersion, Mode: row.Mode, SnapshotDigest: row.SnapshotDigest, BaselineVersion: row.BaselineVersion, BaselineTarget: row.BaselineTarget, BaselineStatus: row.BaselineStatus, State: row.State, Payload: row.Payload}, nil
}
