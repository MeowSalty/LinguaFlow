package workstate

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"entgo.io/ent/dialect/sql"
	"fmt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"io"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/predicate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workcandidate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
)

// PromotionMember freezes identity and acceptance baseline before dispatch.
// It is different from an alignment member: its candidate does not exist yet.
type PromotionMember struct {
	SegmentID            int     `json:"segment_id"`
	WorkID               string  `json:"work_id"`
	CandidateID          string  `json:"candidate_id"`
	BaselineVersion      int64   `json:"baseline_version"`
	BaselineTarget       *string `json:"-"`
	BaselineDigest       string  `json:"baseline_digest"`
	BaselineTargetIsNull bool    `json:"baseline_target_is_null"`
	BaselineStatus       string  `json:"baseline_status"`
	SourceGeneration     int64   `json:"source_generation"`
	SourceRevisionID     *int    `json:"source_revision_id"`
}

type ResponseHandoff struct {
	RequestID       string
	Scope           Scope
	FreezePayload   []byte
	ResponsePayload []byte
	SnapshotDigest  string
	Members         []PromotionMember
	BusinessBound   int64
	Truncated       bool
	Result          RequestResult
}

// ResponseHeader is small enough to restore ownership without reading bodies.
type ResponseHeader struct {
	MetadataBytes  int64
	DatabaseID     int
	RequestID      string
	Scope          Scope
	SnapshotDigest string
	FreezeDigest   string
	ResponseDigest string
	FreezeBytes    int64
	ResponseBytes  int64
	CorrectedBytes int64
	BusinessBound  int64
	MemberCount    int
	Cursor         int
	Phase          string
	Truncated      bool
}

type StoredResponse struct {
	ResponseHeader
	FreezePayload    []byte
	ResponsePayload  []byte
	CorrectedTargets []byte
	CorrectedIndex   []byte
	CorrectedDigest  string
	Members          []PromotionMember
	Receipt          []byte
}

type MemberPromotion struct {
	Scope     Scope
	RequestID string
	Cursor    int
	Candidate *Candidate
	Next      Cursor
	Failure   string
}

func payloadDigest(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }

func validatePromotionMembers(scope Scope, ids []int, members []PromotionMember) error {
	if len(ids) == 0 || len(ids) != len(members) {
		return ErrManifest
	}
	if len(members) > 65536 {
		return ErrManifest
	}
	seen := make(map[string]bool, len(members))
	for i, m := range members {
		if m.SegmentID != ids[i] || m.WorkID != WorkIdentity(scope, m.SegmentID) || m.CandidateID == "" || seen[m.CandidateID] || m.BaselineVersion < 1 {
			return ErrManifest
		}
		if m.BaselineDigest == "" {
			m.BaselineDigest = stringDigest(m.BaselineTarget)
			m.BaselineTargetIsNull = m.BaselineTarget == nil
			members[i] = m
		}
		if len(m.BaselineDigest) != 64 || len(m.WorkID) > 128 || len(m.CandidateID) > 128 || len(m.BaselineStatus) > 128 {
			return ErrManifest
		}
		seen[m.CandidateID] = true
	}
	return nil
}

func decodePromotionMembers(row *ent.WorkRequest) ([]PromotionMember, error) {
	var members []PromotionMember
	if row.PromotionCount < 1 || row.PromotionCount > 65536 || len(row.PromotionMembers) > row.PromotionCount*4096 {
		return nil, ErrManifest
	}
	b, err := backend.NewBuildBounds(int64(row.PromotionCount)*4096, 0)
	if err != nil {
		return nil, err
	}
	if err := b.DecodeJSON(row.PromotionMembers, &members); err != nil {
		return nil, ErrManifest
	}
	scope := Scope{JobID: row.JobID, ResourceID: row.ResourceID, RoundID: row.JobRoundID}
	if err := validatePromotionMembers(scope, row.SegmentIds, members); err != nil {
		return nil, err
	}
	if row.PromotionCount != len(members) || len(row.PromotionStatus) != len(members) || row.PromotionCursor > len(members) {
		return nil, ErrManifest
	}
	return members, nil
}

func guardExecution(ctx context.Context, tx *ent.Client, jobID int) error {
	return CheckExecution(ctx, tx, jobID)
}

