package workstate

import (
	"context"
	"entgo.io/ent/dialect/sql"
	"fmt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/predicate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
)

func (s *Store) LoadCandidatePayload(ctx context.Context, scope Scope, id string, maxBytes int64) (Candidate, error) {
	return s.LoadCandidatePayloadReserved(ctx, scope, id, maxBytes, nil, nil)
}

func (s *Store) LoadCandidatePayloadReserved(ctx context.Context, scope Scope, id string, maxBytes int64, reserve func(int64) (func(), error), build func(func() error) error) (Candidate, error) {
	trace := backend.NewOwnedTrace(ctx, "db_copy", "candidate-orm")
	defer trace.Clear()
	q := s.client.WorkCandidate.Query().Where(workcandidate.IdentityEQ(id), workcandidate.StateIn("pending_alignment", "ready_to_commit"), workcandidate.HasWorkItemWith(workitem.JobIDEQ(scope.JobID), workitem.ResourceIDEQ(scope.ResourceID), workitem.JobRoundIDEQ(scope.RoundID)))
	var headers []struct {
		PayloadBytes int64 `json:"payload_bytes"`
		DtoVersion   int   `json:"dto_version"`
		Version      int64 `json:"version"`
		StoredBytes  int64 `json:"stored_bytes"`
	}
	err := q.Clone().Select(workcandidate.FieldPayloadBytes, workcandidate.FieldDtoVersion, workcandidate.FieldVersion).Aggregate(func(sel *sql.Selector) string {
		return projectedByteLength(sel, workcandidate.FieldPayload) + " AS stored_bytes"
	}).Scan(ctx, &headers)
	if err != nil {
		return Candidate{}, err
	}
	if len(headers) != 1 {
		return Candidate{}, ErrManifest
	}
	header := headers[0]
	if header.DtoVersion != 2 || header.PayloadBytes <= 0 || header.PayloadBytes > maxBytes || header.StoredBytes <= 0 || header.StoredBytes > CandidateReadBytes {
		return Candidate{}, ErrManifest
	}
	if reserve != nil {
		release, err := reserve(header.StoredBytes)
		if err != nil {
			return Candidate{}, err
		}
		defer release()
	}
	row, err := q.Where(workcandidate.PayloadBytesLTE(maxBytes), workcandidate.VersionEQ(header.Version), predicate.WorkCandidate(func(sel *sql.Selector) {
		sel.Where(projectedLengthPredicate(sel, workcandidate.FieldPayload, "<=", header.StoredBytes))
	})).Select(workcandidate.FieldID, workcandidate.FieldIdentity, workcandidate.FieldParentRequestID, workcandidate.FieldVersion, workcandidate.FieldWorkItemID, workcandidate.FieldDtoVersion, workcandidate.FieldMode, workcandidate.FieldSnapshotDigest, workcandidate.FieldBaselineVersion, workcandidate.FieldBaselineDigest, workcandidate.FieldBaselineTargetIsNull, workcandidate.FieldBaselineStatus, workcandidate.FieldState, workcandidate.FieldPayloadBytes, workcandidate.FieldPipelineHeader, workcandidate.FieldPayload).WithWorkItem(func(w *ent.WorkItemQuery) { w.Select(workitem.FieldID, workitem.FieldSegmentID) }).Only(ctx)
	if err != nil {
		return Candidate{}, err
	}
	w := row.Edges.WorkItem
	trace.Set(int64(cap(row.Payload) + cap(row.PipelineHeader)))
	if w == nil || header.StoredBytes != int64(len(row.Payload)) {
		return Candidate{}, ErrManifest
	}
	var payload []byte
	decode := func() error { var err error; payload, err = CanonicalCandidateJSON(row.Payload, maxBytes); return err }
	if build != nil {
		err = build(decode)
	} else {
		err = decode()
	}
	if err != nil {
		return Candidate{}, err
	}
	if int64(len(payload)) != row.PayloadBytes {
		return Candidate{}, fmt.Errorf("%w: canonical candidate bytes mismatch", ErrManifest)
	}
	row.Payload = payload
	return Candidate{PipelineHeader: row.PipelineHeader, ID: row.Identity, ParentRequestID: row.ParentRequestID, Version: row.Version, Scope: scope, SegmentID: w.SegmentID, DTOVersion: row.DtoVersion, Mode: row.Mode, SnapshotDigest: row.SnapshotDigest, BaselineVersion: row.BaselineVersion, BaselineTarget: row.BaselineTarget, BaselineStatus: row.BaselineStatus, State: row.State, Payload: row.Payload}, nil
}
