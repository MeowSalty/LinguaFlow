package stages

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strconv"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/protect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
)

// processBatchAtSize 处理一批 idx（len(idxs) <= curSize）。len==1 或 BatchSize<=1 时走单段路径；
// 否则尝试批量发送，失败时按 FallbackShrink 缩小子批并发递归，直到收敛到单段。
func (s *Translate) processBatchAtSize(ctx context.Context, doc *pipeline.Document, idxs []int, curSize int, logger *slog.Logger) error {
	if len(idxs) == 1 || s.BatchSize <= 1 {
		return s.translateSingle(ctx, doc, idxs[0], logger)
	}

	if s.Limiter != nil {
		if err := s.Limiter.Wait(ctx); err != nil {
			return err
		}
	}

	glos, tmHints := s.lookupHints(ctx, doc, idxs, logger)

	inputs := make([]prompt.SegmentInput, len(idxs))
	wantIDs := make([]string, len(idxs))
	for k, idx := range idxs {
		id := strconv.Itoa(k + 1)
		inputs[k] = prompt.SegmentInput{ID: id, Source: doc.Segments[idx].Source}
		wantIDs[k] = id
	}

	minIdx, maxIdx := idxs[0], idxs[len(idxs)-1]
	prev, next := prompt.BuildContextRange(doc, minIdx, maxIdx)

	data := prompt.Data{
		SourceLang:        doc.SourceLang,
		TargetLang:        doc.TargetLang,
		Segments:          inputs,
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
		return fmt.Errorf("render batch prompt (%d segs): %w", len(idxs), err)
	}

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
		resp    *backend.Response
		res     repair.Result
		picked  backend.Backend
		lastErr error
	)
	for _, b := range backends {
		resp, err = s.callOnce(ctx, b, req)
		if err != nil {
			logger.Warn("batch translate failed, trying next backend",
				"backend", b.Name(), "batch_size", len(idxs), "err", err)
			lastErr = err
			continue
		}
		res = parseBatchResponseLenient(resp.Text, wantIDs, s.Repair)

		if res.ParseErr != nil && s.Repair.PromptUpgrade {
			reminder := repair.BuildRetryReminder(nil, res.ParseErr, headSnippet(resp.Text, 200))
			req2 := req
			req2.System = req.System + reminder
			if resp2, err2 := s.callOnce(ctx, b, req2); err2 == nil {
				res2 := parseBatchResponseLenient(resp2.Text, wantIDs, s.Repair)
				if res2.ParseErr == nil {
					logger.Info("batch response recovered by prompt upgrade",
						"backend", b.Name(), "repaired", res2.Repaired)
					resp = resp2
					res = res2
				}
			}
		}
		if res.ParseErr != nil {
			logger.Warn("batch response parse failed, trying next backend",
				"backend", b.Name(), "batch_size", len(idxs), "err", res.ParseErr,
				"resp_len", len(resp.Text), "resp_head", headSnippet(resp.Text, 200),
				"repaired", res.Repaired)
			lastErr = res.ParseErr
			continue
		}
		missingRatio := 0.0
		if len(wantIDs) > 0 {
			missingRatio = float64(len(res.Missing)) / float64(len(wantIDs))
		}
		if len(res.Missing) > 0 && (!s.Repair.Partial || missingRatio >= s.Repair.PartialThreshold) {
			logger.Warn("partial recovery exceeded threshold, trying next backend",
				"backend", b.Name(), "missing", len(res.Missing), "total", len(wantIDs),
				"threshold", s.Repair.PartialThreshold, "partial_enabled", s.Repair.Partial)
			lastErr = fmt.Errorf("partial recovery exceeded threshold")
			continue
		}
		picked = b
		break
	}
	if picked == nil {
		if lastErr != nil {
			logger.Warn("all backends failed for batch, shrinking or falling back", "batch_size", len(idxs), "err", lastErr)
		}
		return s.shrinkOrFallback(ctx, doc, idxs, curSize, logger)
	}

	if len(res.Repaired) > 0 {
		logger.Info("batch response repaired", "backend", picked.Name(), "ops", res.Repaired,
			"missing", len(res.Missing))
	}

	trans, glosEntries := res.Trans, res.Glos

	logger.Debug("batch translated",
		"backend", picked.Name(), "batch_size", len(idxs),
		"prompt_tokens", resp.Usage.PromptTokens,
		"completion_tokens", resp.Usage.CompletionTokens,
		"inline_glossary", len(glosEntries),
		"missing", len(res.Missing))

	s.absorbInlineGlossary(ctx, glosEntries, trans, doc.TargetLang, logger)

	// 写回并对每段做占位符校验；缺失的段单独补救（走 translateSingle 的 S5 路径）。
	// Partial 路径下 trans 可能不包含全部 ID——缺失的 ID 收集到 missingIdxs 再单跑。
	rep := s.reporter()
	var missingIdxs []int
	for k, idx := range idxs {
		seg := &doc.Segments[idx]
		text, ok := trans[wantIDs[k]]
		if !ok {
			missingIdxs = append(missingIdxs, idx)
			continue
		}
		// L3 占位符归一化：仅 normalize seg.Protected 中已知 key 的变体。
		if s.Repair.PlaceholderNormalize {
			if normText, normalized := repair.NormalizePlaceholders(text, seg.Protected); len(normalized) > 0 {
				logger.Info("placeholders normalized",
					"seg", seg.ID, "normalized", normalized)
				text = normText
			}
		}
		seg.Target = text
		if missing := protect.MissingPlaceholders(seg); len(missing) > 0 {
			logger.Warn("batch segment placeholders missing, single-retry",
				"seg", seg.ID, "missing", missing)
			// translateSingle 内部会在结束时上报本段进度；此处不发，避免双计数。
			if err := s.translateSingle(ctx, doc, idx, logger); err != nil {
				return err
			}
			continue
		}
		s.addTM(ctx, doc, seg, logger)
		rep.SegmentDone()
	}
	for _, idx := range missingIdxs {
		// translateSingle 内部自带 SegmentDone；这些段进度由 single 路径上报，不在批路径双计。
		if err := s.translateSingle(ctx, doc, idx, logger); err != nil {
			return err
		}
	}
	return nil
}