// HandoffResponse makes a complete response, accounting and all pending member
// links visible together. Acknowledgement loss never changes its identity.
func (s *Store) HandoffResponse(ctx context.Context, in ResponseHandoff) error {
	if in.RequestID == "" || len(in.FreezePayload) == 0 || len(in.ResponsePayload) == 0 || in.SnapshotDigest == "" || in.BusinessBound < 0 {
		return ErrManifest
	}
	freezeDigest, responseDigest := payloadDigest(in.FreezePayload), payloadDigest(in.ResponsePayload)
	ctx = operationContext(ctx)
	err := Transaction(ctx, s.client, func(tx *ent.Client) error {
		if err := guardScope(ctx, tx, in.Scope, false); err != nil {
			return err
		}
		if err := guardExecution(ctx, tx, in.Scope.JobID); err != nil {
			return err
		}
		row, err := projectedRequest(tx.WorkRequest.Query().Where(workrequest.IdentityEQ(in.RequestID))).Only(ctx)
		if err != nil {
			return err
		}
		if row.JobID != in.Scope.JobID || row.ResourceID != in.Scope.ResourceID || row.JobRoundID != in.Scope.RoundID || row.RetryEpoch != in.Scope.RetryEpoch || row.Stage != "main" {
			return ErrManifest
		}
		if err := validatePromotionMembers(in.Scope, row.SegmentIds, in.Members); err != nil {
			return err
		}
		encoded, err := json.Marshal(in.Members)
		if err != nil {
			return err
		}
		membersEqual := true
		if row.FreezeDigest != "" || row.ResponseDigest != "" {
			membersEqual, err = promotionMembersMatch(ctx, tx, row, encoded)
			if err != nil {
				return err
			}
		}
		if row.ResponseDigest != "" {
			if row.State != "completed" || row.ResponseFormatVersion != execution.ResponseFormatVersion || row.FreezeDigest != freezeDigest || row.ResponseDigest != responseDigest || row.SnapshotDigest != in.SnapshotDigest || !membersEqual {
				return ErrManifest
			}
			return nil
		}
		if row.State == "failed" || row.State == "cancelled_before_dispatch" {
			return ErrStopped
		}
		if row.FreezeDigest != "" && (row.FreezeDigest != freezeDigest || row.SnapshotDigest != in.SnapshotDigest || !membersEqual) {
			return ErrManifest
		}
		for _, m := range in.Members {
			w, err := member(ctx, tx, in.Scope, m.SegmentID)
			if err != nil {
				return err
			}
			if w.State == "resolved" || w.CandidateID != "" || w.ResponseRequestID != "" && w.ResponseRequestID != in.RequestID {
				return ErrStale
			}
			if err := tx.WorkItem.UpdateOneID(w.ID).SetMemberOwned(true).SetState("response_pending").SetResponseRequestID(in.RequestID).Exec(ctx); err != nil {
				return err
			}
		}
		result := in.Result
		result.State = "completed"
		if _, err := recordRequestAccountingTx(ctx, tx, row, result); err != nil {
			return err
		}
		return tx.WorkRequest.UpdateOneID(row.ID).SetFreezePayload(in.FreezePayload).SetFreezeBytes(int64(len(in.FreezePayload))).SetFreezeDigest(freezeDigest).SetResponsePayload(in.ResponsePayload).SetResponseBytes(int64(len(in.ResponsePayload))).SetResponseDigest(responseDigest).SetResponseFormatVersion(execution.ResponseFormatVersion).SetSnapshotDigest(in.SnapshotDigest).SetPromotionMembers(encoded).SetPromotionCount(len(in.Members)).SetPromotionCursor(0).SetPromotionStatus(make([]byte, len(in.Members))).SetPromotionState("response_pending").SetBusinessBound(in.BusinessBound).SetResponseTruncated(in.Truncated).SetHandoffAt(time.Now().UTC()).Exec(ctx)
	})
	return s.finishUnknown(ctx, in.Scope.JobID, err, func(ctx context.Context, tx *ent.Client) (bool, error) {
		row, err := projectedRequest(tx.WorkRequest.Query().Where(workrequest.IdentityEQ(in.RequestID))).Only(ctx)
		if err != nil {
			return false, err
		}
		if row.ResponseDigest == "" {
			return false, nil
		}
		if row.State != "completed" || row.FreezeDigest != freezeDigest || row.ResponseDigest != responseDigest || row.SnapshotDigest != in.SnapshotDigest || row.ResponseFormatVersion != execution.ResponseFormatVersion {
			return false, errUnverifiable
		}
		members, err := decodePromotionMembers(row)
		if err != nil || len(members) != len(in.Members) {
			return false, ErrManifest
		}
		for i := row.PromotionCursor; i < len(members); i++ {
			w, err := member(ctx, tx, in.Scope, members[i].SegmentID)
			if err != nil {
				return false, err
			}
			if w.ResponseRequestID != in.RequestID || w.State != "response_pending" {
				return false, errUnverifiable
			}
		}
		return true, nil
	})
}

