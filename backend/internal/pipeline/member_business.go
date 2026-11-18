package pipeline

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
)

func processArchivedGlossary(ctx context.Context, h stagedHandler, header MainResponseHeader, store RoundStore, runtime *ExecutionRuntime, logger *slog.Logger) (MainResponseHeader, error) {
	handler, inline := h.(*TranslateHandler)
	if !inline || !handler.InlineBootstrap || handler.Glossary == nil {
		err := runtime.SaveNamed(ctx, persistence.Metadata{Operation: "business_publish", Class: persistence.Reliable, RequestID: header.ID, ResourceID: storeResourceID(store)}, func(saveCtx context.Context) error {
			return store.PublishBusiness(saveCtx, header.ID, glossary.ResponseBusiness{}, nil)
		})
		if err != nil {
			return header, err
		}
		header.Phase = "business_ready"
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
	defer store.AbortMemberBusiness(ctx, header.ID)
	public, err := store.LoadResponsePublic(ctx, header.ID, runtime.responseLimit())
	if err != nil {
		return header, err
	}
	defer public.Release()
	in := glossary.ResponseProcess{RequestID: header.ID, InputDigest: header.ResponseDigest, TargetLang: header.TargetLang, EntriesPayload: public.Payload, Rewrite: handler.InlineConflictStrategy == InlineConflictRewriteLocal, ItemLimit: runtime.Window.limits.ItemBytes, BusinessLimit: header.BusinessBound, Build: runtime.Run}
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
				e = yield(glossary.ResponseTarget{Ordinal: ordinal, ID: input.PromptID, Text: selected.Target, Failure: selected.Failure})
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
		candidate, e := prepareArchivedCandidate(b, h, header, frozen, selected, logger)
		if e != nil {
			return e
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
	in.Publish = func(memberCtx context.Context, out glossary.ResponseBusiness, install func()) error {
		return store.PublishBusiness(memberCtx, header.ID, out, install)
	}
	if !in.Rewrite {
		in.VisitTargets, in.Validate, in.WriteTarget = nil, nil, nil
	}
	var business glossary.ResponseBusiness
	err = runtime.SaveNamed(ctx, persistence.Metadata{Operation: "glossary_publish", Class: persistence.Reliable, RequestID: header.ID, ResourceID: storeResourceID(store)}, func(saveCtx context.Context) error {
		var e error
		business, e = processor.ProcessResponse(saveCtx, in)
		return e
	})
	defer business.Release()
	if err != nil {
		return header, err
	}
	if err = runtime.Admission.RestoreResponseBusiness(header.ID, correctedBytes); err != nil {
		return header, err
	}
	header.Phase = "business_ready"
	header.OutputMemberBytes += correctedBytes
	header.CorrectedBytes = header.OutputMemberBytes
	return header, nil
}
