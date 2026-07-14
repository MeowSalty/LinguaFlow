package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/protect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tm"
)

// TranslateHandler 实现 RoundHandler，执行翻译批次处理。
type TranslateHandler struct {
	Backend          backend.Backend
	BatchSize        int
	MaxWordsPerBatch int
	FallbackShrink   float64
	Retry            backend.RetryPolicy
	ResponseMode     string

	Renderer *prompt.Renderer
	Glossary glossary.Glossary
	TM       tm.TranslationMemory
	Repair   repair.Options
	Context  ContextConfig

	Protector         protect.Protector
	RubyEnabled       bool
	RubyPreserveKinds []string
	RubyMode          string
	Postprocess       *PostprocessConfig

	RubyRestorer      *ruby.Restorer
	RubyRetryBackends []backend.Backend

	InlineBootstrap        bool
	MaxTermsPer1000Chars   float64
	MinBootstrapSourceLen  int
	InlineConflictStrategy string

	Reporter progress.Reporter
	Logger   *slog.Logger
}

func (h *TranslateHandler) ModeName() string { return "translate" }

func (h *TranslateHandler) Finalize(_ context.Context, doc *Document, unresolved []int) error {
	sort.Ints(unresolved)
	if len(unresolved) > 0 {
		failedIndices := make([]string, 0, len(unresolved))
		for _, idx := range unresolved {
			failedIndices = append(failedIndices, strconv.Itoa(idx))
		}
		if doc.Vars == nil {
			doc.Vars = map[string]any{}
		}
		doc.Vars["_translate_failed_indices"] = strings.Join(failedIndices, ",")
		h.logger().Warn("translate round exhausted", "count", len(unresolved))
	} else {
		if doc.Vars != nil {
			delete(doc.Vars, "_translate_failed_indices")
		}
	}
	return nil
}

func (h *TranslateHandler) logger() *slog.Logger {
	if h.Logger == nil {
		return slog.Default()
	}
	return h.Logger
}

func (h *TranslateHandler) reporter() progress.Reporter {
	if h.Reporter == nil {
		return progress.Nop{}
	}
	return h.Reporter
}

// BuildBatches 收集待翻译段落、执行 Protect、分批、上下文扩展。
func (h *TranslateHandler) BuildBatches(ctx context.Context, doc *Document) ([][]int, error) {
	logger := h.logger()

	// 1. 收集 pending（Translate=true 的段落）
	var pending []int
	skippedCount := 0
	for i := range doc.Segments {
		seg := &doc.Segments[i]
		if seg.Skip {
			seg.Target = seg.Source
			skippedCount++
			continue
		}
		if !seg.Translate {
			continue
		}
		if strings.TrimSpace(seg.Source) == "" || IsDecorativeSeparator(seg) {
			seg.Target = seg.Source
			skippedCount++
			continue
		}
		pending = append(pending, i)
	}

	if len(pending) == 0 {
		h.writeSkippedCount(doc, skippedCount)
		return nil, nil
	}

	// 2. Protect
	if h.Protector != nil {
		filtered := pending[:0]
		for _, idx := range pending {
			seg := &doc.Segments[idx]
			if seg.OriginalSource == "" {
				seg.OriginalSource = seg.Source
			}
			if err := h.Protector.Protect(seg); err != nil {
				return nil, fmt.Errorf("protect segment %d: %w", idx, err)
			}
			if IsPlaceholderOnly(seg) {
				seg.Target = seg.OriginalSource
				skippedCount++
				continue
			}
			filtered = append(filtered, idx)
		}
		pending = filtered
	}

	// 存储跳过计数
	h.writeSkippedCount(doc, skippedCount)

	if len(pending) == 0 {
		return nil, nil
	}

	// 3. 上下文窗口
	ctxWindow := max(h.Context.Before, h.Context.After)
	if !h.Context.Enabled {
		ctxWindow = 0
	}

	// 4. 分批
	constraint := BatchConstraint{
		MaxSegments: h.BatchSize,
		MaxWords:    h.MaxWordsPerBatch,
	}
	if constraint.MaxSegments <= 0 && constraint.MaxWords <= 0 {
		constraint.MaxSegments = 1
	}
	batches := BuildContextAwareBatches(doc, pending, constraint, ctxWindow, h.Context.Enabled)

	logger.Info("translate handler: batches built",
		"pending", len(pending), "batches", len(batches),
		"batch_size", h.BatchSize, "max_words_per_batch", h.MaxWordsPerBatch,
		"context_enabled", h.Context.Enabled, "context_window", ctxWindow)

	return batches, nil
}