func responseHeader(row *ent.WorkRequest) ResponseHeader {
	return ResponseHeader{DatabaseID: row.ID, RequestID: row.Identity, Scope: Scope{JobID: row.JobID, ResourceID: row.ResourceID, RoundID: row.JobRoundID, RetryEpoch: row.RetryEpoch}, SnapshotDigest: row.SnapshotDigest, FreezeDigest: row.FreezeDigest, ResponseDigest: row.ResponseDigest, FreezeBytes: row.FreezeBytes, ResponseBytes: row.ResponseBytes, CorrectedBytes: row.CorrectedTargetBytes, BusinessBound: row.BusinessBound, MemberCount: row.PromotionCount, Cursor: row.PromotionCursor, Phase: row.PromotionState, Truncated: row.ResponseTruncated}
}

// Headers deliberately include earlier retry epochs. Only reliable responses
// survive an epoch change; an uncertain invocation cannot enter this query.
func (s *Store) LoadResponseHeaders(ctx context.Context, jobID, after, limit int) ([]ResponseHeader, int, error) {
	if limit < 1 || limit > 256 {
		return nil, after, ErrManifest
	}
	invalid, err := s.client.WorkRequest.Query().Where(workrequest.JobIDEQ(jobID), workrequest.IDGT(after), workrequest.StateEQ("completed"), workrequest.PromotionStateIn("response_pending", "glossary_processed"), predicate.WorkRequest(func(q *sql.Selector) {
		q.Where(sql.Or(sql.ExprP(projectedByteLength(q, workrequest.FieldFreezePayload)+" != "+q.C(workrequest.FieldFreezeBytes)), sql.ExprP(projectedByteLength(q, workrequest.FieldResponsePayload)+" != "+q.C(workrequest.FieldResponseBytes)), sql.ExprP(projectedByteLength(q, workrequest.FieldCorrectedTargets)+" != "+q.C(workrequest.FieldCorrectedTargetBytes))))
	})).Exist(ctx)
	if err != nil {
		return nil, after, err
	}
	if invalid {
		return nil, after, ErrManifest
	}
	rows, err := s.client.WorkRequest.Query().Where(workrequest.JobIDEQ(jobID), workrequest.IDGT(after), workrequest.StateEQ("completed"), workrequest.PromotionStateIn("response_pending", "glossary_processed")).Select(workrequest.FieldID, workrequest.FieldIdentity, workrequest.FieldJobID, workrequest.FieldResourceID, workrequest.FieldJobRoundID, workrequest.FieldRetryEpoch, workrequest.FieldSnapshotDigest, workrequest.FieldFreezeDigest, workrequest.FieldResponseDigest, workrequest.FieldFreezeBytes, workrequest.FieldResponseBytes, workrequest.FieldCorrectedTargetBytes, workrequest.FieldBusinessBound, workrequest.FieldPromotionCount, workrequest.FieldPromotionCursor, workrequest.FieldPromotionState, workrequest.FieldResponseTruncated).Order(ent.Asc(workrequest.FieldID)).Limit(limit).All(ctx)
	if err != nil {
		return nil, after, err
	}
	out := make([]ResponseHeader, 0, len(rows))
	for _, row := range rows {
		h := responseHeader(row)
		h.MetadataBytes, err = loadRequestMetadata(ctx, s.client, row.Identity)
		if err != nil {
			return nil, after, err
		}
		out = append(out, h)
		after = row.ID
	}
	return out, after, nil
}

