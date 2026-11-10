package workstate

import (
	"context"
	"encoding/json"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
)

// SaveResponseBusinessTx is called inside the same Job transaction that absorbs
// glossary entries. It preserves only final UTF-8 targets and a compact receipt.
func SaveResponseBusinessTx(ctx context.Context, tx *ent.Client, row *ent.WorkRequest, body, index, receipt []byte) error {
	if row.ResponseFormatVersion != 1 || row.State != "completed" || row.PromotionState != "response_pending" || row.PromotionCursor != 0 || int64(len(body)) > row.BusinessBound || len(index) != row.PromotionCount*16 || len(receipt) > 32<<10 {
		return ErrManifest
	}
	var compact glossary.ResponseReceipt
	if err := json.Unmarshal(receipt, &compact); err != nil || compact.Version != glossary.ResponseReceiptVersion || compact.RequestID != row.Identity {
		return ErrManifest
	}
	if row.BusinessBound > 0 {
		j, err := tx.Job.Get(ctx, row.JobID)
		if err != nil {
			return err
		}
		state, err := decodeRuntimeState(j.PipelineRuntimeState)
		if err != nil {
			return err
		}
		if state.BusinessOwner != "" && state.BusinessOwner != row.Identity {
			return backend.ErrProgressRequired
		}
		state.BusinessOwner = row.Identity
		if err := saveRuntimeState(ctx, tx, row.JobID, state); err != nil {
			return err
		}
	}
	return tx.WorkRequest.UpdateOneID(row.ID).SetCorrectedTargets(body).SetCorrectedTargetIndex(index).SetCorrectedTargetBytes(int64(len(body))).SetCorrectedTargetDigest(payloadDigest(body)).SetGlossaryReceipt(receipt).SetPromotionState("glossary_processed").Exec(ctx)
}