// writeSkippedCount 将跳过计数写入 doc.Vars，保持单调递增语义。
func (h *TranslateHandler) writeSkippedCount(doc *Document, skippedCount int) {
	if doc.Vars == nil {
		doc.Vars = map[string]any{}
	}
	if prev, ok := doc.Vars["_skipped_count"].(int); ok {
		if skippedCount > prev {
			doc.Vars["_skipped_count"] = skippedCount
		}
	} else {
		doc.Vars["_skipped_count"] = skippedCount
	}
}

// ProcessBatch 处理单个翻译批次。
func (h *TranslateHandler) ProcessBatch(ctx context.Context, doc *Document, idxs []int, attempt int, logger *slog.Logger) batchResult {
	batchStart := time.Now()

	// 构建批次集合
	batchSet := make(map[int]struct{}, len(idxs))
	for _, idx := range idxs {
		batchSet[idx] = struct{}{}
	}

	// 计算上下文窗口
	ctxWindow := max(h.Context.Before, h.Context.After)
	if !h.Context.Enabled {
		ctxWindow = 0
	}

	// 扩展上下文
	expandedIdxs := ExpandBatchWithContext(doc, idxs, len(doc.Segments), ctxWindow)
	contextSet := BuildContextSet(expandedIdxs, batchSet)

	// 构建请求
	_, usr, req, wantIDs, _, glos, buildErr := h.buildRequest(ctx, doc, expandedIdxs, contextSet, logger)
	if buildErr != nil {
		logger.Error("build request failed", "err", buildErr)
		return batchResult{unresolved: FilterPendingIdxs(idxs, contextSet)}
	}

	tried := []string{h.Backend.Name()}
	pendingIdxs := FilterPendingIdxs(idxs, contextSet)

	// 调用 LLM
	callStart := time.Now()
	resp, callErr := h.callOnce(ctx, h.Backend, req)

	if callErr != nil {
		if isFatalBackendError(callErr) {
			logger.Error("backend returned fatal error",
				"backend", h.Backend.Name(), "batch_size", len(idxs), "err", callErr)
			h.emitBatchOutcome(progress.BatchEvent{
				Stage:         "translate",
				SegmentIDs:    pendingSegmentIDStrings(pendingIdxs),
				SegmentCount:  len(pendingIdxs),
				BackendName:   h.Backend.Name(),
				Status:        "failed",
				DurationMs:    time.Since(callStart).Milliseconds(),
				SentContent:   usr,
				TriedBackends: tried,
				ErrorType:     "backend_error",
				ErrorMessage:  callErr.Error(),
				HTTPStatus:    httpStatusFromErr(callErr),
			})
			return batchResult{unresolved: pendingIdxs}
		}

		if isRetryableByBackoff(callErr) {
			logger.Warn("backend returned rate limit error, will backoff and retry",
				"backend", h.Backend.Name(), "batch_size", len(idxs), "err", callErr)
			h.emitBatchOutcome(progress.BatchEvent{
				Stage:         "translate",
				SegmentIDs:    pendingSegmentIDStrings(pendingIdxs),
				SegmentCount:  len(pendingIdxs),
				BackendName:   h.Backend.Name(),
				Status:        "failed",
				DurationMs:    time.Since(callStart).Milliseconds(),
				SentContent:   usr,
				TriedBackends: tried,
				ErrorType:     "backend_error",
				ErrorMessage:  callErr.Error(),
				HTTPStatus:    httpStatusFromErr(callErr),
			})
			wait := backoffDuration(attempt, h.Retry, callErr)
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return batchResult{unresolved: pendingIdxs}
			case <-timer.C:
			}
			return batchResult{retry: &batchJob{idxs: idxs, attempt: attempt + 1}}
		}

		logger.Warn("backend failed for batch, shrinking",
			"backend", h.Backend.Name(), "batch_size", len(idxs), "err", callErr)
		h.emitBatchOutcome(progress.BatchEvent{
			Stage:           "translate",
			SegmentIDs:      pendingSegmentIDStrings(pendingIdxs),
			SegmentCount:    len(pendingIdxs),
			BackendName:     h.Backend.Name(),
			Status:          "failed",
			DurationMs:      time.Since(callStart).Milliseconds(),
			SentContent:     usr,
			TriedBackends:   tried,
			ErrorType:       "backend_error",
			ErrorMessage:    callErr.Error(),
			HTTPStatus:      httpStatusFromErr(callErr),
			ShrinkAttempted: len(pendingIdxs) > 1,
		})
		nextSize := shrinkTo(idxs, h.FallbackShrink)
		var dropped []int
		if nextSize < len(idxs) {
			dropped = FilterPendingIdxs(idxs[nextSize:], contextSet)
		}
		return batchResult{
			unresolved: dropped,
			retry:      &batchJob{idxs: idxs[:nextSize], attempt: attempt + 1},
		}
	}

	// 累加 token
	atomic.AddInt64(&doc.InputTokens, resp.Usage.PromptTokens)
	atomic.AddInt64(&doc.OutputTokens, resp.Usage.CompletionTokens)

	// 解析响应
	isTextMode := h.ResponseMode == "text"
	var res repair.Result
	if isTextMode {
		res = parseBatchResponseLenientText(resp.Text, wantIDs, h.Repair)
	} else {
		res = parseBatchResponseLenient(resp.Text, wantIDs, h.Repair)
	}

	if res.ParseErr != nil {
		if upgradedResp, upgradedRes, ok := h.tryPromptUpgrade(ctx, doc, req, resp, res, wantIDs, logger); ok {
			resp = upgradedResp
			res = upgradedRes
		} else {
			logger.Warn("batch response parse failed, shrinking",
				"backend", h.Backend.Name(), "batch_size", len(pendingIdxs), "err", res.ParseErr,
				"resp_len", len(resp.Text), "resp_head", headSnippet(resp.Text, 200),
				"repaired", res.Repaired)
			h.emitBatchOutcome(progress.BatchEvent{
				Stage:           "translate",
				SegmentIDs:      pendingSegmentIDStrings(pendingIdxs),
				SegmentCount:    len(pendingIdxs),
				BackendName:     h.Backend.Name(),
				Status:          "failed",
				DurationMs:      time.Since(callStart).Milliseconds(),
				InputTokens:     resp.Usage.PromptTokens,
				OutputTokens:    resp.Usage.CompletionTokens,
				SentContent:     usr,
				ReceivedContent: resp.Text,
				TriedBackends:   tried,
				ErrorType:       "parse_error",
				ErrorMessage:    res.ParseErr.Error(),
				ShrinkAttempted: len(pendingIdxs) > 1,
			})
			nextSize := shrinkTo(idxs, h.FallbackShrink)
			var dropped []int
			if nextSize < len(idxs) {
				dropped = FilterPendingIdxs(idxs[nextSize:], contextSet)
			}
			return batchResult{
				unresolved: dropped,
				retry:      &batchJob{idxs: idxs[:nextSize], attempt: attempt + 1},
			}
		}
	}

	missingRatio := 0.0
	if len(wantIDs) > 0 {
		missingRatio = float64(len(res.Missing)) / float64(len(wantIDs))
	}
	if len(res.Missing) > 0 && (!h.Repair.Partial || missingRatio >= h.Repair.PartialThreshold) {
		logger.Warn("partial recovery exceeded threshold, using best partial result",
			"backend", h.Backend.Name(), "missing", len(res.Missing), "total", len(wantIDs),
			"threshold", h.Repair.PartialThreshold, "partial_enabled", h.Repair.Partial)
	}

	if len(res.Repaired) > 0 {
		logger.Info("batch response repaired", "backend", h.Backend.Name(), "ops", res.Repaired,
			"missing", len(res.Missing))
	}

	rawRespText := resp.Text
	durationMs := time.Since(batchStart).Milliseconds()

	trans, glosEntries, rubyOutputMap := res.Trans, res.Glos, res.RubyOutput

	h.emitBatchEvent(pendingIdxs, wantIDs, h.Backend.Name(), res, rawRespText, usr,
		glos, resp.Usage, durationMs, tried, logger)

	logger.Debug("batch translated",
		"backend", h.Backend.Name(), "batch_size", len(idxs),
		"prompt_tokens", resp.Usage.PromptTokens,
		"completion_tokens", resp.Usage.CompletionTokens,
		"inline_glossary", len(glosEntries),
		"missing", len(res.Missing))

	h.absorbInlineGlossary(ctx, glosEntries, trans, doc.TargetLang, logger)

	unresolved, missing := h.processTranslatedSegments(ctx, doc, expandedIdxs, wantIDs, trans, rubyOutputMap, contextSet, logger)

	callbackResult := BuildBatchResult(doc, expandedIdxs, contextSet)
	return batchResult{unresolved: unresolved, missing: missing, callbackResult: &callbackResult}
}

