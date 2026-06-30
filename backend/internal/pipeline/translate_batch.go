package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/protect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
)

// processBatchInRound 处理一批 idx（len(idxs) <= round.BatchSize）。
// 尝试批量发送，失败时按 round.FallbackShrink 缩小子批并发递归，直到收敛到单段。
// batchIndex 是本批在当前轮次中的序号（0-based），用于事件上报。
// contextSet 包含本批中作为上下文参考（不需要翻译）的段落索引。
// 返回 unresolved 的段索引列表；非 nil error 表示 stage 级别终止。
func (s *Translate) processBatchInRound(ctx context.Context, doc *Document, idxs []int, round Round, batchIndex int, logger *slog.Logger, contextSet map[int]struct{}) ([]int, error) {
	batchStart := time.Now()
	renderer := s.resolveRoundRenderer(round)
	repairOpts := s.resolveRoundRepair(round)

	glos, tmHints := s.lookupHints(ctx, doc, idxs, logger)

	inputs := make([]prompt.SegmentInput, len(idxs))
	idMap := make(map[int]string, len(idxs))
	var wantIDs []string
	batchSources := make([]string, 0, len(idxs))
	for k, idx := range idxs {
		id := strconv.Itoa(k + 1)
		idMap[idx] = id
		seg := doc.Segments[idx]
		source := seg.Source
		isCtx := isContext(contextSet, idx)
		if isCtx && seg.OriginalSource != "" {
			source = seg.OriginalSource
		}
		inputs[k] = prompt.SegmentInput{ID: id, Source: source, Translate: !isCtx}
		if !isCtx {
			wantIDs = append(wantIDs, id)
			batchSources = append(batchSources, seg.Source)
		}
	}

	rubyAnns := extractRubyAnnotationsFromDoc(doc, idxs, idMap)
	data := prompt.Data{
		SourceLang:        doc.SourceLang,
		TargetLang:        doc.TargetLang,
		Segments:          inputs,
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
		resp    *backend.Response
		res     repair.Result
		picked  backend.Backend
		lastErr error
		tried   []string // 本次尝试过的所有后端名
	)
	tried = append(tried, round.Backend.Name())
	resp, err = s.callOnce(ctx, round.Backend, req, round.Retry)
	if err != nil {
		if isFatalBackendError(err) {
			logger.Error("backend returned fatal error",
				"backend", round.Backend.Name(), "batch_size", len(idxs), "err", err)
		} else {
			logger.Warn("batch translate failed",
				"backend", round.Backend.Name(), "batch_size", len(idxs), "err", err)
		}
		lastErr = err
		logger.Warn("backend failed for batch, shrinking or falling back", "batch_size", len(idxs), "err", lastErr)
		if len(idxs) <= 1 {
			return filterPendingIdxs(idxs, contextSet), nil
		}
		return s.shrinkOrFallback(ctx, doc, filterPendingIdxs(idxs, contextSet), round, lastErr, logger)
	}

	res = parseBatchResponseLenient(resp.Text, wantIDs, repairOpts)
	atomic.AddInt64(&doc.InputTokens, resp.Usage.PromptTokens)
	atomic.AddInt64(&doc.OutputTokens, resp.Usage.CompletionTokens)

	if res.ParseErr != nil && repairOpts.PromptUpgrade {
		reminder := repair.BuildRetryReminder(nil, res.ParseErr, headSnippet(resp.Text, 200))
		req2 := req
		req2.System = req.System + reminder
		if resp2, err2 := s.callOnce(ctx, round.Backend, req2, round.Retry); err2 == nil {
			res2 := parseBatchResponseLenient(resp2.Text, wantIDs, repairOpts)
			if res2.ParseErr == nil {
				logger.Info("batch response recovered by prompt upgrade",
					"backend", round.Backend.Name(), "repaired", res2.Repaired)
				resp = resp2
				res = res2
				atomic.AddInt64(&doc.InputTokens, resp2.Usage.PromptTokens)
				atomic.AddInt64(&doc.OutputTokens, resp2.Usage.CompletionTokens)
			}
		}
	}
	if res.ParseErr != nil {
		logger.Warn("batch response parse failed, shrinking or falling back",
			"backend", round.Backend.Name(), "batch_size", len(idxs), "err", res.ParseErr,
			"resp_len", len(resp.Text), "resp_head", headSnippet(resp.Text, 200),
			"repaired", res.Repaired)
		if len(idxs) <= 1 {
			return filterPendingIdxs(idxs, contextSet), nil
		}
		return s.shrinkOrFallback(ctx, doc, filterPendingIdxs(idxs, contextSet), round, res.ParseErr, logger)
	}

	missingRatio := 0.0
	if len(wantIDs) > 0 {
		missingRatio = float64(len(res.Missing)) / float64(len(wantIDs))
	}
	if len(res.Missing) > 0 && (!repairOpts.Partial || missingRatio >= repairOpts.PartialThreshold) {
		logger.Warn("partial recovery exceeded threshold, using best partial result",
			"backend", round.Backend.Name(), "missing", len(res.Missing), "total", len(wantIDs),
			"threshold", s.Repair.PartialThreshold, "partial_enabled", s.Repair.Partial)
		// 最佳部分结果已在 res 中（单后端无需比较），缺失段通过 processBatchInRound 补救
	}
	picked = round.Backend

	if len(res.Repaired) > 0 {
		logger.Info("batch response repaired", "backend", picked.Name(), "ops", res.Repaired,
			"missing", len(res.Missing))
	}

	// Capture raw response text BEFORE absorbInlineGlossary modifies it in-place.
	rawRespText := resp.Text
	durationMs := time.Since(batchStart).Milliseconds()

	trans, glosEntries, rubyOutputMap := res.Trans, res.Glos, res.RubyOutput

	// Determine batch status and emit event.
	s.emitBatchEvent(batchIndex, idxs, wantIDs, picked.Name(), res, rawRespText, usr,
		glos, resp.Usage, durationMs, tried, logger)

	logger.Debug("batch translated",
		"backend", picked.Name(), "batch_size", len(idxs),
		"prompt_tokens", resp.Usage.PromptTokens,
		"completion_tokens", resp.Usage.CompletionTokens,
		"inline_glossary", len(glosEntries),
		"missing", len(res.Missing))

	s.absorbInlineGlossary(ctx, glosEntries, trans, doc.TargetLang, logger)

	// 写回并对每段做占位符校验；缺失的段用 processBatchInRound 补救。
	// 仅处理 Translate=true 的段落，跳过上下文段落。
	rep := s.reporter()
	var unresolved []int
	var missingIdxs []int
	keepSet := kindSet(s.PreserveKinds)
	wantIDIdx := 0
	for _, idx := range idxs {
		seg := &doc.Segments[idx]
		if isContext(contextSet, idx) {
			// 上下文段落：跳过翻译，不上报进度
			continue
		}
		id := wantIDs[wantIDIdx]
		wantIDIdx++
		text, ok := trans[id]
		if !ok || strings.TrimSpace(text) == "" {
			missingIdxs = append(missingIdxs, idx)
			continue
		}
		// 分发 ruby_output 到各段
		if rubyOutputMap != nil {
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
			retryExpanded := expandBatchWithContext(doc, []int{idx}, len(doc.Segments), s.contextWindow())
			retryBatchSet := map[int]struct{}{idx: {}}
			retryCtxSet := buildContextSet(retryExpanded, retryBatchSet)
			subUnresolved, err := s.processBatchInRound(ctx, doc, retryExpanded, round, batchIndex, logger, retryCtxSet)
			if err != nil {
				return nil, err
			}
			unresolved = append(unresolved, subUnresolved...)
			continue
		}
		if s.Protector != nil {
			if err := s.Protector.Unprotect(seg); err != nil {
				logger.Warn("unprotect failed", "seg", seg.ID, "err", err)
			}
		}
		if s.Restorer != nil {
			restoreSegmentRuby(ctx, seg, s.Restorer, keepSet, s.RubyRetryBackends, s.Retry, logger, s.Reporter)
		}
		s.addTM(ctx, doc, seg, logger)
		rep.SegmentDone()
	}
	for _, idx := range missingIdxs {
		retryExpanded := expandBatchWithContext(doc, []int{idx}, len(doc.Segments), s.contextWindow())
		retryBatchSet := map[int]struct{}{idx: {}}
		retryCtxSet := buildContextSet(retryExpanded, retryBatchSet)
		subUnresolved, err := s.processBatchInRound(ctx, doc, retryExpanded, round, batchIndex, logger, retryCtxSet)
		if err != nil {
			return nil, err
		}
		unresolved = append(unresolved, subUnresolved...)
	}
	return unresolved, nil
}

