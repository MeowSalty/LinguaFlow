package workstate

import (
	"context"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequestinputmember"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workresponsemember"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
)

func (s *Store) InputMemberIdentity(ctx context.Context, id string, ordinal int) (PromotionMember, error) {
	r, err := memberHeaderQuery(s.client.WorkRequest.Query().Where(workrequest.IdentityEQ(id))).Only(ctx)
	if err != nil {
		return PromotionMember{}, err
	}
	if r.ResponseFormatVersion != execution.ResponseFormatVersion || ordinal < 0 || ordinal >= r.PromotionCount {
		return PromotionMember{}, ErrManifest
	}
	row, err := inputMemberIdentityQuery(s.client.WorkRequestInputMember.Query().Where(workrequestinputmember.RequestIDEQ(r.ID), workrequestinputmember.OrdinalEQ(ordinal))).Only(ctx)
	if err != nil {
		return PromotionMember{}, err
	}
	return inputPromotion(row), nil
}

func inputMemberIdentityQuery(q *ent.WorkRequestInputMemberQuery) *ent.WorkRequestInputMemberQuery {
	return q.Select(workrequestinputmember.FieldID, workrequestinputmember.FieldSegmentID, workrequestinputmember.FieldWorkID, workrequestinputmember.FieldCandidateID, workrequestinputmember.FieldBaselineVersion, workrequestinputmember.FieldBaselineDigest, workrequestinputmember.FieldBaselineTargetIsNull, workrequestinputmember.FieldBaselineStatus, workrequestinputmember.FieldSourceGeneration, workrequestinputmember.FieldSourceRevisionID).WorkRequestInputMemberQuery
}
func pendingMemberResponse(ctx context.Context, tx *ent.Client, w *ent.WorkItem) (bool, error) {
	if w.ResponseRequestID == "" {
		return false, nil
	}
	r, err := memberHeaderQuery(tx.WorkRequest.Query().Where(workrequest.IdentityEQ(w.ResponseRequestID))).Only(ctx)
	if err != nil {
		return false, err
	}
	if r.ResponseFormatVersion != execution.ResponseFormatVersion || r.State != "completed" || r.JobID != w.JobID || r.ResourceID != w.ResourceID || r.JobRoundID != w.JobRoundID || r.PromotionState != "capture_pending" && r.PromotionState != "outputs_ready" && r.PromotionState != "business_ready" {
		return false, ErrManifest
	}
	m, err := inputMemberIdentityQuery(tx.WorkRequestInputMember.Query().Where(workrequestinputmember.RequestIDEQ(r.ID), workrequestinputmember.SegmentIDEQ(w.SegmentID))).Select(workrequestinputmember.FieldOrdinal).Only(ctx)
	if err != nil {
		return false, err
	}
	if m.Ordinal < r.PromotionCursor || m.Ordinal >= r.PromotionCount || m.WorkID != WorkIdentity(Scope{JobID: w.JobID, RoundID: w.JobRoundID}, w.SegmentID) {
		return false, ErrManifest
	}
	pending := r.PromotionState == "capture_pending"
	if !pending {
		pending, err = tx.WorkResponseMember.Query().Where(workresponsemember.RequestIDEQ(r.ID), workresponsemember.OrdinalEQ(m.Ordinal), workresponsemember.StateEQ("pending")).Exist(ctx)
		if err != nil || !pending {
			return pending, err
		}
	}
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
	source, err := tx.Resource.Query().Where(resource.IDEQ(w.ResourceID)).Select(resource.FieldID, resource.FieldSourceGeneration, resource.FieldCurrentSourceRevisionID).Only(ctx)
	if err != nil {
		return false, err
	}
	if m.SourceGeneration != source.SourceGeneration || !sameInt(m.SourceRevisionID, source.CurrentSourceRevisionID) {
		return false, ErrStale
	}
	segmentMatches, err := tx.Segment.Query().Where(segment.IDEQ(w.SegmentID), segment.ContentVersionEQ(m.BaselineVersion), segment.StatusEQ(segment.Status(m.BaselineStatus))).Exist(ctx)
	if err != nil {
		return false, err
	}
	baseline, err := promotionBaselineMatchesSQL(ctx, tx, w.SegmentID, inputPromotion(m))
	if err != nil {
		return false, err
	}
	if !segmentMatches || !baseline {
		return false, ErrStale
	}
	return true, nil
}
