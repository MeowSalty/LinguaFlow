package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

// AdjudicateHandler 实现 RoundHandler，对已标出问题的段落做 AI 裁决，剔除误报。
// 不改译文、不改段落状态；失败按错误路由推进（与 translate/extract 对齐），
// 耗尽后原 issue 保持 pending 原样保留（既未确认也未剔除）。
type AdjudicateHandler struct {
	Backend          backend.Backend
	Renderer         *prompt.AdjudicationRenderer
	BatchSize        int
	MaxWordsPerBatch int
	// MaxBatchIndexSpan 同批段落文档索引跨度上限（max-min）；<=0 不限制（默认）。
	// 预埋：后期可从执行计划透传以限制同批话题跨度。
	MaxBatchIndexSpan int
	Retry             backend.RetryPolicy
	ResponseMode      string
	Repair            repair.Options
	AdjudicateCodes   []string
	Reporter          progress.Reporter
	Logger            *slog.Logger

	// Gate 是任务级暂停闸门（退避重试等待中止信号）；nil 时无暂停语义。
	Gate *PauseGate

	RoundIndex int // execution plan round index, set by caller
}

func (h *AdjudicateHandler) ModeName() string { return RoundModeAdjudicate }

func (h *AdjudicateHandler) Finalize(_ context.Context, _ *Document, _ []int) error {
	return nil
}

func (h *AdjudicateHandler) logger() *slog.Logger {
	if h.Logger == nil {
		return slog.Default()
	}
	return h.Logger
}