// callOnce 调用后端翻译接口。
func (h *TranslateHandler) callOnce(ctx context.Context, b backend.Backend, req backend.Request) (*backend.Response, error) {
	return b.Translate(ctx, req)
}

// buildRequest 构建翻译请求的 prompt 和 backend.Request。
func (h *TranslateHandler) buildRequest(
	ctx context.Context,
	doc *Document,
	idxs []int,
	contextSet map[int]struct{},
	logger *slog.Logger,
) (string, string, backend.Request, []string, map[int]string, []prompt.GlossaryEntry, error) {
	renderer := h.Renderer
	isTextMode := h.ResponseMode == "text"

	glos, tmHints := h.lookupHints(ctx, doc, idxs, logger)

	inputs := make([]prompt.SegmentInput, len(idxs))
	idMap := make(map[int]string, len(idxs))
	var wantIDs []string
	batchSources := make([]string, 0, len(idxs))
	transIdx := 0
	for k, idx := range idxs {
		seg := doc.Segments[idx]
		source := seg.Source
		isCtx := IsContext(contextSet, idx)
		if isCtx && seg.OriginalSource != "" {
			source = seg.OriginalSource
		}

		var id string
		if isTextMode {
			if isCtx {
				id = "*"
			} else {
				transIdx++
				id = strconv.Itoa(transIdx)
			}
		} else {
			id = strconv.Itoa(k + 1)
		}
		idMap[idx] = id
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
		InlineBootstrap:   h.InlineBootstrap,
		MaxBootstrapTerms: h.calcMaxBootstrapTerms(batchSources),
		StrictSchema:      !isTextMode,
		TextMode:          isTextMode,
		RubyAnnotations:   rubyAnns,
		RubyMode:          h.RubyMode,
	}
	sys, usr, err := renderer.Render(data)
	if err != nil {
		return "", "", backend.Request{}, nil, nil, nil, fmt.Errorf("render batch prompt (%d segs): %w", len(idxs), err)
	}

	req := backend.Request{
		System: sys,
		User:   usr,
	}
	if !isTextMode {
		req.JSONSchema = translationsSchema(wantIDs, h.InlineBootstrap, h.RubyMode != "")
	} else {
		req.ResponseFormat = "none"
	}

	return sys, usr, req, wantIDs, idMap, glos, nil
}

