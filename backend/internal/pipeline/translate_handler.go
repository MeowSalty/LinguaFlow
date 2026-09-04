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
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
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

	RubyRetryBackends []backend.Backend
	RubyRetryAttempts int // 注音对齐定向重试轮数；<=0 兜底为 1（仅 backends 非空时生效）

	InlineBootstrap        bool
	MaxTermsPer1000Chars   float64
	MinBootstrapSourceLen  int
	InlineConflictStrategy string

	Reporter progress.Reporter
	Logger   *slog.Logger

	// Gate 是任务级暂停闸门（退避重试等待中止信号）；nil 时无暂停语义。
	// 由 RunRound 调用方经 engine.Round 注入（见 roundexecutor 的 Slots/Gate 注入）。
	Gate *PauseGate

	RoundIndex int // execution plan round index, set by caller
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

// BuildBatches 收集待翻译段落、执行 Protect、分批、上下文扩展。
// pending==nil：池 0，先对全部非 Skip 段做一次保护链分析（结构段标记 + 保护产物，
// 含 Translate=false 的上下文候选段），再扫描 doc；保护产物由 Protect 循环落盘，
// 保护链对每段恰好执行一次。pending!=nil：池间重切，不重扫不 Protect，复用池 0 标记。
func (h *TranslateHandler) BuildBatches(ctx context.Context, doc *Document, pending []int, poolIndex int) ([][]int, error) {
	logger := h.logger()

	if pending == nil {
		// 结构段标记 + 保护分析：必须是本分支第一条语句——先于扫描循环与下方 buildEligibleWordPrefix
		// （前缀和直接消费 isContextEligible），顺序颠倒会导致字数预估与实际发送静默偏离。
		// 判定与实际保护是同一次链执行：翻译目标段的 AnalyzeStructural 产物缓存进 analyses，
		// 下方 Protect 循环经 Apply 落盘、不重跑链；判定随本轮保护配置浮动，
		// rawSource 保证跨轮持久 doc（成功段 Source 已 key 化）与 DB 每轮重建
		// 两条路径输入均为原始文本。
		// Skip 段不标记：两个消费方（扫描循环、isContextEligible）都先被 Skip 短路，
		// 标记从不被读，零值 false 即 fail-open；也免去 ASS 头部等大 Skip 段的整链成本。
		analyses := make([]protect.Analysis, len(doc.Segments))
		for i := range doc.Segments {
			seg := &doc.Segments[i]
			if seg.Skip {
				continue
			}
			if seg.Translate {
				// 翻译目标段：保护产物留待 Protect 循环落盘。
				analyses[i] = protect.AnalyzeStructural(h.Protector, rawSource(seg))
				seg.StructuralOnly = analyses[i].Structural
				continue
			}
			// 上下文候选段（Translate=false）：上下文恒发原文、保护形态从不落盘，只取判定结论。
			seg.StructuralOnly = protect.IsStructuralOnly(h.Protector, rawSource(seg))
		}
		var scan []int
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
			// 结构段（纯占位符/纯标点/空段/占位符+标点混合）无可译文本：不送翻，
			// 原文透传，也不进入任何批次的上下文（isContextEligible 同源排除）。
			if seg.StructuralOnly {
				seg.Target = rawSource(seg)
				skippedCount++
				continue
			}
			scan = append(scan, i)
		}

		if len(scan) == 0 {
			h.writeSkippedCount(doc, skippedCount)
			return nil, nil
		}

		if h.Protector != nil {
			for _, idx := range scan {
				seg := &doc.Segments[idx]
				if seg.OriginalSource == "" {
					seg.OriginalSource = seg.Source
				}
				// 池 0 的标记分析已对同一段文本跑过同一条保护链（scan 段 Source 恒等于
				// rawSource），此处落盘缓存产物而非重跑链；分析失败与直接 Protect 失败同语义。
				if err := analyses[idx].Apply(seg); err != nil {
					return nil, fmt.Errorf("protect segment %d: %w", idx, err)
				}
			}
		}

		h.writeSkippedCount(doc, skippedCount)
		pending = scan
	}

	if len(pending) == 0 {
		return nil, nil
	}

	ctxWindow := max(h.Context.Before, h.Context.After)
	if !h.Context.Enabled {
		ctxWindow = 0
	}

	constraint := BatchConstraint{
		MaxSegments: h.BatchSize,
		MaxWords:    h.MaxWordsPerBatch,
	}
	if constraint.MaxSegments <= 0 && constraint.MaxWords <= 0 {
		constraint.MaxSegments = 1
	}
	constraint = shrinkConstraint(constraint, h.FallbackShrink, poolIndex)
	var estimator contextWordEstimator
	if h.Context.Enabled && ctxWindow > 0 && constraint.MaxWords > 0 {
		eligiblePrefix := buildEligibleWordPrefix(doc)
		estimator = func(candidate []int) int {
			return estimateContextWordsWithPrefix(doc, candidate, ctxWindow, eligiblePrefix)
		}
	}
	batches := BuildContextAwareBatches(doc, pending, constraint, ctxWindow, h.Context.Enabled, estimator)

	logger.Info("translate handler: batches built",
		"pending", len(pending), "batches", len(batches),
		"pool", poolIndex,
		"max_segments", constraint.MaxSegments, "max_words", constraint.MaxWords,
		"context_enabled", h.Context.Enabled, "context_window", ctxWindow)

	return batches, nil
}