func (s *Store) LoadResponse(ctx context.Context, requestID string, maxFreeze, maxResponse, maxBusiness int64) (StoredResponse, error) {
	trace := backend.NewOwnedTrace(ctx, "db_copy", "response-orm")
	defer trace.Clear()
	// Inspect projected sizes before asking the driver to materialize any BLOB.
	row, err := s.client.WorkRequest.Query().Where(workrequest.IdentityEQ(requestID)).Select(workrequest.FieldID, workrequest.FieldFreezeBytes, workrequest.FieldResponseBytes, workrequest.FieldCorrectedTargetBytes).Only(ctx)
	if err != nil {
		return StoredResponse{}, err
	}
	if row.FreezeBytes <= 0 || row.FreezeBytes > maxFreeze || row.ResponseBytes <= 0 || row.ResponseBytes > maxResponse || row.CorrectedTargetBytes > maxBusiness {
		return StoredResponse{}, fmt.Errorf("saved response exceeds configured capacity")
	}
	row, err = s.client.WorkRequest.Query().Where(workrequest.IDEQ(row.ID), workrequest.FreezeBytesLTE(maxFreeze), workrequest.ResponseBytesLTE(maxResponse), workrequest.CorrectedTargetBytesLTE(maxBusiness), func(sel *sql.Selector) {
		sel.Where(requestMetadataBounds(sel))
		for _, bound := range []struct {
			field string
			max   int64
		}{{workrequest.FieldFreezePayload, maxFreeze}, {workrequest.FieldResponsePayload, maxResponse}, {workrequest.FieldCorrectedTargets, maxBusiness}} {
			sel.Where(projectedLengthPredicate(sel, bound.field, "<=", bound.max))
		}
	}).Only(ctx)
	if err != nil {
		return StoredResponse{}, err
	}
	trace.Set(int64(cap(row.FreezePayload) + cap(row.ResponsePayload) + cap(row.CorrectedTargets) + cap(row.CorrectedTargetIndex) + cap(row.PromotionMembers) + cap(row.GlossaryReceipt)))
	if row.State != "completed" || row.ResponseFormatVersion != execution.ResponseFormatVersion || int64(len(row.FreezePayload)) != row.FreezeBytes || int64(len(row.ResponsePayload)) != row.ResponseBytes || payloadDigest(row.FreezePayload) != row.FreezeDigest || payloadDigest(row.ResponsePayload) != row.ResponseDigest || int64(len(row.CorrectedTargets)) != row.CorrectedTargetBytes {
		return StoredResponse{}, ErrManifest
	}
	if row.CorrectedTargetBytes > 0 && payloadDigest(row.CorrectedTargets) != row.CorrectedTargetDigest {
		return StoredResponse{}, ErrManifest
	}
	if err := validateResponseReceipt(row.GlossaryReceipt, row.Identity); err != nil {
		return StoredResponse{}, err
	}
	members, err := decodePromotionMembers(row)
	if err != nil {
		return StoredResponse{}, err
	}
	header := responseHeader(row)
	header.MetadataBytes, err = loadRequestMetadata(ctx, s.client, row.Identity)
	if err != nil {
		return StoredResponse{}, err
	}
	return StoredResponse{ResponseHeader: header, FreezePayload: row.FreezePayload, ResponsePayload: row.ResponsePayload, CorrectedTargets: row.CorrectedTargets, CorrectedIndex: row.CorrectedTargetIndex, CorrectedDigest: row.CorrectedTargetDigest, Members: members, Receipt: row.GlossaryReceipt}, nil
}

func (s *Store) LoadResponseMember(ctx context.Context, id string, cursor int) (PromotionMember, error) {
	r, err := s.client.WorkRequest.Query().Where(workrequest.IdentityEQ(id), func(sel *sql.Selector) { sel.Where(requestMetadataBounds(sel)) }).Select(workrequest.FieldID, workrequest.FieldJobID, workrequest.FieldResourceID, workrequest.FieldJobRoundID, workrequest.FieldSegmentIds, workrequest.FieldPromotionMembers, workrequest.FieldPromotionCount, workrequest.FieldPromotionCursor, workrequest.FieldPromotionStatus).Only(ctx)
	if err != nil {
		return PromotionMember{}, err
	}
	members, err := decodePromotionMembers(r)
	if err != nil || cursor < 0 || cursor >= len(members) {
		return PromotionMember{}, ErrManifest
	}
	return members[cursor], nil
}

