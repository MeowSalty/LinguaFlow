package stages

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/protect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
)

// translateSingle 翻译单段（走 JSON 协议，含 S5 占位符补救）。
// 任何 return nil 路径都表示这段处理结束（无论译完、保留原文，还是补救失败），
// 因此函数末尾通过 defer 上报一次进度；返回非 nil error 则不计入进度（stage 终止）。
func (s *Translate) translateSingle(ctx context.Context, doc *pipeline.Document, idx int, logger *slog.Logger) (retErr error) {
	defer func() {
		if retErr == nil {
			s.reporter().SegmentDone()
		}
	}()

	seg := &doc.Segments[idx]

	if s.Limiter != nil {
		if err := s.Limiter.Wait(ctx); err != nil {
			return err
		}
	}

	glos, tmHints := s.lookupHints(ctx, doc, []int{idx}, logger)
	prev, next := prompt.BuildContext(doc, idx)

	data := prompt.Data{
		SourceLang:        doc.SourceLang,
		TargetLang:        doc.TargetLang,
		Source:            seg.Source,
		PrevContext:       prev,
		NextContext:       next,
		Glossary:          glos,
		TMHints:           tmHints,
		Vars:              doc.Vars,
		InlineBootstrap:   s.InlineBootstrap,
		MaxBootstrapTerms: s.maxBootstrapTerms(),
		StrictSchema:      true,
	}
	sys, usr, err := s.Renderer.Render(data)
	if err != nil {
		return fmt.Errorf("render prompt for seg %s: %w", seg.ID, err)
	}

	wantIDs := []string{prompt.SingleID}
	req := backend.Request{
		System:     sys,
		User:       usr,
		JSONSchema: translationsSchema(wantIDs, s.InlineBootstrap),
	}
	backends, err := s.plannedBackends(ctx)
	if err != nil {
		return err
	}
	var (
		resp        *backend.Response
		trans       map[string]string
		glosEntries []prompt.BootstrapEntry
		picked      backend.Backend
	)
	for _, b := range backends {
		resp, err = s.callOnce(ctx, b, req)
		if err != nil {
			logger.Warn("translate failed, trying next backend",
				"seg", seg.ID, "backend", b.Name(), "err", err)
			continue
		}
		var perr error
		trans, glosEntries, perr = parseBatchResponse(resp.Text, wantIDs)
		if perr != nil {
			logger.Warn("single response parse failed, trying next backend",
				"seg", seg.ID, "backend", b.Name(), "err", perr,
				"resp_len", len(resp.Text), "resp_head", headSnippet(resp.Text, 200))
			continue
		}
		picked = b
		break
	}
	if picked == nil {
		seg.Target = seg.Source
		return nil
	}
	logger.Debug("segment translated",
		"seg", seg.ID, "backend", picked.Name(),
		"prompt_tokens", resp.Usage.PromptTokens,
		"completion_tokens", resp.Usage.CompletionTokens,
		"inline_glossary", len(glosEntries))
	// 先吸收术语并就地修正冲突，再做占位符 normalize / 写回 seg.Target——保证
	// absorbInlineGlossary 能对 trans 做并发冲突修正，避免文档内同一术语翻译不一致。
	s.absorbInlineGlossary(ctx, glosEntries, trans, doc.TargetLang, logger)
	target := trans[prompt.SingleID]
	if s.Repair.PlaceholderNormalize {
		if normText, normalized := repair.NormalizePlaceholders(target, seg.Protected); len(normalized) > 0 {
			logger.Info("placeholders normalized",
				"seg", seg.ID, "normalized", normalized)
			target = normText
		}
	}
	seg.Target = target

	// 占位符完整性校验：缺失则追加补救指令重试一次。
	if missing := protect.MissingPlaceholders(seg); len(missing) > 0 {
		logger.Warn("placeholders missing in translation, retrying with reminder",
			"seg", seg.ID, "backend", picked.Name(), "missing", missing)
		var reminder string
		if s.Repair.PromptUpgrade {
			// L4：用 repair 包提供的反例 reminder，包含 missing 列表与上次响应头摘录。
			reminder = repair.BuildRetryReminder(missing, nil, headSnippet(resp.Text, 200))
		} else {
			reminder = fmt.Sprintf(
				"\n\nIMPORTANT: your previous JSON translation omitted these placeholders: %s. "+
					"Reproduce ALL of them verbatim in the translation, preserving their original positions. "+
					"Reply with the same JSON envelope schema as before.",
				strings.Join(missing, ", "))
		}
		req2 := req
		req2.System = req.System + reminder

		resp2, err2 := s.callOnce(ctx, picked, req2)
		if err2 != nil {
			logger.Warn("placeholder retry failed, keep source",
				"seg", seg.ID, "backend", picked.Name(), "err", err2)
			seg.Target = seg.Source
			return nil
		}
		trans2, glos2, perr2 := parseBatchResponse(resp2.Text, wantIDs)
		if perr2 != nil {
			logger.Warn("placeholder retry response parse failed, keep source",
				"seg", seg.ID, "backend", picked.Name(), "err", perr2)
			seg.Target = seg.Source
			return nil
		}
		s.absorbInlineGlossary(ctx, glos2, trans2, doc.TargetLang, logger)
		target2 := trans2[prompt.SingleID]
		if s.Repair.PlaceholderNormalize {
			if normText, normalized := repair.NormalizePlaceholders(target2, seg.Protected); len(normalized) > 0 {
				logger.Info("placeholders normalized after retry",
					"seg", seg.ID, "normalized", normalized)
				target2 = normText
			}
		}
		seg.Target = target2
		if still := protect.MissingPlaceholders(seg); len(still) > 0 {
			logger.Warn("placeholders still missing after retry, keep source",
				"seg", seg.ID, "backend", picked.Name(), "missing", still)
			seg.Target = seg.Source
			return nil
		}
	}

	s.addTM(ctx, doc, seg, logger)
	return nil
}