func (h *AdjudicateHandler) emitBatchOutcome(evt progress.BatchEvent) {
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

func (h *AdjudicateHandler) adjudicateCodes() []string {
	return h.AdjudicateCodes
}

func adjudicateCodeSet(codes []string) map[string]struct{} {
	set := make(map[string]struct{}, len(codes))
	for _, c := range codes {
		set[c] = struct{}{}
	}
	return set
}

func segmentHasAdjudicableIssue(issues []qa.QualityIssue, codes map[string]struct{}) bool {
	for _, iss := range issues {
		if _, ok := codes[iss.Code]; !ok {
			continue
		}
		if iss.Dismissed() {
			continue // 已裁决为非问题，不再送 LLM
		}
		return true
	}
	return false
}

// BuildBatches 选 status∈{translated,edited} 且 Issues 含可裁决 code 的段，按约束分批。
// pending==nil（池 0）时自行扫描 doc（排除跨轮已解决段）；pending!=nil（池>0）时
// 仅对给定 unresolved 重切，避免全量重扫。
func (h *AdjudicateHandler) BuildBatches(ctx context.Context, doc *Document, pending []int, _ int) ([][]int, error) {
	if h.Renderer == nil || h.Backend == nil {
		return nil, nil
	}
	batches, count, err := buildStoredScopeBounded(ctx, doc, pending, h.adjudicateCodes(), func(seg *Segment, codes map[string]struct{}) bool {
		return (seg.Status == "translated" || seg.Status == "edited") && segmentHasAdjudicableIssue(seg.Issues, codes)
	}, BatchConstraint{MaxSegments: h.BatchSize, MaxWords: h.MaxWordsPerBatch}, h.MaxBatchIndexSpan, false)
	if err != nil {
		return nil, err
	}
	h.logger().Info("adjudicate handler: batches built", "segments", count, "batches", len(batches), "batch_size", h.BatchSize, "max_words_per_batch", h.MaxWordsPerBatch, "max_batch_index_span", h.MaxBatchIndexSpan)
	return batches, nil
}

// ProcessBatch 渲染裁决 prompt → 调用 LLM → 解析 verdict → 剔除 false_positive。
// 错误路由（与 translate/extract 语义对齐）：
//   - 渲染失败 → unresolved（进下一池重切）
//   - 致命 401/403 → fatalUnresolved（跳池 + 跨轮传播换 backend）
//   - 可重试 429/5xx/网络 → retry（同批退避重试，预算耗尽落下一池）
//   - 退避期间外部取消 → unresolved（随轮次取消并入 finalUnresolved）
//   - 非致命不可重试 backend 错误 → unresolved（进下一池重试）
//   - 解析失败 → unresolved（进下一池重切）
//   - 成功 → callbackResult（false_positive 打 dismissed 标记保留）
//
// unresolved 段不回 callback（避免写回陈旧 issue 干扰下一池重判）；
// 末池耗尽后原 issue 保持 pending 原样保留。
// 段计数/断点由 executor 在批次终态时统一登记（handler 不触碰进度计数）。
func (h *AdjudicateHandler) ProcessBatch(ctx context.Context, doc *Document, idxs []int, attempt int, logger *slog.Logger) batchResult {
	emit := func(evt progress.BatchEvent) { h.emitBatchOutcome(storedProgressEvent(ctx, evt)) }
	batchStart := time.Now()
	var codes map[string]struct{}
	tried := []string{h.Backend.Name()}

	// 构建裁决输入（仅可裁决 issue 子集；含 matched_text 以区分同 code 多实例）
	proto := prompt.ProtocolFromResponseMode(h.ResponseMode)
	isTextMode := proto.IsText()
	requestBase := backend.Request{}
	renderErr := runStoredHandlerBuild(ctx, func(b *backend.BuildBounds) (err error) {
		if err = b.ReserveNodes(int64(len(h.AdjudicateCodes))+1, 1024); err != nil {
			return err
		}
		codes = adjudicateCodeSet(h.adjudicateCodes())
		if isTextMode {
			requestBase.ResponseFormat = "none"
			return nil
		}
		requestBase.JSONSchema, err = prompt.AdjudicationVerdictSchemaBounded(b)
		return err
	})
	var req backend.Request
	if renderErr == nil {
		req, renderErr = RenderStoredRequestBounded(ctx, h.Backend, requestBase, func(b *backend.BuildBounds, claim func(backend.PromptMeasure) error) (string, string, error) {
			if e := b.ReserveNodes(int64(len(idxs))+1, 1024); e != nil {
				return "", "", e
			}
			segments := make([]prompt.AdjudicationSegment, 0, len(idxs))
			for _, idx := range idxs {
				seg := &doc.Segments[idx]
				var issues []prompt.AdjudicationIssue
				for _, iss := range seg.Issues {
					if _, ok := codes[iss.Code]; !ok || iss.Dismissed() {
						continue
					}
					var e error
					issues, e = backend.AppendNode(b, issues, prompt.AdjudicationIssue{Code: iss.Code, Message: iss.Message, MatchedText: qa.MatchedText(iss)})
					if e != nil {
						return "", "", e
					}
				}
				segments = append(segments, prompt.AdjudicationSegment{ID: seg.ID, Source: seg.Source, Target: seg.Target, Issues: issues})
			}
			return h.Renderer.RenderWithCreditsBounded(b, prompt.AdjudicationData{SourceLang: doc.SourceLang, TargetLang: doc.TargetLang, Segments: segments, Protocol: proto}, storedHandlerInputLimit(ctx), claim)
		})
	}
	sys, usr := req.System, req.User
	if renderErr != nil {
		if d := workDeferred(renderErr); d != nil {
			return batchResult{waitCapacity: d, deferred: true}
		}
		if storageFailure(renderErr) {
			return batchResult{err: renderErr}
		}
		logger.Error("adjudicate render failed", "err", renderErr)
		emit(progress.BatchEvent{
			Stage:         RoundModeAdjudicate,
			SegmentIDs:    segmentIDStringsFromDoc(doc, idxs),
			SegmentCount:  len(idxs),
			BackendName:   h.Backend.Name(),
			Status:        "failed",
			DurationMs:    time.Since(batchStart).Milliseconds(),
			TriedBackends: tried,
			ErrorType:     "render_error",
			ErrorMessage:  renderErr.Error(),
			RoundIndex:    h.RoundIndex,
		})
		return batchResult{unresolved: idxs}
	}

	// 非 text：只挂 JSONSchema，不强制 ResponseFormat，由 backend 默认决定是否用 schema。
	// text：强制 ResponseFormat=none，不挂 schema。
	callStart := time.Now()
	resp, callErr := h.Backend.Translate(ctx, req)
	releaseStoredRequestText(ctx, &req, &sys, &usr)
	if storageFailure(callErr) {
		return batchResult{err: callErr}
	}
	if d := workDeferred(callErr); d != nil {
		return batchResult{waitCapacity: d, deferred: true}
	}
	if callErr != nil {
		if isFatalBackendError(callErr) {
			logger.Error("adjudicate backend fatal error, deferring to cross-round",
				"backend", h.Backend.Name(), "batch_size", len(idxs), "err", callErr)
			emit(backendErrorBatchEvent(RoundModeAdjudicate, doc, idxs, h.Backend.Name(), tried, callErr, attempt, h.RoundIndex, time.Since(callStart).Milliseconds(), sys, usr, req))
			// 401/403：跳池（同 backend 重试无意义）+ 跨轮传播换 backend。
			return batchResult{fatalUnresolved: idxs}
		}
		// 可重试错误扩大为全 backend.IsRetryable（含 5xx、429/503 等）。
		if backend.IsRetryable(callErr) {
			logger.Warn("adjudicate rate limit, will backoff and retry",
				"backend", h.Backend.Name(), "batch_size", len(idxs), "err", callErr)
			emit(backendErrorBatchEvent(RoundModeAdjudicate, doc, idxs, h.Backend.Name(), tried, callErr, attempt, h.RoundIndex, time.Since(callStart).Milliseconds(), sys, usr, req))
			wait := backoffDuration(attempt, h.Retry, callErr)
			if retry := storedBackoffRetry(ctx, idxs, attempt, wait); retry != nil {
				return batchResult{retry: retry}
			}
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return batchResult{unresolved: idxs}
			case <-h.Gate.Done():
				// 暂停时中止退避等待：段保持未解决，由断点集合覆盖。
				timer.Stop()
				return batchResult{unresolved: idxs}
			case <-timer.C:
			}
			return batchResult{retry: &batchJob{idxs: idxs, attempt: attempt + 1}}
		}
		logger.Warn("adjudicate backend failed, deferring to next pool",
			"backend", h.Backend.Name(), "batch_size", len(idxs), "err", callErr)
		emit(backendErrorBatchEvent(RoundModeAdjudicate, doc, idxs, h.Backend.Name(), tried, callErr, attempt, h.RoundIndex, time.Since(callStart).Milliseconds(), sys, usr, req))
		return batchResult{unresolved: idxs}
	}

	if resp.Truncated {
		logTruncatedResponse(logger, h.Backend.Name())
	}
	atomic.AddInt64(&doc.InputTokens, resp.Usage.PromptTokens)
	atomic.AddInt64(&doc.OutputTokens, resp.Usage.CompletionTokens)

	var parseRepaired []string
	var callback *BatchResult
	var dismissedTotal int
	parseErr := runStoredHandlerBuild(ctx, func(b *backend.BuildBounds) error {
		if int64(len(resp.Text)) > b.OutputLimit() {
			return backend.ErrBuildCapacity
		}
		opt := h.Repair
		opt.Bounds = b
		verdicts, repaired, e := repair.ParseAdjudicationByMode(resp.Text, isTextMode, opt)
		parseRepaired = repaired
		if e != nil {
			return e
		}
		if resp.Truncated {
			return errors.New("response truncated by output token limit: refusing partial verdicts as complete")
		}
		callback, dismissedTotal, e = h.buildAdjudicationResultBounded(b, doc, idxs, codes, verdicts)
		if e != nil {
			return e
		}
		if _, stored := ctx.Value(requestSessionKey{}).(*requestSession); stored {
			return SealStoredBatchResult(b, callback, b.OutputLimit())
		}
		for _, result := range callback.Segments {
			doc.Segments[result.Index].Issues = result.Issues
		}
		return nil
	})
	// 截断响应对 fail-closed stage 恒不采纳：adjudicate 的成功路径对批次全段生效
	//（executor 按「idxs−失败段」计数，无「缺失 verdict → 重跑」通道），partial 会被
	// 计为终态已裁决。JSON 模式下 WithoutSalvage 已拒绝可检测的截断形态，此处封住两个残余通道——
	// text 协议逐行解析无完整性信号（截断的已完成行被当作完整结果）、以及截断点
	// 恰在完整边界导致解析成功；截断即报错走 unresolved → 下一池整批重试。
	if parseErr == nil && resp.Truncated {
		parseErr = fmt.Errorf("response truncated by output token limit: refusing partial verdicts as complete")
	}
	if parseErr != nil {
		logger.Warn("adjudicate parse failed, deferring to next pool",
			"backend", h.Backend.Name(), "batch_size", len(idxs), "err", parseErr,
			"resp_len", len(resp.Text), "resp_head", headSnippet(resp.Text, 200))
		emit(progress.BatchEvent{
			Stage:           RoundModeAdjudicate,
			SegmentIDs:      segmentIDStringsFromDoc(doc, idxs),
			SegmentCount:    len(idxs),
			BackendName:     h.Backend.Name(),
			Status:          "failed",
			DurationMs:      time.Since(callStart).Milliseconds(),
			InputTokens:     resp.Usage.PromptTokens,
			OutputTokens:    resp.Usage.CompletionTokens,
			SentContent:     usr,
			ReceivedContent: resp.Text,
			TriedBackends:   tried,
			ErrorType:       "parse_error",
			ErrorMessage:    parseErr.Error(),
			Truncated:       resp.Truncated,
			Repaired:        parseRepaired,
			RoundIndex:      h.RoundIndex,
			Attempt:         attempt,
			SystemPrompt:    sys,
			UserMessage:     usr,
			ResponseFormat:  req.ResponseFormat,
			JSONSchema:      req.JSONSchema,
			ResponseContent: resp.Text,
		})
		// 解析失败：段交下一池重切（与 translate/extract 语义对齐，复用 BuildBatches
		// 的 pending 重切通道）。不计数（段尚未解决，executor 在终态批次时计数），
		// 不返回 callbackResult（避免 batchHandler 写回陈旧 issue 干扰下一池重判）。
		if errors.Is(parseErr, backend.ErrBuildCapacity) {
			return batchResult{failedSegments: idxs}
		}
		return batchResult{unresolved: idxs}
	}

	// (id, issue_code, matched_text) → verdict（携带 reason 供审计沉淀）
	logger.Info("adjudicate batch ok",
		"backend", h.Backend.Name(),
		"segments", len(idxs),
		"dismissed", dismissedTotal,
		"prompt_tokens", resp.Usage.PromptTokens,
		"completion_tokens", resp.Usage.CompletionTokens)

	emit(progress.BatchEvent{
		Stage:           RoundModeAdjudicate,
		SegmentIDs:      segmentIDStringsFromDoc(doc, idxs),
		SegmentCount:    len(idxs),
		BackendName:     h.Backend.Name(),
		Status:          "success",
		DurationMs:      time.Since(batchStart).Milliseconds(),
		InputTokens:     resp.Usage.PromptTokens,
		OutputTokens:    resp.Usage.CompletionTokens,
		SentContent:     usr,
		ReceivedContent: resp.Text,
		TriedBackends:   tried,
		Truncated:       resp.Truncated,
		Repaired:        parseRepaired,
		RoundIndex:      h.RoundIndex,
		Attempt:         attempt,
		SystemPrompt:    sys,
		UserMessage:     usr,
		ResponseFormat:  req.ResponseFormat,
		JSONSchema:      req.JSONSchema,
		ResponseContent: resp.Text,
	})

	return batchResult{
		callbackResult: callback,
	}
}