// HasPendingResponse is used by Retry while holding the Job lifecycle lock.
// It preserves a reliable member link without manufacturing a new request.
func HasPendingResponse(ctx context.Context, tx *ent.Client, w *ent.WorkItem) (bool, error) {
	if w.ResponseRequestID == "" {
		return false, nil
	}
	r, err := projectedRequest(tx.WorkRequest.Query().Where(workrequest.IdentityEQ(w.ResponseRequestID))).Only(ctx)
	if err != nil {
		return false, err
	}
	if r.JobID != w.JobID || r.ResourceID != w.ResourceID || r.JobRoundID != w.JobRoundID || r.State != "completed" || r.ResponseFormatVersion != execution.ResponseFormatVersion || r.PromotionState != "response_pending" && r.PromotionState != "glossary_processed" {
		return false, ErrManifest
	}
	members, err := decodePromotionMembers(r)
	if err != nil {
		return false, err
	}
	for i := r.PromotionCursor; i < len(members); i++ {
		if members[i].SegmentID == w.SegmentID {
			digest := executionDigest(ctx)
			if digest == "" {
				config, err := LoadExecutionConfig(ctx, tx, w.JobID, ExecutionDefinitionReadLimit)
				if err != nil {
					return false, err
				}
				digest, err = ExecutionSnapshotDigest(config)
				if err != nil {
					return false, err
				}
			}
			if digest != r.SnapshotDigest {
				return false, ErrManifest
			}
			resourceRow, err := tx.Resource.Query().Where(resource.IDEQ(w.ResourceID)).Select(resource.FieldID, resource.FieldSourceGeneration, resource.FieldCurrentSourceRevisionID).Only(ctx)
			if err != nil {
				return false, err
			}
			m := members[i]
			if m.SourceGeneration != resourceRow.SourceGeneration || !sameInt(m.SourceRevisionID, resourceRow.CurrentSourceRevisionID) {
				return false, ErrStale
			}
			// The baseline is a byte digest, so comparison belongs in SQL. No
			// source/meta/quality JSON or target body is copied into the retry path.
			segmentMatches, err := tx.Segment.Query().Where(segment.IDEQ(w.SegmentID), segment.ContentVersionEQ(m.BaselineVersion), segment.StatusEQ(segment.Status(m.BaselineStatus))).Exist(ctx)
			if err != nil {
				return false, err
			}
			baseline, err := promotionBaselineMatchesSQL(ctx, tx, w.SegmentID, m)
			if err != nil {
				return false, err
			}
			if !segmentMatches || !baseline {
				return false, ErrStale
			}
			return true, nil
		}
	}
	return false, ErrManifest
}

// ExecutionSnapshotDigest uses the same current typed snapshot encoding as the
// worker, rather than hashing the database's differently ordered JSON map.
func ExecutionSnapshotDigest(config map[string]any) (string, error) {
	if err := execution.ValidateExecutionConfigFormat(config); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return "", err
	}
	var spec execution.JobExecutionSnapshot
	if err := json.Unmarshal(encoded, &spec); err != nil {
		return "", err
	}
	encoded, err = json.Marshal(&spec)
	if err != nil {
		return "", err
	}
	return payloadDigest(encoded), nil
}

