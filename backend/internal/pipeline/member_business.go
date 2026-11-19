package pipeline

import (
	"context"
	"errors"
	"fmt"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"
	"log/slog"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
)

func processArchivedGlossary(ctx context.Context, h stagedHandler, header MainResponseHeader, store RoundStore, runtime *ExecutionRuntime, logger *slog.Logger) (confirmed MainResponseHeader, resultErr error) {
	handler, inline := h.(*TranslateHandler)
	if !inline || !handler.InlineBootstrap || handler.Glossary == nil {
		err := runtime.SaveNamed(ctx, persistence.Metadata{Operation: string(persistence.BusinessPublication), Class: persistence.Reliable, RequestID: header.ID, ResourceID: storeResourceID(store)}, func(saveCtx context.Context) error {
			confirmed, confirmationErr := store.PublishBusiness(saveCtx, header.ID, glossary.ResponseBusiness{}, nil)
			if confirmationErr == nil {
				header = confirmed
			}
			return confirmationErr
		})
		if err != nil {
			return header, err
		}
		if err = retireResponsePublication(ctx, store, header.ID); err != nil {
			return header, err
		}
		if err = runtime.Admission.RestoreResponseOwnership(header.ID, prepareIntent(h, store, backend.RequestStageMain).RoundIndex, storeResourceID(store), header.Facts(), header.Phase, true); err != nil {
			return header, err
		}
		return header, nil
	}
	granted, err := runtime.Admission.TryBusinessOwner(header.ID)
	if err != nil {
		return header, err
	}
	if !granted {
		granted, err = runtime.TransferBusiness(ctx, runtime.Admission.RuntimeState().BusinessOwner, header.ID, storeResourceID(store), prepareIntent(h, store, backend.RequestStageMain).RoundIndex)
		if err != nil {
			return header, err
		}
		if !granted {
			return header, &WorkDeferred{Wait: backend.AdmissionWait{Reason: "business_lane", CapacityChanged: runtime.Admission.Changed()}}
		}
	}
	processor, ok := handler.Glossary.(glossary.ResponseProcessor)
	if !ok {
		return header, fmt.Errorf("glossary adapter has no atomic response processor")
	}
	if file, ok := handler.Glossary.(*glossary.FileGlossary); ok {
		if err = file.BindAllocationOwner(ctx, runtime.Admission); err != nil {
			return header, err
		}
	}
	defer func() {
		if header.Phase == responsecontract.OutputsReady {
			resultErr = errors.Join(resultErr, store.AbortMemberBusiness(ctx, header.ID))
		}
	}()
	public, err := store.LoadResponsePublic(ctx, header.ID, runtime.responseLimit())
	if err != nil {
		return header, err
	}
	defer func() {
		if public.Release != nil {
			public.Release()
		}
	}()
	in := glossary.ResponseProcess{RequestID: header.ID, CaptureEpoch: header.CaptureEpoch, InputDigest: header.ResponseDigest, TargetLang: header.TargetLang, EntriesPayload: public.Payload, Rewrite: handler.InlineConflictStrategy == InlineConflictRewriteLocal, ItemLimit: runtime.Window.limits.ItemBytes, BusinessLimit: header.BusinessBound, Build: runtime.Run}
	var correctedBytes int64
	in.VisitTargets = func(memberCtx context.Context, yield func(glossary.ResponseTarget) error) error {
		correctedBytes = 0
		for ordinal := 0; ordinal < header.MemberCount; ordinal++ {
			key := archiveMemberKey(header, ordinal)
			input, e := store.LoadInputMember(memberCtx, key, runtime.handoffLimit())
			if e != nil {
				return e
			}
			output, e := store.LoadOutputMember(memberCtx, key, false, in.ItemLimit)
			if e != nil {
				input.Release()
				return e
			}
			b, e := backend.NewBuildBoundsFromContext(memberCtx, in.ItemLimit, output.Bytes)
			if e != nil {
				input.Release()
				output.Release()
				return e
			}
			var selected archivedOutput
			e = b.DecodeJSON(output.Payload, &selected)
			if e == nil {
				e = yield(glossary.ResponseTarget{Ordinal: ordinal, ID: input.PromptID, Text: selected.Target, Failure: selected.Failure, Outcome: selected.Outcome})
			}
			selected = archivedOutput{}
			input.Release()
			output.Release()
			b.CloseRetired()
			if e != nil {
				return e
			}
		}
		return nil
	}
	in.Validate = func(memberCtx context.Context, target glossary.ResponseTarget) error {
		if !in.Rewrite {
			return nil
		}
		key := archiveMemberKey(header, target.Ordinal)
		input, e := store.LoadInputMember(memberCtx, key, runtime.handoffLimit())
		if e != nil {
			return e
		}
		defer input.Release()
		output, e := store.LoadOutputMember(memberCtx, key, false, in.ItemLimit)
		if e != nil {
			return e
		}
		defer output.Release()
		b, e := backend.NewBuildBoundsFromContext(memberCtx, in.ItemLimit, input.Bytes+output.Bytes)
		if e != nil {
			return e
		}
		defer b.CloseRetired()
		var frozen frozenMainMember
		var selected archivedOutput
		if e = b.DecodeJSON(input.Payload, &frozen); e != nil {
			return e
		}
		if e = b.DecodeJSON(output.Payload, &selected); e != nil {
			return e
		}
		selected.Target = target.Text
		if e = restoreMetadataTypes(&frozen.Segment, frozen.MetaTypes, b); e != nil {
			return e
		}
		memberHeader := header
		memberHeader.Cursor = target.Ordinal
		candidate, e := prepareArchivedCandidate(b, h, memberHeader, frozen, selected, logger)
		if e != nil {
			return e
		}
		measured, e := b.MeasureJSON(candidate)
		if e != nil {
			return e
		}
		if measured > in.ItemLimit {
			return &responsecontract.MemberContentError{Ordinal: target.Ordinal, Kind: responsecontract.MemberTooLarge, Limit: in.ItemLimit, Observed: measured}
		}
		return candidate.SealAndCompact(b, in.ItemLimit)
	}
	in.WriteTarget = func(memberCtx context.Context, target glossary.ResponseTarget) error {
		if !in.Rewrite {
			return nil
		}
		key := archiveMemberKey(header, target.Ordinal)
		frame, e := store.LoadOutputMember(memberCtx, key, false, in.ItemLimit)
		if e != nil {
			return e
		}
		defer frame.Release()
		b, e := backend.NewBuildBoundsFromContext(memberCtx, in.ItemLimit, frame.Bytes)
		if e != nil {
			return e
		}
		defer b.CloseRetired()
		var selected archivedOutput
		if e = b.DecodeJSON(frame.Payload, &selected); e != nil {
			return e
		}
		selected.Target, selected.Failure = target.Text, target.Failure
		selected.Outcome = target.Outcome
		payload, e := b.EncodeJSON(selected, in.ItemLimit)
		if e != nil {
			return e
		}
		allocation, e := b.RetainedAllocation(int64(cap(payload)))
		if e != nil {
			return e
		}
		defer allocation.Release()
		if e = allocation.DetachBuild(); e != nil {
			return e
		}
		e = store.StageCorrectedMember(memberCtx, OutputMemberFrame{Key: key, Payload: payload, Bytes: int64(len(payload)), Digest: memberResponseDigest(ctx, payload), Allocation: allocation})
		if e != nil {
			return e
		}
		correctedBytes += int64(len(payload))
		return nil
	}
	in.Publish = func(memberCtx context.Context, out glossary.ResponseBusiness, prepared *responsecontract.PreparedPublication) error {
		if prepared == nil || prepared.Install == nil {
			return fmt.Errorf("missing publication preparation")
		}
		var install func()
		if store.PublicationDomain().Medium == responsecontract.PublicationMemory {
			install = func() { _ = prepared.InstallOnce() }
		}
		confirmed, confirmationErr := store.PublishBusiness(memberCtx, header.ID, out, install)
		if confirmationErr == nil && store.PublicationDomain().Medium == responsecontract.PublicationMemory {
			header = confirmed
		}
		return confirmationErr
	}
	if !in.Rewrite {
		in.VisitTargets, in.Validate, in.WriteTarget = nil, nil, nil
	}
	var business glossary.ResponseBusiness
	err = runtime.SaveNamed(ctx, persistence.Metadata{Operation: string(persistence.BusinessPublication), Class: persistence.Reliable, RequestID: header.ID, ResourceID: storeResourceID(store)}, func(saveCtx context.Context) error {
		var e error
		business, e = processor.ProcessResponse(saveCtx, in)
		return e
	})
	defer business.Release()
	in.EntriesPayload, in.Entries = nil, nil
	public.Payload = nil
	public.Release()
	public = OutputMemberFrame{}
	// ProcessResponse has exited every dictionary and store publication lock.
	// An installed receipt remains authoritative even if old ownership retires
	// with an error; pending refund rights stay in the store for the next retry.
	err = errors.Join(err, retireResponsePublication(ctx, store, header.ID))
	if store.PublicationDomain().Medium == responsecontract.PublicationSQL {
		// The publisher executes inside the glossary transaction. Its header is
		// provisional until COMMIT or an authoritative unknown-ACK lookup succeeds.
		// A receipt replay can succeed without invoking the publisher again.
		confirmationDeadline := time.Now().Add(5 * time.Second)
		if deadline := persistence.Deadline(ctx); !deadline.IsZero() && deadline.Before(confirmationDeadline) {
			confirmationDeadline = deadline
		}
		factCtx, finish := context.WithDeadline(context.WithoutCancel(ctx), confirmationDeadline)
		confirmed, confirmationErr := store.ResponseHeader(factCtx, header.ID)
		finish()
		if confirmationErr != nil {
			return header, errors.Join(err, confirmationErr)
		}
		identityMatches := confirmed.DatabaseID == header.DatabaseID && confirmed.ID == header.ID && confirmed.ResourceID == header.ResourceID && confirmed.RoundID == header.RoundID && confirmed.FormatVersion == header.FormatVersion && confirmed.MemberCount == header.MemberCount && confirmed.CaptureEpoch == header.CaptureEpoch && confirmed.ResponseDigest == header.ResponseDigest && confirmed.SnapshotDigest == header.SnapshotDigest && confirmed.FreezeDigest == header.FreezeDigest
		if !identityMatches {
			return header, errors.Join(err, fmt.Errorf("business publication confirmation identity changed"))
		}
		if confirmed.Phase == responsecontract.BusinessReady || confirmed.Phase == responsecontract.Promoted {
			header = confirmed
		} else if err == nil {
			return header, fmt.Errorf("business publication confirmation phase changed")
		}
	}
	if err != nil {
		return header, err
	}
	if err = runtime.Admission.RestoreResponseOwnership(header.ID, prepareIntent(h, store, backend.RequestStageMain).RoundIndex, storeResourceID(store), header.Facts(), header.Phase, true); err != nil {
		return header, err
	}
	return header, nil
}

func retireResponsePublication(ctx context.Context, store RoundStore, id string) error {
	if retirement, ok := store.(interface {
		RetireResponse(context.Context, string) error
	}); ok {
		return retirement.RetireResponse(ctx, id)
	}
	return nil
}