func (h *AdjudicateHandler) buildAdjudicationResultBounded(b *backend.BuildBounds, doc *Document, idxs []int, codes map[string]struct{}, verdicts []prompt.AdjudicationVerdict) (*BatchResult, int, error) {
	if e := b.ReserveNodes(int64(len(verdicts)+len(idxs)+3), 1024); e != nil {
		return nil, 0, e
	}
	verdictMap := make(map[string]prompt.AdjudicationVerdict, len(verdicts))
	for _, verdict := range verdicts {
		size := int64(len(verdict.ID)) + int64(len(verdict.IssueCode)) + int64(len(verdict.MatchedText)) + 2
		if e := b.ReserveText(size, size); e != nil {
			return nil, 0, e
		}
		verdictMap[adjudicationKey(verdict.ID, verdict.IssueCode, verdict.MatchedText)] = verdict
	}
	result := &BatchResult{Segments: make([]TranslatedSegment, 0, len(idxs))}
	total := 0
	for _, idx := range idxs {
		seg := &doc.Segments[idx]
		filtered, count, e := applyVerdictsBounded(b, seg.Issues, seg.ID, codes, verdictMap)
		if e != nil {
			return nil, 0, e
		}
		total += count
		result.Segments = append(result.Segments, TranslatedSegment{Index: idx, ID: seg.ID, SourceText: seg.Source, TargetText: seg.Target, Issues: filtered})
	}
	return result, total, nil
}

