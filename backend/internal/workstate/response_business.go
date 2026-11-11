package workstate

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"io"
	"unicode/utf8"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
)

const responseReceiptReadBytes = 2 << 10

// SaveResponseBusinessTx is called inside the same Job transaction that absorbs
// glossary entries. It preserves only final UTF-8 targets and a compact receipt.
func SaveResponseBusinessTx(ctx context.Context, tx *ent.Client, row *ent.WorkRequest, body, index, receipt []byte) error {
	if row.ResponseFormatVersion != 1 || row.State != "completed" || row.PromotionState != "response_pending" || row.PromotionCursor != 0 || int64(len(body)) > row.BusinessBound || len(index) != row.PromotionCount*16 || len(receipt) == 0 || len(receipt) > responseReceiptReadBytes {
		return ErrManifest
	}
	if err := validateResponseReceipt(receipt, row.Identity); err != nil {
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

func validateResponseReceipt(data []byte, requestID string) error {
	if len(data) == 0 {
		return nil
	}
	if len(data) > responseReceiptReadBytes || !utf8.Valid(data) || !utf8.ValidString(requestID) {
		return ErrManifest
	}
	var compact glossary.ResponseReceipt
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&compact); err != nil {
		return ErrManifest
	}
	var extra json.RawMessage
	if err := d.Decode(&extra); err != io.EOF {
		return ErrManifest
	}
	outcome, err := hex.DecodeString(compact.OutcomeDigest)
	if compact.Version != glossary.ResponseReceiptVersion || compact.RequestID != requestID || len(compact.RequestID) == 0 || len(compact.RequestID) > 128 || !utf8.ValidString(compact.RequestID) || len(compact.InputDigest) == 0 || len(compact.InputDigest) > 128 || !utf8.ValidString(compact.InputDigest) || len(compact.OutcomeDigest) != 64 || err != nil || len(outcome) != 32 || compact.Added < 0 || compact.Skipped < 0 || compact.Added > 65536 || compact.Skipped > 65536 {
		return ErrManifest
	}
	return nil
}