// shrinkOrFallback 根据 FallbackShrink 决定：
//   - 缩小到 >=2 的子批并发递归（每个子批又可能继续缩小）
//   - 否则坍缩到 fallbackSingles（顺序单段）
func (s *Translate) shrinkOrFallback(ctx context.Context, doc *pipeline.Document, idxs []int, curSize int, logger *slog.Logger) error {
	nextSize := shrinkNext(curSize, s.FallbackShrink)
	if nextSize < 2 {
		return s.fallbackSingles(ctx, doc, idxs, logger)
	}
	var sub [][]int
	for i := 0; i < len(idxs); i += nextSize {
		end := min(i+nextSize, len(idxs))
		sub = append(sub, idxs[i:end])
	}
	logger.Info("shrinking batch and retrying",
		"from", curSize, "to", nextSize, "sub_batches", len(sub), "shrink", s.FallbackShrink)
	return runConcurrent(ctx, len(sub), s.Concurrency, func(ctx context.Context, bidx int) error {
		return s.processBatchAtSize(ctx, doc, sub[bidx], nextSize, logger)
	})
}

// shrinkNext 计算下一级 batch 大小。
//   - shrink <= 0 或 NaN/Inf：返回 0（调用方据此走 fallbackSingles）
//   - shrink >= 1：返回 0（Validate 本应已拦截，但保险起见）
//   - 否则 next = floor(cur * shrink)；若 >= cur 则强制 cur-1，避免不收敛
//   - next < 2 也返回 0（再缩等同单段，由调用方坍缩处理）
func shrinkNext(cur int, shrink float64) int {
	if shrink <= 0 || shrink >= 1 || math.IsNaN(shrink) || math.IsInf(shrink, 0) {
		return 0
	}
	next := int(math.Floor(float64(cur) * shrink))
	if next >= cur {
		next = cur - 1
	}
	if next < 2 {
		return 0
	}
	return next
}

// fallbackSingles 顺序对 idxs 中每段调 translateSingle。
func (s *Translate) fallbackSingles(ctx context.Context, doc *pipeline.Document, idxs []int, logger *slog.Logger) error {
	for _, idx := range idxs {
		if err := s.translateSingle(ctx, doc, idx, logger); err != nil {
			return err
		}
	}
	return nil
}