// tryPromptUpgrade 尝试通过附加反例 reminder 重试一次。
func (h *TranslateHandler) tryPromptUpgrade(
	ctx context.Context,
	doc *Document,
	req backend.Request,
	resp *backend.Response,
	res repair.Result,
	wantIDs []string,
	logger *slog.Logger,
) (*backend.Response, repair.Result, bool) {
	if !h.Repair.PromptUpgrade || res.ParseErr == nil {
		return resp, res, false
	}

	isTextMode := h.ResponseMode == "text"

	reminder := repair.BuildRetryReminder(nil, res.ParseErr, headSnippet(resp.Text, 200))
	req2 := req
	req2.System = req.System + reminder

	resp2, err2 := h.callOnce(ctx, h.Backend, req2)
	if err2 != nil {
		return resp, res, false
	}

	var res2 repair.Result
	if isTextMode {
		res2 = parseBatchResponseLenientText(resp2.Text, wantIDs, h.Repair)
	} else {
		res2 = parseBatchResponseLenient(resp2.Text, wantIDs, h.Repair)
	}
	if res2.ParseErr != nil {
		return resp, res, false
	}

	logger.Info("batch response recovered by prompt upgrade",
		"backend", h.Backend.Name(), "repaired", res2.Repaired)
	atomic.AddInt64(&doc.InputTokens, resp2.Usage.PromptTokens)
	atomic.AddInt64(&doc.OutputTokens, resp2.Usage.CompletionTokens)
	return resp2, res2, true
}