// PromoteMember transfers one raw member to the existing candidate contract.
// The candidate, retry cursor, request cursor and target cleanup commit together.
func (s *Store) PromoteMember(ctx context.Context, in MemberPromotion) error {
	if in.Cursor < 0 || in.RequestID == "" {
		return ErrManifest
	}
	ctx = operationContext(ctx)
	err := Transaction(ctx, s.client, func(tx *ent.Client) error {
		if err := guardScope(ctx, tx, in.Scope, false); err != nil {
			return err
		}
		if err := guardExecution(ctx, tx, in.Scope.JobID); err != nil {
			return err
		}
		r, err := projectedRequest(tx.WorkRequest.Query().Where(workrequest.IdentityEQ(in.RequestID))).Only(ctx)
		if err != nil {
			return err
		}
		members, err := decodePromotionMembers(r)
		if err != nil {
			return err
		}
		if r.JobID != in.Scope.JobID || r.ResourceID != in.Scope.ResourceID || r.JobRoundID != in.Scope.RoundID || r.State != "completed" || in.Cursor >= len(members) {
			return ErrManifest
		}
		if r.PromotionCursor > in.Cursor {
			return verifyPromotedMember(ctx, tx, r, members[in.Cursor], in)
		}
		if r.PromotionCursor != in.Cursor {
			return ErrCandidateVersion
		}
		m := members[in.Cursor]
		w, err := member(ctx, tx, in.Scope, m.SegmentID)
		if err != nil {
			return err
		}
		if w.ResponseRequestID != in.RequestID || w.State != "response_pending" {
			return ErrManifest
		}
		if m.SourceGeneration != in.Scope.SourceGeneration || !sameInt(m.SourceRevisionID, in.Scope.SourceRevisionID) {
			return ErrStale
		}
		status := byte(2)
		if in.Candidate != nil {
			c := *in.Candidate
			if c.ID != m.CandidateID || c.WorkID != m.WorkID || c.SegmentID != m.SegmentID || c.ParentRequestID != r.Identity || c.SnapshotDigest != r.SnapshotDigest || c.Version != 1 || c.DTOVersion != 2 || c.BaselineVersion != m.BaselineVersion || !promotionCandidateBaselineMatches(m, c) || c.BaselineStatus != m.BaselineStatus {
				return ErrManifest
			}
			c.Scope = in.Scope
			if err := saveCandidate(ctx, tx, c); err != nil {
				return err
			}
			status = 1
			if err := transferRuntimeBindingTx(ctx, tx, in.Scope.JobID, in.RequestID, c.ID); err != nil {
				return err
			}
		}
		next := in.Next
		if in.Candidate != nil {
			next.State = "candidate"
		}
		if err := saveCursor(ctx, tx, in.Scope, m.SegmentID, next, make(map[int]attemptCounters)); err != nil {
			return err
		}
		if err := tx.WorkItem.UpdateOneID(w.ID).SetResponseRequestID("").Exec(ctx); err != nil {
			return err
		}
		if r.CorrectedTargetBytes > 0 {
			corrected, err := tx.WorkRequest.Query().Where(workrequest.IDEQ(r.ID), workrequest.CorrectedTargetBytesLTE(r.BusinessBound)).Select(workrequest.FieldID, workrequest.FieldCorrectedTargets, workrequest.FieldCorrectedTargetIndex).Only(ctx)
			if err != nil {
				return err
			}
			r.CorrectedTargets, r.CorrectedTargetIndex = corrected.CorrectedTargets, corrected.CorrectedTargetIndex
		}
		states := append([]byte(nil), r.PromotionStatus...)
		states[in.Cursor] = status
		u := tx.WorkRequest.UpdateOneID(r.ID).SetPromotionCursor(in.Cursor + 1).SetPromotionStatus(states)
		if len(r.CorrectedTargetIndex) > 0 {
			body, index, err := removeCorrectedTarget(r.CorrectedTargets, r.CorrectedTargetIndex, in.Cursor)
			if err != nil {
				return err
			}
			u.SetCorrectedTargets(body).SetCorrectedTargetIndex(index).SetCorrectedTargetBytes(int64(len(body))).SetCorrectedTargetDigest(payloadDigest(body))
		}
		if in.Cursor+1 == len(members) {
			u.SetPromotionState("promoted").ClearFreezePayload().SetFreezeBytes(0).ClearResponsePayload().SetResponseBytes(0).ClearCorrectedTargets().ClearCorrectedTargetIndex().SetCorrectedTargetBytes(0).SetBusinessBound(0)
		}
		if err := u.Exec(ctx); err != nil {
			return err
		}
		if in.Cursor+1 == len(members) {
			return clearRuntimeBindingTx(ctx, tx, in.Scope.JobID, in.RequestID, true)
		}
		return nil
	})
	return s.finishUnknown(ctx, in.Scope.JobID, err, func(ctx context.Context, tx *ent.Client) (bool, error) {
		r, err := projectedRequest(tx.WorkRequest.Query().Where(workrequest.IdentityEQ(in.RequestID))).Only(ctx)
		if err != nil {
			return false, err
		}
		members, err := decodePromotionMembers(r)
		if err != nil || in.Cursor >= len(members) {
			return false, ErrManifest
		}
		if r.PromotionCursor <= in.Cursor {
			return false, nil
		}
		if err := verifyPromotedMember(ctx, tx, r, members[in.Cursor], in); err != nil {
			return false, err
		}
		return true, nil
	})
}