// processBatchInRound 是 Plan 模式下的批量翻译路径，与 processBatchAtSize 逻辑类似，
// 但使用 round 级别的后端选择，失败时不 shrink 而是 defer 到下一轮。
func (s *Translate) processBatchInRound(ctx context.Context, doc *pipeline.Document, idxs []int, round runtimeRound, logger *slog.Logger) ([]int, error) {
	if len(idxs) == 1 || round.BatchSize <= 1 {
		ok, err := s.translateSingleInRound(ctx, doc, idxs[0], round, logger)
		if err != nil {
			return nil, err
		}
		if ok {
			return nil, nil
		}
		return append([]int(nil), idxs...), nil
	}

	if s.Limiter != nil {
		if err := s.Limiter.Wait(ctx); err != nil {
			return nil, err
		}
	}

	glos, tmHints := s.lookupHints(ctx, doc, idxs, logger)
	inputs := make([]prompt.SegmentInput, len(idxs))
	wantIDs := make([]string, len(idxs))
	for k, idx := range idxs {
		id := strconv.Itoa(k + 1)
		inputs[k] = prompt.SegmentInput{ID: id, Source: doc.Segments[idx].Source}
		wantIDs[k] = id
	}
	prev, next := prompt.BuildContextRange(doc, idxs[0], idxs[len(idxs)-1])
	data := prompt.Data{
		SourceLang:        doc.SourceLang,
		TargetLang:        doc.TargetLang,
		Segments:          inputs,
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
		return nil, fmt.Errorf("render batch prompt (%d segs): %w", len(idxs), err)
	}
	req := backend.Request{System: sys, User: usr, JSONSchema: translationsSchema(wantIDs, s.InlineBootstrap)}
	backends, err := s.plannedBackendsFor(ctx, round.BackendMode, round.BackendOrder)
	if err != nil {
		return nil, err
	}
	var (
		resp    *backend.Response
		res     repair.Result
		picked  backend.Backend
		lastErr error
	)
	for _, b := range backends {
		resp, err = s.callOnce(ctx, b, req)
		if err != nil {
			logger.Warn("batch translate failed, trying next backend",
				"backend", b.Name(), "batch_size", len(idxs), "round", round.Name, "err", err)
			lastErr = err
			continue
		}
		res = parseBatchResponseLenient(resp.Text, wantIDs, s.Repair)
		if res.ParseErr != nil && s.Repair.PromptUpgrade {
			reminder := repair.BuildRetryReminder(nil, res.ParseErr, headSnippet(resp.Text, 200))
			req2 := req
			req2.System = req.System + reminder
			if resp2, err2 := s.callOnce(ctx, b, req2); err2 == nil {
				res2 := parseBatchResponseLenient(resp2.Text, wantIDs, s.Repair)
				if res2.ParseErr == nil {
					resp = resp2
					res = res2
				}
			}
		}
		if res.ParseErr != nil {
			logger.Warn("batch response parse failed, trying next backend",
				"backend", b.Name(), "batch_size", len(idxs), "round", round.Name, "err", res.ParseErr,
				"resp_len", len(resp.Text), "resp_head", headSnippet(resp.Text, 200), "repaired", res.Repaired)
			lastErr = res.ParseErr
			continue
		}
		picked = b
		break
	}
	if picked == nil {
		if lastErr != nil {
			logger.Warn("all backends failed for planned batch, defer to next round", "batch_size", len(idxs), "round", round.Name, "err", lastErr)
		}
		return append([]int(nil), idxs...), nil
	}

	trans, glosEntries := res.Trans, res.Glos
	s.absorbInlineGlossary(ctx, glosEntries, trans, doc.TargetLang, logger)
	rep := s.reporter()
	unresolved := make([]int, 0)
	for k, idx := range idxs {
		seg := &doc.Segments[idx]
		text, ok := trans[wantIDs[k]]
		if !ok {
			seg.Target = ""
			unresolved = append(unresolved, idx)
			continue
		}
		if s.Repair.PlaceholderNormalize {
			if normText, normalized := repair.NormalizePlaceholders(text, seg.Protected); len(normalized) > 0 {
				logger.Info("placeholders normalized", "seg", seg.ID, "normalized", normalized)
				text = normText
			}
		}
		seg.Target = text
		if missing := protect.MissingPlaceholders(seg); len(missing) > 0 {
			logger.Warn("planned batch segment placeholders missing, defer to next round", "seg", seg.ID, "missing", missing, "round", round.Name)
			seg.Target = ""
			unresolved = append(unresolved, idx)
			continue
		}
		s.addTM(ctx, doc, seg, logger)
		rep.SegmentDone()
	}
	return unresolved, nil
}
