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
	ids := make([]string, len(frozen.Members))
	for i, m := range frozen.Members {
		ids[i] = m.PromptID
	}
	var parsed repair.Result
	var raw string
	var entriesPayload []byte
	err := runtime.Run(ctx, func() error {
		b, e := backend.NewBuildBounds(runtime.responseLimit(), response.FreezeBytes+response.ResponseBytes)
		if e != nil {
			return e
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
	in := glossary.ResponseProcess{RequestID: response.ID, InputDigest: response.ResponseDigest, TargetLang: frozen.TargetLang, EntriesPayload: entriesPayload, Rewrite: h.InlineConflictStrategy == InlineConflictRewriteLocal, ItemLimit: runtime.Window.limits.ItemBytes, BusinessLimit: response.BusinessBound, Build: runtime.Run}
	for _, id := range ids {
		target := glossary.ResponseTarget{ID: id}
		err := runtime.Run(ctx, func() error {
			b, e := backend.NewBuildBounds(in.ItemLimit, response.ResponseBytes+response.FreezeBytes)
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
				body, index, err := glossary.EncodeResponseBusiness([]glossary.ResponseTarget{{Text: target.Text}}, in.ItemLimit)
				if err != nil {
					return err
				}
				override.CorrectedTargets = body
				override.CorrectedIndex = make([]byte, len(ids)*16)
				copy(override.CorrectedIndex[i*16:], index)
				b, e := backend.NewBuildBounds(in.ItemLimit, response.ResponseBytes+response.FreezeBytes)
				if e != nil {
					return e
				}
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
					if err := b.ReserveNodes(int64(len(member.Segment.Meta))+4, 256); err != nil {
						return err
					}
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
	var business glossary.ResponseBusiness
	if ok {
		business, err = processor.ProcessResponse(ctx, in)
	} else {
		err = errors.New("glossary adapter has no atomic response processor")
	}
	if err != nil {
		return response, err
	}
	if memory, ok := store.(interface {
		SaveResponseBusiness(context.Context, string, glossary.ResponseBusiness) error
	}); ok {
		if err := memory.SaveResponseBusiness(ctx, response.ID, business); err != nil {
			return response, err
		}
	}
	response.CorrectedTargets, response.CorrectedIndex, response.Receipt = business.Body, business.Index, business.Receipt
	response.CorrectedBytes = int64(len(business.Body))
	response.Phase = "glossary_processed"
	return response, nil
}