// processTranslatedSegments 处理翻译结果：写回译文、占位符校验、Unprotect/RubyRestore/TM。
func (h *TranslateHandler) processTranslatedSegments(
	ctx context.Context,
	doc *Document,
	idxs []int,
	wantIDs []string,
	trans map[string]string,
	rubyOutputMap map[string][]ruby.OutputEntry,
	contextSet map[int]struct{},
	logger *slog.Logger,
) (unresolved []int, missing []int) {
	rep := h.reporter()
	wantIDIdx := 0
	for _, idx := range idxs {
		seg := &doc.Segments[idx]
		if IsContext(contextSet, idx) {
			continue
		}
		id := wantIDs[wantIDIdx]
		wantIDIdx++
		text, ok := trans[id]
		if !ok || strings.TrimSpace(text) == "" {
			missing = append(missing, idx)
			continue
		}
		if rubyOutputMap != nil {
			if ro, rok := rubyOutputMap[id]; rok && len(ro) > 0 {
				if _, hasAnns := seg.Meta["ruby_annotations"]; hasAnns {
					seg.Meta["ruby_output"] = ro
				}
			}
		}
		if h.Repair.PlaceholderNormalize {
			if normText, normalized := repair.NormalizePlaceholders(text, seg.Protected); len(normalized) > 0 {
				logger.Info("placeholders normalized",
					"seg", seg.ID, "normalized", normalized)
				text = normText
			}
		}
		seg.Target = text
		if missingPH := protect.MissingPlaceholders(seg); len(missingPH) > 0 {
			logger.Warn("batch segment placeholders missing",
				"seg", seg.ID, "missing", missingPH)
			seg.Target = ""
			unresolved = append(unresolved, idx)
			continue
		}

		// TrimSpaces（在 Unprotect 之前）
		if h.Postprocess != nil && h.Postprocess.TrimSpaces {
			seg.Target = strings.TrimSpace(seg.Target)
		}

		// Unprotect
		if h.Protector != nil {
			if err := h.Protector.Unprotect(seg); err != nil {
				logger.Warn("unprotect failed", "seg", seg.ID, "err", err)
			}
		}

		// RubyRestore
		if h.RubyEnabled && h.RubyRestorer != nil {
			keepSet := kindSet(h.RubyPreserveKinds)
			isTextMode := h.ResponseMode == "text"
			restoreSegmentRuby(ctx, seg, h.RubyRestorer, keepSet,
				h.RubyRetryBackends, h.Retry, logger, h.Reporter, isTextMode)
		}

		// TM（直接调用，使用 OriginalSource）
		if h.TM != nil {
			source := seg.OriginalSource
			if source == "" {
				source = seg.Source
			}
			if err := h.TM.Add(ctx, source, seg.Target, doc.SourceLang, doc.TargetLang); err != nil {
				logger.Debug("tm add failed", "err", err)
			}
		}

		rep.SegmentDone()
	}
	return unresolved, missing
}

