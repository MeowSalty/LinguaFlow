package stages

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"sync"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/protect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
)

// processBatchInRound 处理一批 idx（len(idxs) <= round.BatchSize）。len==1 或 BatchSize<=1 时走单段路径；
// 否则尝试批量发送，失败时按 round.FallbackShrink 缩小子批并发递归，直到收敛到单段。
// 返回 unresolved 的段索引列表；非 nil error 表示 stage 级别终止。
func (s *Translate) processBatchInRound(ctx context.Context, doc *pipeline.Document, idxs []int, round Round, logger *slog.Logger) ([]int, error) {
	renderer := s.resolveRoundRenderer(round)
	repairOpts := s.resolveRoundRepair(round)

	bs := max(round.BatchSize, 1)
	if len(idxs) == 1 || bs <= 1 {
		ok, err := s.translateSingleInRound(ctx, doc, idxs[0], round, logger)
		if err != nil {
			return nil, err
		}
		if !ok {
			return append([]int(nil), idxs...), nil
		}
		return nil, nil
	}

	if s.Limiter != nil {
		if err := s.Limiter.Wait(ctx); err != nil {
			return nil, err
		}
	}

	glos, tmHints := s.lookupHints(ctx, doc, idxs, logger)

	inputs := make([]prompt.SegmentInput, len(idxs))
	wantIDs := make([]string, len(idxs))
	batchSources := make([]string, len(idxs))
	for k, idx := range idxs {
		id := strconv.Itoa(k + 1)
		inputs[k] = prompt.SegmentInput{ID: id, Source: doc.Segments[idx].Source}
		wantIDs[k] = id
		batchSources[k] = doc.Segments[idx].Source
	}

	minIdx, maxIdx := idxs[0], idxs[len(idxs)-1]
	prev, next := prompt.BuildContextRange(doc, minIdx, maxIdx)

	rubyAnns := extractRubyAnnotationsFromDoc(doc, idxs)
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
		MaxBootstrapTerms: s.calcMaxBootstrapTerms(batchSources),
		StrictSchema:      true,
		RubyAnnotations:   rubyAnns,
		RubyOutputFormat:  s.RubyOutputFormat,
	}
	sys, usr, err := renderer.Render(data)
	if err != nil {
		return nil, fmt.Errorf("render batch prompt (%d segs): %w", len(idxs), err)
	}

	req := backend.Request{
		System:     sys,
		User:       usr,
		JSONSchema: translationsSchema(wantIDs, s.InlineBootstrap, s.RubyOutputFormat != ""),
	}

	var (
		resp        *backend.Response
		res         repair.Result
		picked      backend.Backend
		lastErr     error
		bestRes     repair.Result // 跟踪最佳部分结果（最多翻译数）
		bestResp    *backend.Response
		bestBackend backend.Backend
	)
	for _, b := range round.Backends {
		resp, err = s.callOnce(ctx, b, req, round.Retry)
		if err != nil {
			if isFatalBackendError(err) {
				logger.Error("backend returned fatal error",
					"backend", b.Name(), "batch_size", len(idxs), "err", err)
			} else {
				logger.Warn("batch translate failed, trying next backend",
					"backend", b.Name(), "batch_size", len(idxs), "err", err)
			}
			lastErr = err
			continue
		}
		res = parseBatchResponseLenient(resp.Text, wantIDs, repairOpts)

		if res.ParseErr != nil && repairOpts.PromptUpgrade {
			reminder := repair.BuildRetryReminder(nil, res.ParseErr, headSnippet(resp.Text, 200))
			req2 := req
			req2.System = req.System + reminder
			if resp2, err2 := s.callOnce(ctx, b, req2, round.Retry); err2 == nil {
				res2 := parseBatchResponseLenient(resp2.Text, wantIDs, repairOpts)
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
		// 跟踪最佳部分结果（翻译数最多）
		if len(res.Trans) > len(bestRes.Trans) {
			bestRes = res
			bestResp = resp
			bestBackend = b
		}
		if len(res.Missing) > 0 && (!repairOpts.Partial || missingRatio >= repairOpts.PartialThreshold) {
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
		// 如果有最佳部分结果，使用它；缺失的段标记为 unresolved。
		if bestBackend != nil {
			picked = bestBackend
			res = bestRes
			resp = bestResp
		} else {
			if lastErr != nil {
				logger.Warn("all backends failed for batch, shrinking or falling back", "batch_size", len(idxs), "err", lastErr)
			}
			return s.shrinkOrFallback(ctx, doc, idxs, round, lastErr, logger)
		}
	}

	if len(res.Repaired) > 0 {
		logger.Info("batch response repaired", "backend", picked.Name(), "ops", res.Repaired,
			"missing", len(res.Missing))
	}

	trans, glosEntries, rubyOutputMap := res.Trans, res.Glos, res.RubyOutput

	logger.Debug("batch translated",
		"backend", picked.Name(), "batch_size", len(idxs),
		"prompt_tokens", resp.Usage.PromptTokens,
		"completion_tokens", resp.Usage.CompletionTokens,
		"inline_glossary", len(glosEntries),
		"missing", len(res.Missing))

	s.absorbInlineGlossary(ctx, glosEntries, trans, doc.TargetLang, logger)

	// 写回并对每段做占位符校验；缺失的段用 translateSingleInRound 补救。
	// Partial 路径下 trans 可能不包含全部 ID——缺失的 ID 收集后单跑。
	rep := s.reporter()
	var unresolved []int
	var missingIdxs []int
	for k, idx := range idxs {
		seg := &doc.Segments[idx]
		text, ok := trans[wantIDs[k]]
		if !ok {
			missingIdxs = append(missingIdxs, idx)
			continue
		}
		// 分发 ruby_output 到各段
		if rubyOutputMap != nil {
			id := wantIDs[k]
			if ro, rok := rubyOutputMap[id]; rok && len(ro) > 0 {
				if seg.Meta == nil {
					seg.Meta = make(map[string]any)
				}
				seg.Meta["ruby_output"] = ro
			}
		}
		// L3 占位符归一化：仅 normalize seg.Protected 中已知 key 的变体。
		if repairOpts.PlaceholderNormalize {
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
			ok, err := s.translateSingleInRound(ctx, doc, idx, round, logger)
			if err != nil {
				return nil, err
			}
			if !ok {
				unresolved = append(unresolved, idx)
			}
			continue
		}
		s.addTM(ctx, doc, seg, logger)
		rep.SegmentDone()
	}
	for _, idx := range missingIdxs {
		ok, err := s.translateSingleInRound(ctx, doc, idx, round, logger)
		if err != nil {
			return nil, err
		}
		if !ok {
			unresolved = append(unresolved, idx)
		}
	}
	return unresolved, nil
}

// shrinkOrFallback 根据 round.FallbackShrink 决定：
//   - 缩小到 >=2 的子批并发递归（每个子批又可能继续缩小）
//   - 否则坍缩到顺序单段（调用 translateSingleInRound）
func (s *Translate) shrinkOrFallback(ctx context.Context, doc *pipeline.Document, idxs []int, round Round, lastErr error, logger *slog.Logger) ([]int, error) {
	if isFatalBackendError(lastErr) {
		logger.Warn("all backends failed with fatal error, segments will be marked as unresolved",
			"batch_size", len(idxs), "err", lastErr)
		return idxs, nil
	}
	nextSize := shrinkNext(len(idxs), round.FallbackShrink)
	if nextSize < 2 {
		// 坍缩到顺序单段
		var unresolved []int
		for _, idx := range idxs {
			ok, err := s.translateSingleInRound(ctx, doc, idx, round, logger)
			if err != nil {
				return nil, err
			}
			if !ok {
				unresolved = append(unresolved, idx)
			}
		}
		return unresolved, nil
	}
	var sub [][]int
	for i := 0; i < len(idxs); i += nextSize {
		end := min(i+nextSize, len(idxs))
		sub = append(sub, idxs[i:end])
	}
	logger.Info("shrinking batch and retrying",
		"from", len(idxs), "to", nextSize, "sub_batches", len(sub), "shrink", round.FallbackShrink)

	var (
		mu         sync.Mutex
		unresolved []int
	)
	if err := runConcurrent(ctx, len(sub), round.Concurrency, func(ctx context.Context, bidx int) error {
		subUnresolved, err := s.processBatchInRound(ctx, doc, sub[bidx], round, logger)
		if err != nil {
			return err
		}
		if len(subUnresolved) > 0 {
			mu.Lock()
			unresolved = append(unresolved, subUnresolved...)
			mu.Unlock()
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return unresolved, nil
}

// shrinkNext 计算下一级 batch 大小。
//   - shrink <= 0 或 NaN/Inf：返回 0（调用方据此走 single fallback）
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

// isFatalBackendError 判断是否为不可恢复的致命错误（如 401、403 认证失败）。
// 遇到此类错误时当前 backend 被视为不可用，段将推迟到后续轮次处理。
func isFatalBackendError(err error) bool {
	var hsErr backend.HTTPStatusError
	if errors.As(err, &hsErr) {
		code := hsErr.HTTPStatus()
		return code == 401 || code == 403
	}
	return false
}