// shrinkOrFallback 根据 round.FallbackShrink 决定：
//   - 缩小到 >=2 的子批并发递归（每个子批又可能继续缩小）
//   - 否则坍缩到顺序单段（调用 processBatchInRound 处理每个段落）
func (s *Translate) shrinkOrFallback(ctx context.Context, doc *Document, idxs []int, round Round, lastErr error, logger *slog.Logger) ([]int, error) {
	if isFatalBackendError(lastErr) {
		logger.Warn("all backends failed with fatal error, segments will be marked as unresolved",
			"batch_size", len(idxs), "err", lastErr)
		return idxs, nil
	}
	curConstraint := BatchConstraint{
		MaxSegments: len(idxs),
		MaxWords:    round.MaxWordsPerBatch,
	}
	nextConstraint := shrinkConstraint(curConstraint, round.FallbackShrink)
	ctxWin := s.contextWindow()
	if nextConstraint.MaxSegments < 2 {
		// 坍缩到顺序单段：复用 processBatchInRound 处理每个段落
		var unresolved []int
		for _, idx := range idxs {
			retryExpanded := expandBatchWithContext(doc, []int{idx}, len(doc.Segments), ctxWin)
			retryBatchSet := map[int]struct{}{idx: {}}
			retryCtxSet := buildContextSet(retryExpanded, retryBatchSet)
			subUnresolved, err := s.processBatchInRound(ctx, doc, retryExpanded, round, 0, logger, retryCtxSet)
			if err != nil {
				return nil, err
			}
			unresolved = append(unresolved, subUnresolved...)
		}
		return unresolved, nil
	}
	var sub [][]int
	nextSize := nextConstraint.MaxSegments
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
	if err := RunConcurrent(ctx, len(sub), round.Concurrency, func(ctx context.Context, bidx int) error {
		subBatch := sub[bidx]
		subExpanded := expandBatchWithContext(doc, subBatch, len(doc.Segments), ctxWin)
		subBatchSet := make(map[int]struct{}, len(subBatch))
		for _, idx := range subBatch {
			subBatchSet[idx] = struct{}{}
		}
		subCtxSet := buildContextSet(subExpanded, subBatchSet)
		subUnresolved, err := s.processBatchInRound(ctx, doc, subExpanded, round, bidx, logger, subCtxSet)
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

// shrinkConstraint 计算下一级约束。
// MaxSegments 使用 shrinkNext 收敛到单段；
// MaxWords 按相同比例缩放，未启用（<=0）则保持 0。
func shrinkConstraint(cur BatchConstraint, shrink float64) BatchConstraint {
	next := BatchConstraint{
		MaxSegments: shrinkNext(cur.MaxSegments, shrink),
		MaxWords:    0,
	}
	if cur.MaxWords > 0 && shrink > 0 && shrink < 1 {
		next.MaxWords = int(math.Floor(float64(cur.MaxWords) * shrink))
		if next.MaxWords < 1 {
			next.MaxWords = 0
		}
	}
	return next
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

// isContext 检查 idx 是否在 contextSet 中（即作为上下文参考、不需要翻译）。
// contextSet 为 nil 或空时返回 false（所有段落都需要翻译）。
func isContext(contextSet map[int]struct{}, idx int) bool {
	if len(contextSet) == 0 {
		return false
	}
	_, ok := contextSet[idx]
	return ok
}

// contextWindow 从配置计算上下文窗口大小。
func (s *Translate) contextWindow() int {
	if !s.Context.Enabled {
		return 0
	}
	return max(s.Context.Before, s.Context.After)
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

// filterPendingIdxs 过滤出需要翻译的段落索引（排除上下文段落）。
// 用于 shrinkOrFallback 调用前，避免上下文段落被错误翻译。
func filterPendingIdxs(idxs []int, contextSet map[int]struct{}) []int {
	if len(contextSet) == 0 {
		return idxs
	}
	var pending []int
	for _, idx := range idxs {
		if !isContext(contextSet, idx) {
			pending = append(pending, idx)
		}
	}
	return pending
}

// emitBatchEvent constructs a BatchEvent and delivers it via the Reporter's
// BatchObserver interface (if implemented). Called after the backend loop ends
// and before absorbInlineGlossary.
func (s *Translate) emitBatchEvent(
	batchIndex int,
	idxs []int,
	wantIDs []string,
	backendName string,
	res repair.Result,
	rawRespText string,
	sentContent string,
	usedGlossary []prompt.GlossaryEntry,
	usage backend.Usage,
	durationMs int64,
	triedBackends []string,
	logger *slog.Logger,
) {
	rep := s.Reporter
	if rep == nil {
		return
	}
	obs, ok := rep.(progress.BatchObserver)
	if !ok {
		return
	}

	// Build segment IDs list.
	segIDs := make([]string, len(idxs))
	for i, idx := range idxs {
		segIDs[i] = strconv.Itoa(idx)
	}

	// Determine status.
	status := "success"
	errorType := ""
	errorMsg := ""
	if len(res.Missing) > 0 {
		status = "partial"
	}
	if res.ParseErr != nil {
		errorType = "parse_error"
		errorMsg = res.ParseErr.Error()
	}

	evt := progress.BatchEvent{
		Stage:           "translate",
		BatchIndex:      batchIndex,
		SegmentIDs:      segIDs,
		SegmentCount:    len(idxs),
		BackendName:     backendName,
		Status:          status,
		DurationMs:      durationMs,
		InputTokens:     usage.PromptTokens,
		OutputTokens:    usage.CompletionTokens,
		SentContent:     sentContent,
		ReceivedContent: rawRespText,
		UsedGlossary:    usedGlossary,
		AddedGlossary:   res.Glos,
		ErrorType:       errorType,
		ErrorMessage:    errorMsg,
		TriedBackends:   triedBackends,
	}

	obs.OnBatchEvent(evt)
}
