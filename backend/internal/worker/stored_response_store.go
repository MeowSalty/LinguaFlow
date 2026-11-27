package worker

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

var _ pipeline.StoredResponseStore = (*roundStore)(nil)

func (s *roundStore) RecoverStoredOwnership(ctx context.Context, consumer *pipeline.WindowConsumer) error {
	runtime := pipeline.ExecutionRuntimeFromContext(ctx)
	for after := 0; ; {
		members, next, err := s.store.LoadRoundOwnedMemberHeaders(ctx, s.scope, after, 16)
		if err != nil || len(members) == 0 {
			return s.classify(err)
		}
		for _, member := range members {
			if _, ok := s.segmentIndex(member.SegmentID); !ok {
				return permanentStoreError{workstate.ErrManifest}
			}
			if err := runtime.Window.RestoreRaw(member.Identity, 1, s.scope.ResourceID, s.scope.RoundID, consumer); err != nil {
				return err
			}
		}
		after = next
	}
}

func (s *roundStore) SettleStoredResponse(ctx context.Context, doc *pipeline.Document, in pipeline.StoredResponseSettlement) error {
	if in.Install != nil || in.Apply != nil || in.Header.ResourceID != s.scope.ResourceID || in.Header.RoundID != s.scope.RoundID || in.Header.SnapshotDigest != s.digest {
		return permanentStoreError{workstate.ErrManifest}
	}
	settlement := workstate.StoredResponseSettlement{
		Scope: s.scope, RequestID: in.Header.ID, Digest: in.Header.ResponseDigest, Mode: in.Header.Mode,
		SnapshotDigest: s.digest, CaptureEpoch: in.Header.CaptureEpoch, GlossaryReceipt: in.GlossaryReceipt,
	}
	for _, d := range in.Dispositions {
		if d.Accepted && in.Header.Mode != pipeline.RoundModeExtract && in.Result == nil {
			return permanentStoreError{workstate.ErrManifest}
		}
		id, ok := s.segmentID(d.Index)
		if !ok || id != d.SegmentID {
			return permanentStoreError{workstate.ErrManifest}
		}
		settlement.Dispositions = append(settlement.Dispositions, workstate.StoredResponseDisposition{SegmentID: id, Cursor: d.Cursor, Accepted: d.Accepted, Reason: d.Reason})
	}
	var apply func(context.Context, *ent.Client, workstate.PromotionMember) (bool, error)
	if in.Header.Mode != pipeline.RoundModeExtract && in.Result != nil {
		if doc == nil {
			return permanentStoreError{workstate.ErrManifest}
		}
		var err error
		apply, err = s.storedQualityWriter(ctx, doc, in.Header.Mode, in.Result.PreparedPayload)
		if err != nil {
			return permanentStoreError{err}
		}
	}
	err := s.store.SettleStoredResponse(ctx, settlement, apply)
	if errors.Is(err, workstate.ErrStale) && in.Header.Mode == pipeline.RoundModeExtract {
		return errors.Join(pipeline.ErrStoredResponseStale, s.classify(err))
	}
	if err != nil {
		return s.classify(err)
	}
	for i, d := range settlement.Dispositions {
		in.Dispositions[i].Accepted, in.Dispositions[i].Cursor, in.Dispositions[i].Reason = d.Accepted, d.Cursor, d.Reason
	}
	return nil
}

func (s *roundStore) storedQualityWriter(ctx context.Context, doc *pipeline.Document, mode string, payload []byte) (func(context.Context, *ent.Client, workstate.PromotionMember) (bool, error), error) {
	runtime := pipeline.ExecutionRuntimeFromContext(ctx)
	prefix := []byte("{\"Segments\":[")
	if runtime == nil || !bytes.HasPrefix(payload, prefix) {
		return nil, fmt.Errorf("%w: invalid stored quality payload", workstate.ErrManifest)
	}
	var remaining []byte
	var activeTx *ent.Client
	return func(ctx context.Context, tx *ent.Client, member workstate.PromotionMember) (bool, error) {
		if activeTx != tx {
			activeTx = tx
			remaining = payload[len(prefix):]
		}
		index, ok := s.segmentIndex(member.SegmentID)
		if !ok {
			return false, workstate.ErrManifest
		}
		var result pipeline.TranslatedSegment
		var bounds *backend.BuildBounds
		defer func() { bounds.CloseRetired() }()
		err := runtime.Run(ctx, func() error {
			for len(remaining) > 0 && remaining[0] != ']' {
				b, err := backend.NewBuildBoundsFromContext(ctx, runtime.ResponseBytes, runtime.HandoffBytes)
				if err != nil {
					return err
				}
				raw, used, err := backend.FirstJSONRaw(b, remaining)
				if err == nil {
					result = pipeline.TranslatedSegment{}
					err = b.DecodeJSONLimited(raw, &result, runtime.ResponseBytes)
				}
				if err != nil {
					b.CloseRetired()
					return err
				}
				remaining = remaining[used:]
				if len(remaining) > 0 && remaining[0] == ',' {
					remaining = remaining[1:]
				}
				if result.Index == index {
					if result.ID != doc.Segments[index].ID {
						b.CloseRetired()
						return workstate.ErrManifest
					}
					bounds = b
					return prepareCapturedQualityMember(b, mode, doc.Segments[index], &result)
				}
				b.CloseRetired()
				if result.Index > index {
					break
				}
			}
			return workstate.ErrManifest
		})
		if err != nil {
			return false, err
		}
		return applyCapturedQualityMember(ctx, tx, s.scope, mode, member, result)
	}, nil
}