// lookupHints 为 idxs 中每段查 glossary / TM 并合并去重。
func (h *TranslateHandler) lookupHints(ctx context.Context, doc *Document, idxs []int, logger *slog.Logger) ([]prompt.GlossaryEntry, []prompt.TMHint) {
	if ctx.Err() != nil {
		return nil, nil
	}
	var (
		glosOrder []string
		glosMap   = map[string]prompt.GlossaryEntry{}
		tmOrder   []string
		tmMap     = map[string]prompt.TMHint{}
	)
	for _, idx := range idxs {
		seg := &doc.Segments[idx]
		if h.Glossary != nil {
			hits, err := h.Glossary.Lookup(ctx, seg.Source, doc.SourceLang, doc.TargetLang)
			if err != nil {
				logger.Warn("glossary lookup failed", "err", err, "seg", seg.ID)
			}
			for _, hit := range hits {
				key := hit.Source + "\x00" + hit.Target
				if _, ok := glosMap[key]; !ok {
					glosOrder = append(glosOrder, key)
				}
				glosMap[key] = prompt.GlossaryEntry{Source: hit.Source, Target: hit.Target, Notes: hit.Notes}
			}
		}
		if h.TM != nil {
			ms, err := h.TM.Search(ctx, seg.Source, doc.SourceLang, doc.TargetLang)
			if err != nil {
				logger.Warn("tm search failed", "err", err, "seg", seg.ID)
			}
			for _, m := range ms {
				key := m.Source + "\x00" + m.Target
				if old, ok := tmMap[key]; !ok {
					tmOrder = append(tmOrder, key)
					tmMap[key] = prompt.TMHint{Source: m.Source, Target: m.Target, Score: m.Score}
				} else if m.Score > old.Score {
					tmMap[key] = prompt.TMHint{Source: m.Source, Target: m.Target, Score: m.Score}
				}
			}
		}
	}
	glos := make([]prompt.GlossaryEntry, 0, len(glosOrder))
	for _, k := range glosOrder {
		glos = append(glos, glosMap[k])
	}
	hints := make([]prompt.TMHint, 0, len(tmOrder))
	for _, k := range tmOrder {
		hints = append(hints, tmMap[k])
	}
	return glos, hints
}

// absorbInlineGlossary 把 LLM 在 translate 响应中携带的 glossary 条目写入运行时 Glossary。
func (h *TranslateHandler) absorbInlineGlossary(
	ctx context.Context,
	entries []prompt.BootstrapEntry,
	translations map[string]string,
	targetLang string,
	logger *slog.Logger,
) {
	if !h.InlineBootstrap || len(entries) == 0 || h.Glossary == nil {
		return
	}
	minLen := h.MinBootstrapSourceLen
	if minLen < 1 {
		minLen = 2
	}
	candidates := make([]glossary.Entry, 0, len(entries))
	for _, e := range entries {
		if len([]rune(e.Source)) < minLen {
			continue
		}
		if e.Source == "" || e.Target == "" {
			continue
		}
		candidates = append(candidates, glossary.Entry{
			Source: e.Source,
			Target: e.Target,
			Notes:  e.Notes,
		})
	}
	if len(candidates) == 0 {
		return
	}
	result, err := h.Glossary.Add(ctx, candidates...)
	if err != nil {
		logger.Warn("inline glossary add failed", "err", err)
	}
	if len(result.Added) > 0 {
		logger.Debug("inline glossary absorbed",
			"added", len(result.Added),
			"skipped", len(result.Skipped),
			"received", len(entries))
	}

	if h.InlineConflictStrategy != InlineConflictRewriteLocal {
		return
	}
	if len(result.Skipped) == 0 || len(translations) == 0 {
		return
	}
	h.rewriteConflictsInBatch(result.Skipped, translations, targetLang, logger)
}

