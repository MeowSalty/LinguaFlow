package workstate

import (
	"context"
	"encoding/json"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/predicate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequestinputmember"
	"github.com/MeowSalty/LinguaFlow/backend/internal/model"
)

type StoredResponseDisposition struct {
	SegmentID int
	Cursor    model.RoundWorkCursor
	Accepted  bool
	Reason    string
}

type StoredResponseSettlement struct {
	Scope
	RequestID, Digest, Mode, SnapshotDigest string
	CaptureEpoch                            int64
	Dispositions                            []StoredResponseDisposition
	GlossaryReceipt                         []byte
}

type storedSettlementProof struct {
	Scope
	requestID, digest, mode, snapshotDigest, receiptDigest string
	captureEpoch                                           int64
	memberCount                                            int
}

func (in StoredResponseSettlement) proof() storedSettlementProof {
	p := storedSettlementProof{Scope: in.Scope, requestID: in.RequestID, digest: in.Digest, mode: in.Mode, snapshotDigest: in.SnapshotDigest, captureEpoch: in.CaptureEpoch, memberCount: len(in.Dispositions)}
	if len(in.GlossaryReceipt) > 0 {
		p.receiptDigest = payloadDigest(in.GlossaryReceipt)
	}
	return p
}

func retireUncapturedStoredInputs(ctx context.Context, tx *ent.Client, jobID int, filter predicate.WorkRequest) error {
	after := 0
	for {
		rows, err := tx.WorkRequest.Query().Where(filter, workrequest.JobIDEQ(jobID), workrequest.IDGT(after), workrequest.PromotionStateEQ("inputs_ready"), workrequest.MemberModeIn("semantic_qa", "adjudicate", "extract")).Select(workrequest.FieldID, workrequest.FieldInputMemberBytes).Order(ent.Asc(workrequest.FieldID)).Limit(16).All(ctx)
		if err != nil || len(rows) == 0 {
			return err
		}
		for _, row := range rows {
			if _, err := tx.WorkRequestInputMember.Update().Where(workrequestinputmember.RequestIDEQ(row.ID)).ClearPayload().SetPayloadBytes(0).Save(ctx); err != nil {
				return err
			}
			if err := tx.WorkRequest.UpdateOneID(row.ID).SetPromotionState("cancelled").SetInputMemberBytes(0).SetRemainingMembers(0).SetBusinessBound(0).AddMemberStorageBytes(-row.InputMemberBytes).Select(workrequest.FieldID).Exec(ctx); err != nil {
				return err
			}
			after = row.ID
		}
	}
}

// SettleStoredResponse acknowledges the captured batch in the same transaction
// as its domain writes and checkpoints. Glossary publication supplies the
// transaction already holding the job lock.
func (s *Store) SettleStoredResponse(ctx context.Context, in StoredResponseSettlement, apply func(context.Context, *ent.Client, PromotionMember) (bool, error)) error {
	if tx, ok := ctx.Value(businessTransactionKey{}).(*ent.Client); ok {
		return SettleStoredResponseTx(ctx, tx, in, apply)
	}
	ctx = operationContext(ctx)
	check := in.proof().exists
	ctx = WithCommitConfirmation(ctx, s.client, in.JobID, check)
	err := Transaction(ctx, s.client, func(tx *ent.Client) error {
		if err := LockJob(ctx, tx, in.JobID); err != nil {
			return err
		}
		return SettleStoredResponseTx(ctx, tx, in, apply)
	})
	return s.finishUnknown(ctx, in.JobID, err, check)
}

func (p storedSettlementProof) request(ctx context.Context, tx *ent.Client) (*ent.WorkRequest, error) {
	switch p.mode {
	case "semantic_qa", "adjudicate", "extract":
	default:
		return nil, ErrManifest
	}
	row, err := memberRequest(ctx, tx, MemberKey{RequestID: p.requestID, CaptureEpoch: p.captureEpoch})
	if err != nil {
		return nil, err
	}
	if row.JobID != p.JobID || row.ResourceID != p.ResourceID || row.JobRoundID != p.RoundID || row.State != "completed" || row.MemberMode != p.mode || row.SnapshotDigest != p.snapshotDigest || row.ResponseDigest != p.digest || row.PromotionCount != p.memberCount {
		return nil, ErrManifest
	}
	return row, nil
}

