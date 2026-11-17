package pipeline

import (
	"context"
	"errors"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"log/slog"
	"unicode/utf8"

	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
)

func processFrozenGlossary(ctx context.Context, h *TranslateHandler, frozen frozenMainBatch, response MainResponse, store RoundStore, runtime *ExecutionRuntime, logger *slog.Logger) (MainResponse, error) {
	var scopes []*backend.BuildBounds
	bounds := func(limit, input int64) (*backend.BuildBounds, error) {
		b, err := backend.NewBuildBoundsFromContext(ctx, limit, input)
		if err == nil {
			scopes = append(scopes, b)
		}
		return b, err
	}
	var ids []string
	var parsed repair.Result
	var raw string
	var entriesPayload []byte
	var in glossary.ResponseProcess
	defer func() {
		ids, raw, entriesPayload = nil, "", nil
		parsed = repair.Result{}
		in = glossary.ResponseProcess{}
		for _, b := range scopes {
			b.CloseRetired()
		}
	}()
	err := runtime.Run(ctx, func() error {
		b, e := bounds(runtime.responseLimit(), response.FreezeBytes+response.ResponseBytes)
		if e != nil {
			return e
		}
		if e = backend.ReserveSlice[string](b, int64(len(frozen.Members))); e != nil {
			return e
		}
		ids = make([]string, len(frozen.Members))
		for i, m := range frozen.Members {
			ids[i] = m.PromptID
		}
		if e = b.ReserveText(int64(len(response.Raw)), int64(len(response.Raw))); e != nil {
			return e
		}
		raw = string(response.Raw)
		opt := h.Repair
		opt.Bounds = b
		parsed, e = repair.ParseMainMemberBounded(b, raw, frozen.TextMode, ids, "", opt)
		if e != nil {
			return e
		}
		if parsed.ParseErr != nil {
			return parsed.ParseErr
		}
		var entries []glossary.Entry
		minLen := max(2, h.MinBootstrapSourceLen)
		for _, entry := range parsed.Glos {
			if utf8.RuneCountInString(entry.Source) >= minLen && entry.Source != "" && entry.Target != "" {
				entries, e = backend.AppendNode(b, entries, glossary.Entry{Source: entry.Source, Target: entry.Target, Notes: entry.Notes})
				if e != nil {
					return e
				}
			}
		}
		entriesPayload, e = b.EncodeJSON(entries, runtime.responseLimit())
		parsed = repair.Result{}
		return e
	})
	if err != nil {
		return response, err
	}
	if parsed.ParseErr != nil {
		return response, parsed.ParseErr
	}
	in = glossary.ResponseProcess{RequestID: response.ID, InputDigest: response.ResponseDigest, TargetLang: frozen.TargetLang, EntriesPayload: entriesPayload, Rewrite: h.InlineConflictStrategy == InlineConflictRewriteLocal, ItemLimit: runtime.Window.limits.ItemBytes, BusinessLimit: response.BusinessBound, Build: runtime.Run}
	in.TrackBounds = func(b *backend.BuildBounds) { scopes = append(scopes, b) }
	if err = runtime.Run(ctx, func() error {
		b, e := bounds(0, 0)
		if e != nil {
			return e
		}
		if e = backend.ReserveSlice[glossary.ResponseTarget](b, int64(len(ids))); e != nil {
			return e
		}
		in.Targets = make([]glossary.ResponseTarget, 0, len(ids))
		return nil
	}); err != nil {
		return response, err
	}
	for _, id := range ids {
		target := glossary.ResponseTarget{ID: id}
		err := runtime.Run(ctx, func() error {
			b, e := bounds(in.ItemLimit, response.ResponseBytes+response.FreezeBytes)
			if e != nil {
				return e
			}
			opt := h.Repair
			opt.Bounds = b
			selected, e := repair.ParseMainMemberBounded(b, raw, frozen.TextMode, ids, id, opt)
			if e != nil {
				return e
			}
			if selected.ParseErr != nil {
				return selected.ParseErr
			}
			target.Text = selected.Trans[id]
			return nil
		})
		if err != nil {
			target.Failure = err.Error()
			target.Text = ""
		}
		in.Targets = append(in.Targets, target)
	}
	parsed = repair.Result{}
	raw = ""

	in.Validate = func(_ context.Context, target glossary.ResponseTarget) error {
		if !in.Rewrite {
			return nil
		}
		for i, m := range frozen.Members {
			if m.PromptID == target.ID {
				override := response
				b, e := bounds(in.ItemLimit, response.ResponseBytes+response.FreezeBytes)
				if e != nil {
					return e
				}
				body, index, err := glossary.EncodeResponseBusinessBounded(b, []glossary.ResponseTarget{{Text: target.Text}}, in.ItemLimit)
				if err != nil {
					return err
				}
				override.CorrectedTargets = body
				if err = backend.ReserveSlice[[2]uint64](b, int64(len(ids))); err != nil {
					return err
				}
				override.CorrectedIndex = make([]byte, len(ids)*16)
				copy(override.CorrectedIndex[i*16:], index)
				var full frozenMainBatch
				if err := b.DecodeJSON(response.FreezePayload, &full); err != nil {
					return err
				}
				if i >= len(full.Members) {
					return ErrCandidateCapacity
				}
				member := &full.Members[i]
				if err := restoreMetadataTypes(&member.Segment, member.MetaTypes, b); err != nil {
					return err
				}
				if len(member.RubyItems) > 0 {
					if member.Segment.Meta == nil {
						member.Segment.Meta = make(map[string]any, 1)
					}
					member.Segment.Meta["ruby_items"] = member.RubyItems
				}
				candidate, err := parseFrozenMemberBounded(b, h, full, override, i, logger)
				if err != nil {
					return err
				}
				_, err = b.EncodeJSON(candidate, in.ItemLimit)
				return err
			}
		}
		return ErrCandidateCapacity
	}
	processor, ok := h.Glossary.(glossary.ResponseProcessor)
	if file, ok := h.Glossary.(*glossary.FileGlossary); ok {
		if err = file.BindAllocationOwner(ctx, runtime.Admission); err != nil {
			return response, err
		}
	}
	var business glossary.ResponseBusiness
	if ok {
		business, err = processor.ProcessResponse(ctx, in)
	} else {
		err = errors.New("glossary adapter has no atomic response processor")
	}
	if err != nil {
		business.Release()
		return response, err
	}
	if memory, ok := store.(interface {
		SaveResponseBusiness(context.Context, string, glossary.ResponseBusiness) error
	}); ok {
		if err := memory.SaveResponseBusiness(ctx, response.ID, business); err != nil {
			business.Release()
			return response, err
		}
		// The producer's handle was consumed by the store. Obtain an explicit
		// reader of that same backing for the remainder of this promotion.
		borrowed, err := store.Response(ctx, response.ID, runtime.handoffLimit(), runtime.responseLimit(), runtime.handoffLimit())
		if err != nil {
			business.Release()
			return response, err
		}
		response.ReleaseOwned = borrowed.ReleaseOwned
	} else {
		owned := business
		response.ReleaseOwned = func() { owned.Release() }
	}
	response.CorrectedTargets, response.CorrectedIndex, response.Receipt = business.Body, business.Index, business.Receipt
	response.CorrectedBytes = int64(len(business.Body))
	response.Phase = "glossary_processed"
	business = glossary.ResponseBusiness{}
	return response, nil
}