// translateSingleInRound 是 Plan 模式下的单段翻译路径，与 translateSingle 逻辑类似，
// 但使用 round 级别的后端选择，失败时返回 (false, nil) 以便 defer 到下一轮。
func (s *Translate) translateSingleInRound(ctx context.Context, doc *pipeline.Document, idx int, round runtimeRound, logger *slog.Logger) (bool, error) {
	seg := &doc.Segments[idx]
	if s.Limiter != nil {
		if err := s.Limiter.Wait(ctx); err != nil {
			return false, err
		}
	}
	glos, tmHints := s.lookupHints(ctx, doc, []int{idx}, logger)
	prev, next := prompt.BuildContext(doc, idx)
	data := prompt.Data{
		SourceLang:        doc.SourceLang,
		TargetLang:        doc.TargetLang,
		Source:            seg.Source,
		PrevContext:       prev,
		NextContext:       next,
		Glossary:          glos,
		TMHints:           tmHints,
		Vars:              doc.Vars,
		InlineBootstrap:   s.InlineBootstrap,
		MaxBootstrapTerms: s.maxBootstrapTerms(),
		StrictSchema:      true,
	}
	sys, usr, err := s.Renderer.Render(data)
	if err != nil {
		return false, fmt.Errorf("render prompt for seg %s: %w", seg.ID, err)
	}
	wantIDs := []string{prompt.SingleID}
	req := backend.Request{System: sys, User: usr, JSONSchema: translationsSchema(wantIDs, s.InlineBootstrap)}
	backends, err := s.plannedBackendsFor(ctx, round.BackendMode, round.BackendOrder)
	if err != nil {
		return false, err
	}
	var (
		resp        *backend.Response
		trans       map[string]string
		glosEntries []prompt.BootstrapEntry
		picked      backend.Backend
	)
	for _, b := range backends {
		resp, err = s.callOnce(ctx, b, req)
		if err != nil {
			logger.Warn("translate failed, trying next backend", "seg", seg.ID, "backend", b.Name(), "round", round.Name, "err", err)
			continue
		}
		var perr error
		trans, glosEntries, perr = parseBatchResponse(resp.Text, wantIDs)
		if perr != nil {
			logger.Warn("single response parse failed, trying next backend", "seg", seg.ID, "backend", b.Name(), "round", round.Name, "err", perr,
				"resp_len", len(resp.Text), "resp_head", headSnippet(resp.Text, 200))
			continue
		}
		picked = b
		break
	}
	if picked == nil {
		seg.Target = ""
		return false, nil
	}
	s.absorbInlineGlossary(ctx, glosEntries, trans, doc.TargetLang, logger)
	target := trans[prompt.SingleID]
	if s.Repair.PlaceholderNormalize {
		if normText, normalized := repair.NormalizePlaceholders(target, seg.Protected); len(normalized) > 0 {
			logger.Info("placeholders normalized", "seg", seg.ID, "normalized", normalized)
			target = normText
		}
	}
	seg.Target = target
	if missing := protect.MissingPlaceholders(seg); len(missing) > 0 {
		logger.Warn("placeholders missing in planned single, defer to next round", "seg", seg.ID, "backend", picked.Name(), "round", round.Name, "missing", missing)
		seg.Target = ""
		return false, nil
	}
	s.addTM(ctx, doc, seg, logger)
	s.reporter().SegmentDone()
	return true, nil
}