func verifyPromotedMember(ctx context.Context, tx *ent.Client, r *ent.WorkRequest, m PromotionMember, in MemberPromotion) error {
	w, err := member(ctx, tx, in.Scope, m.SegmentID)
	if err != nil {
		return err
	}
	if w.ResponseRequestID != "" {
		return errUnverifiable
	}
	if in.Candidate == nil {
		if r.PromotionStatus[in.Cursor] != 2 {
			return errUnverifiable
		}
		return nil
	}
	c := in.Candidate
	row, err := tx.WorkCandidate.Query().Where(workcandidate.IdentityEQ(m.CandidateID)).Select(candidateSaveFields...).Only(ctx)
	if err != nil {
		return err
	}
	if r.PromotionStatus[in.Cursor] != 1 || row.WorkItemID != w.ID || row.Version < c.Version || row.ParentRequestID != r.Identity || row.SnapshotDigest != c.SnapshotDigest {
		return errUnverifiable
	}
	if row.Version == c.Version {
		equal, err := candidatePayloadMatches(ctx, tx, row, c.Payload, candidatePayloadLimit(*c))
		if err != nil {
			return err
		}
		if !equal {
			return errUnverifiable
		}
	}
	return nil
}

// guardPromotedResponse proves that a main raw saved before Retry still owns
// this exact member and baseline. It does not authorize a late old invocation.
func guardPromotedResponse(ctx context.Context, tx *ent.Client, c Candidate) (bool, error) {
	w, err := member(ctx, tx, c.Scope, c.SegmentID)
	if err != nil {
		return false, err
	}
	if w.ResponseRequestID == "" || w.ResponseRequestID != c.ParentRequestID {
		return false, nil
	}
	r, err := projectedRequest(tx.WorkRequest.Query().Where(workrequest.IdentityEQ(w.ResponseRequestID))).Only(ctx)
	if err != nil {
		return false, err
	}
	members, err := decodePromotionMembers(r)
	if err != nil {
		return false, err
	}
	if r.State != "completed" || r.SnapshotDigest != c.SnapshotDigest {
		return false, ErrManifest
	}
	for i := r.PromotionCursor; i < len(members); i++ {
		m := members[i]
		if m.CandidateID == c.ID && m.SegmentID == c.SegmentID && m.WorkID == c.WorkID && m.BaselineVersion == c.BaselineVersion && promotionCandidateBaselineMatches(m, c) && m.BaselineStatus == c.BaselineStatus {
			return true, nil
		}
	}
	return false, ErrManifest
}

// CorrectedTarget reads a member's final UTF-8 text without a second candidate DTO.
func CorrectedTarget(body, index []byte, member int) ([]byte, bool, error) {
	if len(index) == 0 {
		return nil, false, nil
	}
	if member < 0 || member >= len(index)/16 || len(index)%16 != 0 {
		return nil, false, ErrManifest
	}
	start := binary.LittleEndian.Uint64(index[member*16:])
	n := binary.LittleEndian.Uint64(index[member*16+8:])
	if start > uint64(len(body)) || n > uint64(len(body))-start {
		return nil, false, ErrManifest
	}
	return body[start : start+n], true, nil
}

func removeCorrectedTarget(body, index []byte, member int) ([]byte, []byte, error) {
	return glossary.RemoveResponseTarget(body, index, member)
}

func stringDigest(value *string) string {
	h := sha256.New()
	if value != nil {
		_, _ = io.WriteString(h, *value)
	}
	return hex.EncodeToString(h.Sum(nil))
}
func samePromotionBaseline(m PromotionMember, value *string) bool {
	return m.BaselineTargetIsNull == (value == nil) && m.BaselineDigest == stringDigest(value)
}

var requestFactFields = func() []string {
	out := make([]string, 0, len(workrequest.Columns))
	for _, f := range workrequest.Columns {
		switch f {
		case workrequest.FieldFreezePayload, workrequest.FieldResponsePayload, workrequest.FieldCorrectedTargets, workrequest.FieldCorrectedTargetIndex:
			continue
		}
		out = append(out, f)
	}
	return out
}()

func projectedRequest(q *ent.WorkRequestQuery) *ent.WorkRequestQuery {
	return q.Where(func(sel *sql.Selector) { sel.Where(requestMetadataBounds(sel)) }).Select(requestFactFields...).WorkRequestQuery
}

func promotionMembersMatch(ctx context.Context, tx *ent.Client, row *ent.WorkRequest, payload []byte) (bool, error) {
	return tx.WorkRequest.Query().Where(workrequest.IDEQ(row.ID), workrequest.IdentityEQ(row.Identity), func(sel *sql.Selector) {
		sel.Where(storageJSONEquality(sel, workrequest.FieldPromotionMembers, payload, false))
	}).Exist(ctx)
}