// shrinkConstraint 按池索引缩放批次约束：floor(orig * shrink^poolIndex)，下限 clamp 到 1。
func shrinkConstraint(orig BatchConstraint, shrink float64, poolIndex int) BatchConstraint {
	if poolIndex <= 0 || shrink <= 0 || shrink >= 1 || math.IsNaN(shrink) || math.IsInf(shrink, 0) {
		return orig
	}
	factor := math.Pow(shrink, float64(poolIndex))
	out := orig
	if orig.MaxSegments > 0 {
		next := int(math.Floor(float64(orig.MaxSegments) * factor))
		if next < 1 {
			next = 1
		}
		out.MaxSegments = next
	}
	if orig.MaxWords > 0 {
		next := int(math.Floor(float64(orig.MaxWords) * factor))
		if next < 1 {
			next = 1
		}
		out.MaxWords = next
	}
	return out
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
	expanded := ExpandBatchWithContext(doc, idxs, len(doc.Segments), ctxWindow, h.Context.MaxChars)
	expandedIdxs := expanded.Idxs
	contextSet := BuildContextSet(expandedIdxs, batchSet)

	// 构建请求
	sys, usr, req, wantIDs, _, glos, buildErr := h.buildRequest(ctx, doc, expandedIdxs, contextSet, expanded.TruncatedSrc, logger)
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
				Stage:          "translate",
				SegmentIDs:     segmentIDStringsFromDoc(doc, pendingIdxs),
				SegmentCount:   len(pendingIdxs),
				BackendName:    h.Backend.Name(),
				Status:         "failed",
				DurationMs:     time.Since(callStart).Milliseconds(),
				SentContent:    usr,
				TriedBackends:  tried,
				ErrorType:      "backend_error",
				ErrorMessage:   callErr.Error(),
				HTTPStatus:     httpStatusFromErr(callErr),
				RoundIndex:     h.RoundIndex,
				Attempt:        attempt,
				SystemPrompt:   sys,
				UserMessage:    usr,
				ResponseFormat: req.ResponseFormat,
				JSONSchema:     req.JSONSchema,
			})
			// 401/403 升级为 fatalUnresolved：跳过剩余池（同 backend 重试无意义），
			// 跨轮传播语义不变（仍可换 backend 接力）。
			return batchResult{fatalUnresolved: pendingIdxs}
		}

		if backend.IsRetryable(callErr) {
			logger.Warn("backend returned retryable error, will backoff and retry",
				"backend", h.Backend.Name(), "batch_size", len(idxs), "err", callErr)
			h.emitBatchOutcome(progress.BatchEvent{
				Stage:          "translate",
				SegmentIDs:     segmentIDStringsFromDoc(doc, pendingIdxs),
				SegmentCount:   len(pendingIdxs),
				BackendName:    h.Backend.Name(),
				Status:         "failed",
				DurationMs:     time.Since(callStart).Milliseconds(),
				SentContent:    usr,
				TriedBackends:  tried,
				ErrorType:      "backend_error",
				ErrorMessage:   callErr.Error(),
				HTTPStatus:     httpStatusFromErr(callErr),
				RoundIndex:     h.RoundIndex,
				Attempt:        attempt,
				SystemPrompt:   sys,
				UserMessage:    usr,
				ResponseFormat: req.ResponseFormat,
				JSONSchema:     req.JSONSchema,
			})
			wait := backoffDuration(attempt, h.Retry, callErr)
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return batchResult{unresolved: pendingIdxs}
			case <-h.Gate.Done():
				// 暂停时中止退避等待：段保持未解决，由断点集合覆盖。
				timer.Stop()
				return batchResult{unresolved: pendingIdxs}
			case <-timer.C:
			}
			return batchResult{retry: &batchJob{idxs: idxs, attempt: attempt + 1}}
		}

		logger.Warn("backend failed for batch, deferring to next pool",
			"backend", h.Backend.Name(), "batch_size", len(idxs), "err", callErr)
		h.emitBatchOutcome(progress.BatchEvent{
			Stage:          "translate",
			SegmentIDs:     segmentIDStringsFromDoc(doc, pendingIdxs),
			SegmentCount:   len(pendingIdxs),
			BackendName:    h.Backend.Name(),
			Status:         "failed",
			DurationMs:     time.Since(callStart).Milliseconds(),
			SentContent:    usr,
			TriedBackends:  tried,
			ErrorType:      "backend_error",
			ErrorMessage:   callErr.Error(),
			HTTPStatus:     httpStatusFromErr(callErr),
			RoundIndex:     h.RoundIndex,
			Attempt:        attempt,
			SystemPrompt:   sys,
			UserMessage:    usr,
			ResponseFormat: req.ResponseFormat,
			JSONSchema:     req.JSONSchema,
		})
		return batchResult{unresolved: pendingIdxs}
	}

	// 累加 token
	if resp.Truncated {
		logTruncatedResponse(logger, h.Backend.Name())
	}
	atomic.AddInt64(&doc.InputTokens, resp.Usage.PromptTokens)
	atomic.AddInt64(&doc.OutputTokens, resp.Usage.CompletionTokens)

	// 解析响应
	isTextMode := prompt.ProtocolFromResponseMode(h.ResponseMode).IsText()
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
			logger.Warn("batch response parse failed, deferring to next pool",
				"backend", h.Backend.Name(), "batch_size", len(pendingIdxs), "err", res.ParseErr,
				"resp_len", len(resp.Text), "resp_head", headSnippet(resp.Text, 200),
				"repaired", res.Repaired)
			h.emitBatchOutcome(progress.BatchEvent{
				Stage:           "translate",
				SegmentIDs:      segmentIDStringsFromDoc(doc, pendingIdxs),
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
				Truncated:       resp.Truncated,
				Repaired:        res.Repaired,
				RoundIndex:      h.RoundIndex,
				Attempt:         attempt,
				SystemPrompt:    sys,
				UserMessage:     usr,
				ResponseFormat:  req.ResponseFormat,
				JSONSchema:      req.JSONSchema,
				ResponseContent: resp.Text,
			})
			return batchResult{unresolved: pendingIdxs}
		}
	}

	if len(res.Missing) > 0 {
		logger.Warn("partial recovery, using best partial result",
			"backend", h.Backend.Name(), "missing", len(res.Missing), "total", len(wantIDs))
	}

	if len(res.Repaired) > 0 {
		logger.Info("batch response repaired", "backend", h.Backend.Name(), "ops", res.Repaired,
			"missing", len(res.Missing))
	}

	rawRespText := resp.Text
	durationMs := time.Since(batchStart).Milliseconds()

	trans, glosEntries, rubyOutputMap := res.Trans, res.Glos, res.RubyOutput

	h.emitBatchEvent(doc, pendingIdxs, wantIDs, h.Backend.Name(), res, rawRespText, sys, usr,
		req.ResponseFormat, req.JSONSchema, attempt, glos, resp.Usage, resp.Truncated, durationMs, tried, logger)

	logger.Debug("batch translated",
		"backend", h.Backend.Name(), "batch_size", len(idxs),
		"prompt_tokens", resp.Usage.PromptTokens,
		"completion_tokens", resp.Usage.CompletionTokens,
		"inline_glossary", len(glosEntries),
		"missing", len(res.Missing))

	h.absorbInlineGlossary(ctx, glosEntries, trans, doc.TargetLang, logger)

	unresolved := h.processTranslatedSegments(ctx, doc, expandedIdxs, wantIDs, trans, rubyOutputMap, contextSet, logger)

	callbackResult := BuildBatchResult(doc, expandedIdxs, contextSet)
	return batchResult{unresolved: unresolved, callbackResult: &callbackResult}
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
	truncatedSrc map[int]string,
	logger *slog.Logger,
) (string, string, backend.Request, []string, map[int]string, []prompt.GlossaryEntry, error) {
	renderer := h.Renderer
	proto := prompt.ProtocolFromResponseMode(h.ResponseMode)
	isTextMode := proto.IsText()

	glos, tmHints := h.lookupHints(ctx, doc, idxs, logger)

	inputs := make([]prompt.SegmentInput, len(idxs))
	idMap := make(map[int]string, len(idxs))
	var wantIDs []string
	batchSources := make([]string, 0, len(idxs))
	transIdx := 0
	for k, idx := range idxs {
		seg := doc.Segments[idx]
		// 批次段发送 seg.Source（key 化保护形态）；仅上下文段回退保护前原文。
		source := seg.Source
		isCtx := IsContext(contextSet, idx)
		if isCtx {
			source = rawSource(&seg)
			if trunc, ok := truncatedSrc[idx]; ok {
				source = trunc
			}
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
		Protocol:          proto,
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
	if isTextMode {
		req.ResponseFormat = "none"
	} else {
		req.JSONSchema = translationsSchema(wantIDs, h.InlineBootstrap, h.RubyMode != "")
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

	isTextMode := prompt.ProtocolFromResponseMode(h.ResponseMode).IsText()

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
) (unresolved []int) {
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
			// LLM 本轮漏译：清空 Target/Issues 使该段以「本轮无产出」形态流入
			// BuildBatchResult（TargetText="" ⇒ Failed=true）。否则 DB 重载的旧译文
			// 非空会被下游空串守卫放过，旧裁决连同对未变更文本新扫的同指纹 issue
			// 一并落库，产生 dismissed + pending 孪生条目（旧裁决本应随文本消亡）。
			// 与下方占位符违规分支的 seg.Target = "" 同口径。
			seg.Target = ""
			seg.Issues = nil
			unresolved = append(unresolved, idx)
			continue
		}
		if rubyOutputMap != nil {
			if ro, rok := rubyOutputMap[id]; rok && len(ro) > 0 {
				// inline 模式：LLM 已把标记内嵌进译文，标记即真相，不合并进 items
				if !strings.Contains(text, "⟦ruby:") {
					if items, ok := seg.Meta["ruby_items"].([]ruby.Item); ok && len(items) > 0 {
						ruby.MergeByOutput(items, ro)
						seg.Meta["ruby_items"] = items
					}
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
		// translate 轮旧裁决不跨文本存活：成功段先清空 DB 重载的旧 issues，
		// 防止重译段复活陈旧裁决（须在下方 RubyRestore 追加守恒 issue 之前）。
		seg.Issues = nil
		seg.Target = text
		missingPH, duplicatedPH, inventedPH := protect.PlaceholderViolations(seg)
		if len(missingPH) > 0 || len(duplicatedPH) > 0 || len(inventedPH) > 0 {
			logger.Warn("batch segment placeholder integrity violation",
				"seg", seg.ID,
				"missing", missingPH,
				"duplicated", duplicatedPH,
				"invented", inventedPH)
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
		if h.RubyEnabled {
			keepSet := kindSet(h.RubyPreserveKinds)
			isTextMode := prompt.ProtocolFromResponseMode(h.ResponseMode).IsText()
			outcome := restoreSegmentRuby(ctx, seg, keepSet,
				h.RubyRetryBackends, h.Retry, logger, h.Reporter, isTextMode, h.RoundIndex, h.Repair,
				h.RubyRetryAttempts)
			// 守恒信号：应还原条目存在但未全部还原时追加 warning issue，
			// 经 BuildBatchResult → worker translate batchHandler 落库。
			if outcome.Want > 0 && outcome.Restored < outcome.Want {
				seg.Issues = append(seg.Issues, qa.QualityIssue{
					SegmentIndex: idx,
					Severity:     qa.SeverityWarning,
					Code:         qa.CodeRubyRestoreIncomplete,
					Message:      fmt.Sprintf("注音还原不完整：应还原 %d 条，实际 %d 条", outcome.Want, outcome.Restored),
				})
			}
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
	}
	return unresolved
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
				if hit.Forbidden {
					continue
				}
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
			Source:    e.Source,
			Target:    e.Target,
			Mandatory: false,
			Notes:     e.Notes,
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
	doc *Document,
	pendingIdxs []int,
	wantIDs []string,
	backendName string,
	res repair.Result,
	rawRespText string,
	sys string,
	usr string,
	responseFormat string,
	jsonSchema map[string]any,
	attempt int,
	usedGlossary []prompt.GlossaryEntry,
	usage backend.Usage,
	truncated bool,
	durationMs int64,
	triedBackends []string,
	logger *slog.Logger,
) {
	segIDs := segmentIDStringsFromDoc(doc, pendingIdxs)

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
		SentContent:     usr,
		ReceivedContent: rawRespText,
		UsedGlossary:    usedGlossary,
		AddedGlossary:   res.Glos,
		ErrorType:       errorType,
		ErrorMessage:    errorMsg,
		TriedBackends:   triedBackends,
		Truncated:       truncated,
		Repaired:        res.Repaired,
		RoundIndex:      h.RoundIndex,
		Attempt:         attempt,
		SystemPrompt:    sys,
		UserMessage:     usr,
		ResponseFormat:  responseFormat,
		JSONSchema:      jsonSchema,
		ResponseContent: rawRespText,
	})
}

// headSnippet 截取字符串前 n 个字符。
func headSnippet(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// extractRubyAnnotationsFromDoc 从文档段落中提取注音注释（带段内条目 id）。
// 只读统一结构 seg.Meta["ruby_items"]（[]ruby.Item，带 ID；提取阶段恒写入）。
func extractRubyAnnotationsFromDoc(doc *Document, idxs []int, idMap map[int]string) map[string][]prompt.RubyAnnotation {
	result := make(map[string][]prompt.RubyAnnotation)
	for _, idx := range idxs {
		seg := doc.Segments[idx]
		var converted []prompt.RubyAnnotation
		if raw, ok := seg.Meta["ruby_items"]; ok {
			if items, ok := raw.([]ruby.Item); ok {
				converted = make([]prompt.RubyAnnotation, len(items))
				for i, it := range items {
					converted[i] = prompt.RubyAnnotation{ID: it.ID, Base: it.SourceBase, Text: it.SourceText}
				}
			}
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