// rewriteConflictsInBatch 遍历 Skipped 列表，把本批译文里 worker 自己用的 target 字面值
// 替换为权威表里已有的版本。
func (h *TranslateHandler) rewriteConflictsInBatch(
	skipped []glossary.SkippedEntry,
	translations map[string]string,
	targetLang string,
	logger *slog.Logger,
) {
	for _, sk := range skipped {
		if sk.Reason != glossary.SkipReasonExists {
			continue
		}
		from := sk.Proposed.Target
		to := sk.Existing.Target
		if from == "" || from == to {
			continue
		}
		rewrote := 0
		var warns []string
		for id, text := range translations {
			newText, replaced, warn := glossary.SafeReplace(text, from, to, targetLang)
			if replaced {
				translations[id] = newText
				rewrote++
			}
			if warn != "" {
				warns = append(warns, warn)
			}
		}
		if rewrote > 0 {
			logger.Info("inline glossary conflict: rewrote local target",
				"source", sk.Proposed.Source,
				"from", from,
				"to", to,
				"rewrites", rewrote)
		}
		if len(warns) > 0 {
			logger.Warn("inline glossary conflict: ambiguous match",
				"source", sk.Proposed.Source,
				"proposed_target", from,
				"authoritative_target", to,
				"details", warns)
		}
	}
}

// calcMaxBootstrapTerms 基于文本字词数动态计算本批最大术语抽取数。
func (h *TranslateHandler) calcMaxBootstrapTerms(segments []string) int {
	coeff := h.MaxTermsPer1000Chars
	if coeff <= 0 {
		coeff = 3.0
	}
	totalWords := 0
	for _, seg := range segments {
		totalWords += CountWords(seg)
	}
	maxTerms := int(math.Ceil(float64(totalWords) / 1000.0 * coeff))
	return max(maxTerms, 1)
}

// emitBatchOutcome 发送批次事件到 Reporter。
func (h *TranslateHandler) emitBatchOutcome(evt progress.BatchEvent) {
	rep := h.Reporter
	if rep == nil {
		return
	}
	obs, ok := rep.(progress.BatchObserver)
	if !ok {
		return
	}
	obs.OnBatchEvent(evt)
}

// emitBatchEvent 发送成功的批次事件。
func (h *TranslateHandler) emitBatchEvent(
	pendingIdxs []int,
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
	segIDs := pendingSegmentIDStrings(pendingIdxs)

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

	h.emitBatchOutcome(progress.BatchEvent{
		Stage:           "translate",
		SegmentIDs:      segIDs,
		SegmentCount:    len(pendingIdxs),
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
	})
}

// headSnippet 截取字符串前 n 个字符。
func headSnippet(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// extractRubyAnnotationsFromDoc 从文档段落中提取注音注释。
func extractRubyAnnotationsFromDoc(doc *Document, idxs []int, idMap map[int]string) map[string][]prompt.RubyAnnotation {
	result := make(map[string][]prompt.RubyAnnotation)
	for _, idx := range idxs {
		seg := doc.Segments[idx]
		raw, ok := seg.Meta["ruby_annotations"]
		if !ok {
			continue
		}
		annots, ok := raw.([]ruby.Annotation)
		if !ok {
			continue
		}
		converted := make([]prompt.RubyAnnotation, len(annots))
		for i, a := range annots {
			converted[i] = prompt.RubyAnnotation{Base: a.Base, Text: a.Text}
		}
		if len(converted) > 0 {
			key := seg.ID
			if idMap != nil {
				if mapped, ok := idMap[idx]; ok {
					key = mapped
				}
			}
			result[key] = converted
		}
	}
	return result
}