func applyVerdictsBounded(b *backend.BuildBounds, issues []qa.QualityIssue, segID string, codes map[string]struct{}, verdicts map[string]prompt.AdjudicationVerdict) ([]qa.QualityIssue, int, error) {
	if len(issues) == 0 {
		return nil, 0, nil
	}
	if e := b.ReserveNodes(int64(len(codes)+len(issues)+3), 1024); e != nil {
		return nil, 0, e
	}
	now := timeutil.NowUTC()
	counts := make(map[string]int, len(codes))
	for _, issue := range issues {
		if _, ok := codes[issue.Code]; ok && !issue.Dismissed() {
			counts[issue.Code]++
		}
	}
	out := make([]qa.QualityIssue, 0, len(issues))
	dismissed := 0
	for _, issue := range issues {
		if issue.Dismissed() {
			out = append(out, issue)
			continue
		}
		if issue.Disposition == "" {
			issue.Disposition = qa.DispositionPending
		}
		if _, ok := codes[issue.Code]; !ok {
			out = append(out, issue)
			continue
		}
		matched := qa.MatchedText(issue)
		size := int64(len(segID)) + int64(len(issue.Code)) + int64(len(matched)) + 2
		if e := b.ReserveText(size, size); e != nil {
			return nil, 0, e
		}
		verdict, found := verdicts[adjudicationKey(segID, issue.Code, matched)]
		if !found && matched != "" && counts[issue.Code] == 1 {
			size = int64(len(segID)) + int64(len(issue.Code)) + 2
			if e := b.ReserveText(size, size); e != nil {
				return nil, 0, e
			}
			verdict, found = verdicts[adjudicationKey(segID, issue.Code, "")]
		}
		if found && verdict.Verdict == "false_positive" {
			issue.Disposition = qa.DispositionDismissed
			issue.DecidedAt = &now
			issue.DecidedBy = nil
			issue.Note = verdict.Reason
			dismissed++
		}
		out = append(out, issue)
	}
	return out, dismissed, nil
}