func (p storedSettlementProof) exists(ctx context.Context, tx *ent.Client) (bool, error) {
	row, err := p.request(ctx, tx)
	if err != nil {
		return false, err
	}
	return p.confirmed(row)
}

func (p storedSettlementProof) confirmed(row *ent.WorkRequest) (bool, error) {
	if row.PromotionState == "capture_pending" {
		return false, nil
	}
	if row.PromotionState != "promoted" || row.PromotionCursor != row.PromotionCount || row.ArchiveDigest != row.ResponseDigest || row.RemainingMembers != 0 || row.CaptureBytes != 0 || row.InputMemberBytes != 0 || p.receiptDigest != "" && row.PublicDigest != p.receiptDigest {
		return false, ErrManifest
	}
	return true, nil
}

func SettleStoredResponseTx(ctx context.Context, tx *ent.Client, in StoredResponseSettlement, apply func(context.Context, *ent.Client, PromotionMember) (bool, error)) error {
	if in.Mode != "extract" && len(in.GlossaryReceipt) > 0 {
		return ErrManifest
	}
	if err := validateResponseReceipt(in.GlossaryReceipt, in.RequestID); err != nil {
		return err
	}
	round, err := guardLockedScope(ctx, tx, in.Scope, false, true)
	if err != nil {
		return err
	}
	if !round.ManifestSealed {
		return ErrManifest
	}
	if err := guardExecution(ctx, tx, in.JobID); err != nil {
		return err
	}
	proof := in.proof()
	row, err := proof.request(ctx, tx)
	if err != nil {
		return err
	}
	if done, err := proof.confirmed(row); done || err != nil {
		return err
	}
	if row.PromotionCursor != 0 || row.OutputMemberBytes != 0 || row.PublicBytes != 0 {
		return ErrManifest
	}
	_, err = ConfirmIDStream(ctx, tx, in.JobID, in.RoundID, func(confirm func([]int) error) error {
		for start := 0; start < len(in.Dispositions); start += 16 {
			end := min(start+16, len(in.Dispositions))
			inputs, err := inputMemberIdentityQuery(tx.WorkRequestInputMember.Query().Where(workrequestinputmember.RequestIDEQ(row.ID), workrequestinputmember.OrdinalGTE(start), workrequestinputmember.OrdinalLT(end))).Select(workrequestinputmember.FieldOrdinal).Order(ent.Asc(workrequestinputmember.FieldOrdinal)).All(ctx)
			if err != nil {
				return err
			}
			if len(inputs) != end-start {
				return ErrManifest
			}
			var accepted [16]int
			n := 0
			for i, input := range inputs {
				d := &in.Dispositions[start+i]
				if input.Ordinal != start+i || input.SegmentID != d.SegmentID || input.WorkID != WorkIdentity(in.Scope, d.SegmentID) || input.SourceGeneration != in.SourceGeneration || !sameInt(input.SourceRevisionID, in.SourceRevisionID) {
					return ErrManifest
				}
				ack, err := settleStoredMember(ctx, tx, in, row, inputPromotion(input), d, apply)
				if err != nil {
					return err
				}
				if ack {
					accepted[n] = d.SegmentID
					n++
				}
			}
			if err := confirm(accepted[:n]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	changed, err := tx.WorkRequestInputMember.Update().Where(workrequestinputmember.RequestIDEQ(row.ID)).ClearPayload().SetPayloadBytes(0).Save(ctx)
	if err != nil {
		return err
	}
	if changed != row.PromotionCount {
		return ErrManifest
	}
	u := tx.WorkRequest.UpdateOneID(row.ID).SetPromotionState("promoted").SetPromotionCursor(row.PromotionCount).SetArchiveDigest(row.ResponseDigest).SetRemainingMembers(0).ClearCapturePayload().SetCaptureBytes(0).SetInputMemberBytes(0).SetBusinessBound(0).AddMemberStorageBytes(-row.CaptureBytes - row.InputMemberBytes + int64(len(in.GlossaryReceipt)))
	if len(in.GlossaryReceipt) > 0 {
		u.SetGlossaryReceipt(in.GlossaryReceipt).SetPublicDigest(payloadDigest(in.GlossaryReceipt))
	}
	if err := u.Select(workrequest.FieldID).Exec(ctx); err != nil {
		return err
	}
	return clearRuntimeBindingTx(ctx, tx, in.JobID, in.RequestID, true)
}

func settleStoredMember(ctx context.Context, tx *ent.Client, in StoredResponseSettlement, request *ent.WorkRequest, input PromotionMember, disposition *StoredResponseDisposition, apply func(context.Context, *ent.Client, PromotionMember) (bool, error)) (bool, error) {
	w, err := member(ctx, tx, in.Scope, input.SegmentID)
	if err != nil {
		return false, err
	}
	if w.State != "response_pending" || w.ResponseRequestID != in.RequestID || w.CandidateID != "" || request.RetryEpoch == in.RetryEpoch && (request.MemberPool != w.PoolIndex || request.MemberAttempt+1 != w.MainAttempts) {
		return false, ErrManifest
	}
	accepted := disposition.Accepted
	if accepted {
		matches, err := tx.Segment.Query().Where(segment.IDEQ(input.SegmentID), segment.ContentVersionEQ(input.BaselineVersion), segment.StatusEQ(segment.Status(input.BaselineStatus))).Exist(ctx)
		if err != nil {
			return false, err
		}
		if matches {
			matches, err = promotionBaselineMatchesSQL(ctx, tx, input.SegmentID, input)
			if err != nil {
				return false, err
			}
		}
		if matches && apply != nil {
			matches, err = apply(ctx, tx, input)
			if err != nil {
				return false, err
			}
		}
		if !matches && in.Mode == "extract" {
			return false, ErrStale
		}
		if !matches {
			accepted = false
			disposition.Accepted = false
			disposition.Reason = "captured member baseline changed"
			disposition.Cursor = model.RoundWorkCursor{Pool: w.PoolIndex, Attempt: w.MainAttempts, State: "stale"}
		}
	}
	if !accepted {
		next := disposition.Cursor
		if request.RetryEpoch != in.RetryEpoch && next.State != "stale" {
			next = model.RoundWorkCursor{Pool: w.PoolIndex, Attempt: w.MainAttempts, State: "pending"}
		}
		cursor := cursorFromRow(w)
		if next.Pool > w.PoolIndex {
			cursor = Cursor{PoolIndex: next.Pool}
		}
		cursor.State, cursor.MainAttempts, cursor.PromptPhase = next.State, max(cursor.MainAttempts, next.Attempt), next.Phase
		cursor.LastError = disposition.Reason
		cursor.Data, err = json.Marshal(next)
		if err != nil {
			return false, err
		}
		cursor.NextAttemptAt = nil
		if !next.NextAttemptAt.IsZero() {
			at := next.NextAttemptAt.UTC().Truncate(time.Microsecond)
			cursor.NextAttemptAt = &at
		}
		if err := saveCursorRows(ctx, tx, in.Scope, []CursorTransition{{SegmentID: input.SegmentID, Cursor: cursor}}, map[int]*ent.WorkItem{input.SegmentID: w}); err != nil {
			return false, err
		}
	}
	u := tx.WorkItem.UpdateOneID(w.ID).SetResponseRequestID("")
	if accepted {
		u.SetState("resolved").SetMemberOwned(false).ClearNextAttemptAt().SetLastError("")
	}
	return accepted, u.Select(workitem.FieldID).Exec(ctx)
}