// adjudicationKey 构建 (segment_id, code, matched_text) 裁决键。
func adjudicationKey(segID, code, matchedText string) string {
	return segID + "\x00" + code + "\x00" + matchedText
}

// applyVerdicts 应用 LLM 裁决：false_positive 的 issue 打 dismissed 标记并保留在
// 数组中（沉淀审计痕迹，避免重跑重复付费），real/缺失/其他保持 pending 原样保留。
// 已 dismissed 的 issue 原样带过，不被重复处理。
// 优先按 (id, code, matched_text) 匹配；若该段该 code 仅有一条待决 issue，则允许回退到空 matched_text 裁决。
func applyVerdicts(
	issues []qa.QualityIssue,
	segID string,
	codes map[string]struct{},
	verdictMap map[string]prompt.AdjudicationVerdict,
	logger *slog.Logger,
) (out []qa.QualityIssue, newlyDismissed int) {
	if len(issues) == 0 {
		return nil, 0
	}
	now := timeutil.NowUTC()
	issueCounts := make(map[string]int, len(codes))
	for _, iss := range issues {
		if _, adjudicable := codes[iss.Code]; adjudicable && !iss.Dismissed() {
			issueCounts[iss.Code]++
		}
	}
	out = make([]qa.QualityIssue, 0, len(issues))
	for _, iss := range issues {
		if iss.Dismissed() {
			// 已裁决的非问题：原样带过，不重复送 LLM/不重复计数。
			out = append(out, iss)
			continue
		}
		// pending issue 的 Disposition 可能为 Go 零值 ""（checker 构造时省略），
		// 归一化为显式 pending，保证内存与落库值一致。
		if iss.Disposition == "" {
			iss.Disposition = qa.DispositionPending
		}
		if _, adjudicable := codes[iss.Code]; !adjudicable {
			out = append(out, iss)
			continue
		}
		mt := qa.MatchedText(iss)
		key := adjudicationKey(segID, iss.Code, mt)
		v, ok := verdictMap[key]
		if !ok && mt != "" && issueCounts[iss.Code] == 1 {
			// 单实例兼容：LLM 未回传 matched_text 时，用空 matched_text 键。
			v, ok = verdictMap[adjudicationKey(segID, iss.Code, "")]
		}
		if !ok || v.Verdict != "false_positive" {
			// 缺失 / real / 其他 → 保持 pending，原样保留
			out = append(out, iss)
			continue
		}
		iss.Disposition = qa.DispositionDismissed
		iss.DecidedAt = &now
		iss.DecidedBy = nil // nil 表示 LLM 裁决
		iss.Note = v.Reason // LLM 理由沉淀为审计
		out = append(out, iss)
		newlyDismissed++
		logger.Info("adjudicate dismissed false_positive",
			"segment_id", segID, "code", iss.Code, "matched_text", mt, "message", iss.Message, "reason", v.Reason)
	}
	return out, newlyDismissed
}
