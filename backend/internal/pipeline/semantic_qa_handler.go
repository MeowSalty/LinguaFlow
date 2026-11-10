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
)

// SemanticQAHandler 实现 RoundHandler，对已翻译段落做 LLM 语义质检，产出 warning 级 issue。
// 不改译文、不改段落状态；失败时不产出新 issue（由 batchHandler 跳过写库）。
type SemanticQAHandler struct {
	Backend          backend.Backend
	Renderer         *prompt.SemanticQARenderer
	BatchSize        int
	MaxWordsPerBatch int
	// MaxBatchIndexSpan 同批段落文档索引跨度上限（max-min）；<=0 不限制（默认）。
	MaxBatchIndexSpan int
	Retry             backend.RetryPolicy
	ResponseMode      string
	Repair            repair.Options
	SegmentScope      string   // "all"(默认) | "with_issues" | "with_issue_codes"
	IssueCodes        []string // 仅 with_issue_codes 生效
	Reporter          progress.Reporter
	Logger            *slog.Logger

	// Gate 是任务级暂停闸门（退避重试等待中止信号）；nil 时无暂停语义。
	Gate *PauseGate

	RoundIndex int // execution plan round index, set by caller
}

func (h *SemanticQAHandler) ModeName() string { return RoundModeSemanticQA }

func (h *SemanticQAHandler) Finalize(_ context.Context, _ *Document, _ []int) error {
	return nil
}

func (h *SemanticQAHandler) logger() *slog.Logger {
	if h.Logger == nil {
		return slog.Default()
	}
	return h.Logger
}

func (h *SemanticQAHandler) emitBatchOutcome(evt progress.BatchEvent) {
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

func (h *SemanticQAHandler) segmentScope() string {
	return h.SegmentScope
}

func (h *SemanticQAHandler) issueCodeSet() map[string]struct{} {
	set := make(map[string]struct{}, len(h.IssueCodes))
	for _, c := range h.IssueCodes {
		set[c] = struct{}{}
	}
	return set
}

// segmentInScope 判断段是否落入当前 scope（前置 status∈{translated,edited} 且 Target 非空已由调用方保证）。
func segmentInScope(seg Segment, scope string, codes map[string]struct{}) bool {
	switch scope {
	case "all":
		return true
	case "with_issues":
		return len(seg.Issues) > 0
	case "with_issue_codes":
		for _, iss := range seg.Issues {
			if _, ok := codes[iss.Code]; ok {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// BuildBatches 选 status∈{translated,edited} 且 Target 非空、并落入 segment_scope 的段，按约束分批。
// pending==nil（池 0）时自行扫描 doc（排除跨轮已解决段）；pending!=nil（池>0）时
// 仅对给定 unresolved 重切，避免全量重扫。
func (h *SemanticQAHandler) BuildBatches(ctx context.Context, doc *Document, pending []int, _ int) ([][]int, error) {
	if h.Renderer == nil || h.Backend == nil {
		return nil, nil
	}
	batches, count, err := buildStoredScopeBounded(ctx, doc, pending, h.IssueCodes, func(seg *Segment, codes map[string]struct{}) bool {
		return seg.Translate && (seg.Status == "translated" || seg.Status == "edited") && seg.Target != "" && segmentInScope(*seg, h.segmentScope(), codes)
	}, BatchConstraint{MaxSegments: h.BatchSize, MaxWords: h.MaxWordsPerBatch, WordCount: func(seg Segment) int { return CountWords(seg.Source) + CountWords(seg.Target) }}, h.MaxBatchIndexSpan, false)
	if err != nil {
		return nil, err
	}
	h.logger().Info("semantic_qa handler: batches built", "segments", count, "batches", len(batches), "batch_size", h.BatchSize, "max_words_per_batch", h.MaxWordsPerBatch, "max_batch_index_span", h.MaxBatchIndexSpan)
	return batches, nil
}

// ProcessBatch 渲染语义质检 prompt → 调用 LLM → 解析 issues。
// 失败时不返回 callbackResult（batchHandler 跳过写库，原 issue 保留）。
// 在途重试预算由 executor transientBudget 统一管控（min(max_attempts+1,3)）：
// 瞬时/解析错误预算内重试，耗尽则 unresolved（交下一池重切 + 末轮跨轮传播换 backend），
// 可见性由 job_runner 的 warning_message 通道承担。
// render 失败（确定性配置/模板问题）→ terminalFailure（软警告，不重试）。
// 致命 401/403 → fatalUnresolved（跳池+跨轮传播）。
func (h *SemanticQAHandler) ProcessBatch(ctx context.Context, doc *Document, idxs []int, attempt int, logger *slog.Logger) batchResult {
	emit := func(evt progress.BatchEvent) { h.emitBatchOutcome(storedProgressEvent(ctx, evt)) }
	batchStart := time.Now()
	tried := []string{h.Backend.Name()}

	proto := prompt.ProtocolFromResponseMode(h.ResponseMode)
	isTextMode := proto.IsText()
	requestBase := backend.Request{}
	renderErr := runStoredHandlerBuild(ctx, func(b *backend.BuildBounds) (err error) {
		if isTextMode {
			requestBase.ResponseFormat = "none"
			return nil
		}
		requestBase.JSONSchema, err = prompt.SemanticQAIssueSchemaBounded(b)
		return err
	})
	var req backend.Request
	if renderErr == nil {
		req, renderErr = RenderStoredRequestBounded(ctx, h.Backend, requestBase, func(b *backend.BuildBounds, claim func(backend.PromptMeasure) error) (string, string, error) {
			if e := b.ReserveNodes(int64(len(idxs))+1, 1024); e != nil {
				return "", "", e
			}
			segments := make([]prompt.SemanticQASegment, 0, len(idxs))
			for _, idx := range idxs {
				seg := &doc.Segments[idx]
				segments = append(segments, prompt.SemanticQASegment{ID: seg.ID, Source: seg.Source, Target: seg.Target})
			}
			return h.Renderer.RenderWithCreditsBounded(b, prompt.SemanticQAData{SourceLang: doc.SourceLang, TargetLang: doc.TargetLang, Segments: segments, Protocol: proto}, storedHandlerInputLimit(ctx), claim)
		})
	}
	sys, usr := req.System, req.User
	if renderErr != nil {
		if ctx.Err() != nil {
			return h.preserveResultBounded(ctx, doc, idxs)
		}
		if d := workDeferred(renderErr); d != nil {
			return batchResult{waitCapacity: d, deferred: true}
		}
		if storageFailure(renderErr) {
			return batchResult{err: renderErr}
		}
		logger.Error("semantic_qa render failed", "err", renderErr)
		emit(progress.BatchEvent{
			Stage:         RoundModeSemanticQA,
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
		return h.terminalFailureBounded(ctx, doc, idxs)
	}

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
		// 外部中断：Canceled，或 DeadlineExceeded 且父 ctx 已死（job 取消/外层 deadline）。
		// 本地 backend timeout 产出 DeadlineExceeded 但父 ctx 仍活，不走此分支。
		isExternalInterrupt := errors.Is(callErr, context.Canceled) ||
			(errors.Is(callErr, context.DeadlineExceeded) && ctx.Err() != nil)
		if isExternalInterrupt {
			logger.Info("semantic_qa backend interrupted by context",
				"backend", h.Backend.Name(), "batch_size", len(idxs), "err", callErr)
			emit(backendErrorBatchEvent(RoundModeSemanticQA, doc, idxs, h.Backend.Name(), tried, callErr, attempt, h.RoundIndex, time.Since(callStart).Milliseconds(), sys, usr, req))
			return h.preserveResultBounded(ctx, doc, idxs)
		}

		// 本地 backend timeout：父 ctx 仍活，按可重试 backend 错误处理。
		// IsRetryable 对 DeadlineExceeded 恒返 false，须在此主动闸为可重试。
		isLocalTimeout := errors.Is(callErr, context.DeadlineExceeded) && ctx.Err() == nil

		// 致命 401/403：跳池（同 backend 重试无意义）+ 跨轮传播换 backend。
		// 不计入扫描失败软警告（真实配置/权限问题交给下一轮换 backend 接力）。
		if isFatalBackendError(callErr) {
			logger.Warn("semantic_qa backend fatal error, deferring to cross-round",
				"backend", h.Backend.Name(), "batch_size", len(idxs),
				"attempt", attempt, "err", callErr)
			emit(backendErrorBatchEvent(RoundModeSemanticQA, doc, idxs, h.Backend.Name(), tried, callErr, attempt, h.RoundIndex, time.Since(callStart).Milliseconds(), sys, usr, req))
			return batchResult{fatalUnresolved: idxs}
		}

		emit(backendErrorBatchEvent(RoundModeSemanticQA, doc, idxs, h.Backend.Name(), tried, callErr, attempt, h.RoundIndex, time.Since(callStart).Milliseconds(), sys, usr, req))

		// 本地超时 / 5xx / 429 / 裸网络错误可退避重试；外部中断不计入。
		// 401/403 已在上文 fatalUnresolved 分支提前返回。
		// 预算内重试；耗尽则 unresolved（交下一池重切，末轮跨轮传播换 backend）。
		// 预算与 executor transientBudget 同源（transientBudgetFor）。
		if (isLocalTimeout || backend.IsRetryable(callErr)) && attempt+1 < transientBudgetFor(h.Retry) {
			if isLocalTimeout {
				logger.Warn("semantic_qa backend local timeout, will backoff and retry",
					"backend", h.Backend.Name(), "batch_size", len(idxs),
					"attempt", attempt, "err", callErr)
			} else {
				logger.Warn("semantic_qa backend retryable error, will backoff and retry",
					"backend", h.Backend.Name(), "batch_size", len(idxs),
					"attempt", attempt, "err", callErr)
			}
			wait := backoffDuration(attempt, h.Retry, callErr)
			if retry := storedBackoffRetry(ctx, idxs, attempt, wait); retry != nil {
				return batchResult{retry: retry}
			}
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				// 退避期间取消：段保持未解决（不 preserve——preserve 会把
				// 未完成 QA 段计入 resolved 断点，retry 后被永久跳过）。
				return batchResult{unresolved: idxs}
			case <-h.Gate.Done():
				// 暂停时中止退避等待：段保持未解决，由断点集合覆盖。
				timer.Stop()
				return batchResult{unresolved: idxs}
			case <-timer.C:
			}
			return batchResult{retry: &batchJob{idxs: idxs, attempt: attempt + 1}}
		}

		if isLocalTimeout {
			logger.Warn("semantic_qa backend local timeout exhausted, deferring to next pool",
				"backend", h.Backend.Name(), "batch_size", len(idxs),
				"attempt", attempt, "err", callErr)
		} else {
			logger.Warn("semantic_qa backend failed terminally, deferring to next pool",
				"backend", h.Backend.Name(), "batch_size", len(idxs),
				"attempt", attempt, "err", callErr)
		}
		// 预算耗尽：段交下一池重切（更小批次可能因上下文缩减而成功），
		// 末轮后仍未解决则跨轮传播（可换 backend 接力）。可见性由 job_runner
		// 的 warning_message 通道承担（与原 terminalFailure 软警告等价，不降级）。
		// 不计数（段尚未解决，executor 在终态批次时计数），不返回 callbackResult（避免写回空 issue）。
		return batchResult{unresolved: idxs}
	}

	if resp.Truncated {
		logTruncatedResponse(logger, h.Backend.Name())
	}
	atomic.AddInt64(&doc.InputTokens, resp.Usage.PromptTokens)
	atomic.AddInt64(&doc.OutputTokens, resp.Usage.CompletionTokens)

	var parseRepaired []string
	var callback *BatchResult
	var producedTotal int
	parseErr := runStoredHandlerBuild(ctx, func(b *backend.BuildBounds) error {
		if int64(len(resp.Text)) > b.OutputLimit() {
			return backend.ErrBuildCapacity
		}
		opt := h.Repair
		opt.Bounds = b
		issues, repaired, e := repair.ParseSemanticQAByMode(resp.Text, isTextMode, opt)
		parseRepaired = repaired
		if e != nil {
			return e
		}
		if resp.Truncated {
			return errors.New("response truncated by output token limit: refusing partial issues as complete")
		}
		callback, producedTotal, e = h.buildSemanticResultBounded(b, doc, idxs, issues)
		issues = nil
		if e != nil {
			return e
		}
		if _, stored := ctx.Value(requestSessionKey{}).(*requestSession); stored {
			return SealStoredBatchResult(b, callback, b.OutputLimit())
		}
		for _, result := range callback.Segments {
			for _, issue := range result.Issues {
				doc.Segments[result.Index].Issues, e = backend.AppendNode(b, doc.Segments[result.Index].Issues, issue)
				if e != nil {
					return e
				}
			}
		}
		return nil
	})
	// 截断响应对 fail-closed stage 恒不采纳：issues 的 partial 会被下游解释为
	// 「缺失段=已扫描无问题」（假阴性质检）。JSON 模式下 WithoutSalvage 已拒绝
	// 可检测的截断形态，此处封住两个残余通道——text 协议逐行解析无完整性信号
	// （截断的已完成行被当作完整结果）、以及截断点恰在完整边界导致解析成功；
	// 截断即报错走重试/下一池，代价由池预算约束。
	if parseErr == nil && resp.Truncated {
		parseErr = fmt.Errorf("response truncated by output token limit: refusing partial issues as complete")
	}
	if parseErr != nil {
		if ctx.Err() != nil {
			return h.preserveResultBounded(ctx, doc, idxs)
		}
		logger.Warn("semantic_qa parse failed",
			"backend", h.Backend.Name(), "batch_size", len(idxs), "err", parseErr,
			"attempt", attempt, "resp_len", len(resp.Text), "resp_head", headSnippet(resp.Text, 200))
		emit(progress.BatchEvent{
			Stage:           RoundModeSemanticQA,
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
		// parse 失败：预算内立即再入队（无退避）；耗尽则交下一池重切。
		// LLM 响应畸形可能因随机性恢复，故预算内走重试通道（与路由表"解析失败→retry"一致）。
		// 预算耗尽后落 unresolved：换更小批次重切可能成功，末轮仍未解决则跨轮传播换 backend；
		// 可见性由 job_runner 的 warning_message 通道承担，不依赖 failedSegments 软警告。
		if errors.Is(parseErr, backend.ErrBuildCapacity) {
			return batchResult{failedSegments: idxs}
		}
		if attempt+1 < transientBudgetFor(h.Retry) {
			return batchResult{retry: &batchJob{idxs: idxs, attempt: attempt + 1}}
		}
		// 不计数（段尚未解决，executor 在终态批次时计数），不返回 callbackResult（避免写回空 issue）。
		return batchResult{unresolved: idxs}
	}

	// id → []issue（snippet → Span；定位失败仍保留 MatchedText）
	if len(parseRepaired) > 0 {
		logger.Info("semantic_qa response repaired",
			"backend", h.Backend.Name(), "ops", parseRepaired)
	}

	logger.Info("semantic_qa batch ok",
		"backend", h.Backend.Name(),
		"segments", len(idxs),
		"issues", producedTotal,
		"prompt_tokens", resp.Usage.PromptTokens,
		"completion_tokens", resp.Usage.CompletionTokens)

	emit(progress.BatchEvent{
		Stage:           RoundModeSemanticQA,
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

func (h *SemanticQAHandler) buildSemanticResultBounded(b *backend.BuildBounds, doc *Document, idxs []int, issues []prompt.SemanticQAIssue) (*BatchResult, int, error) {
	if e := b.ReserveNodes(int64(2*len(idxs)+len(issues)+3), 1024); e != nil {
		return nil, 0, e
	}
	byID := make(map[string][]qa.QualityIssue, len(idxs))
	segByID := make(map[string]*Segment, len(idxs))
	for _, idx := range idxs {
		segByID[doc.Segments[idx].ID] = &doc.Segments[idx]
	}
	for _, issue := range issues {
		if e := b.ReserveNodes(10, 1024); e != nil {
			return nil, 0, e
		}
		var span *qa.Span
		if issue.Snippet != "" {
			if seg, ok := segByID[issue.ID]; ok {
				span = qa.LocateSpan(seg.Target, issue.Snippet)
				if span == nil || span.TargetStart == nil {
					if sourceSpan := qa.LocateSpan(seg.Source, issue.Snippet); sourceSpan != nil {
						span = &qa.Span{MatchedText: sourceSpan.MatchedText}
					} else if span == nil {
						span = &qa.Span{MatchedText: issue.Snippet}
					}
				}
			} else {
				span = &qa.Span{MatchedText: issue.Snippet}
			}
		}
		var e error
		byID[issue.ID], e = backend.AppendNode(b, byID[issue.ID], qa.QualityIssue{Code: issue.Code, Message: issue.Message, Severity: qa.SeverityWarning, Span: span, Disposition: qa.DispositionPending})
		if e != nil {
			return nil, 0, e
		}
	}
	if e := b.ReserveNodes(int64(len(idxs))+2, 1024); e != nil {
		return nil, 0, e
	}
	result := &BatchResult{Segments: make([]TranslatedSegment, 0, len(idxs))}
	total := 0
	for _, idx := range idxs {
		seg := &doc.Segments[idx]
		items := byID[seg.ID]
		if e := b.ReserveNodes(int64(2*len(items)+2), 1024); e != nil {
			return nil, 0, e
		}
		seen := make(map[string]struct{}, len(items))
		newIssues := make([]qa.QualityIssue, 0, len(items))
		for _, issue := range items {
			keyBytes := int64(len(issue.Code)) + 1 + int64(len(qa.MatchedText(issue)))
			if e := b.ReserveText(keyBytes, keyBytes); e != nil {
				return nil, 0, e
			}
			key := issue.Code + ":" + qa.MatchedText(issue)
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			issue.SegmentIndex = idx
			newIssues = append(newIssues, issue)
		}
		total += len(newIssues)
		result.Segments = append(result.Segments, TranslatedSegment{Index: idx, ID: seg.ID, SourceText: seg.Source, TargetText: seg.Target, Issues: newIssues})
	}
	return result, total, nil
}

// preserveResult 不产出新 issue（Issues 为 nil），batchHandler 跳过写库，原 issue 保留。
// 不设置 failedSegments（用于 ctx 取消等外部中断，不计入扫描失败）。
// 注意：ctx 取消时本 result 经 cleanup 路径被丢弃，不进入计数/断点登记。
func (h *SemanticQAHandler) preserveResult(doc *Document, idxs []int) batchResult {
	callbackSegs := make([]TranslatedSegment, 0, len(idxs))
	for _, idx := range idxs {
		seg := &doc.Segments[idx]
		callbackSegs = append(callbackSegs, TranslatedSegment{
			Index:      idx,
			ID:         seg.ID,
			SourceText: seg.Source,
			TargetText: seg.Target,
			Issues:     nil,
		})
	}
	return batchResult{
		callbackResult: &BatchResult{Segments: callbackSegs},
	}
}

func (h *SemanticQAHandler) preserveResultBounded(ctx context.Context, doc *Document, idxs []int) batchResult {
	var result *BatchResult
	err := runStoredHandlerBuild(context.WithoutCancel(ctx), func(b *backend.BuildBounds) error {
		if e := b.ReserveNodes(int64(len(idxs))+2, 1024); e != nil {
			return e
		}
		result = &BatchResult{Segments: make([]TranslatedSegment, 0, len(idxs))}
		for _, idx := range idxs {
			seg := &doc.Segments[idx]
			result.Segments = append(result.Segments, TranslatedSegment{Index: idx, ID: seg.ID, SourceText: seg.Source, TargetText: seg.Target})
		}
		if _, stored := ctx.Value(requestSessionKey{}).(*requestSession); stored {
			return SealStoredBatchResult(b, result, b.OutputLimit())
		}
		return nil
	})
	if err != nil {
		return batchResult{unresolved: idxs}
	}
	return batchResult{callbackResult: result}
}

func (h *SemanticQAHandler) terminalFailureBounded(ctx context.Context, doc *Document, idxs []int) batchResult {
	result := h.preserveResultBounded(ctx, doc, idxs)
	result.failedSegments = idxs
	return result
}

// terminalFailure 在 preserveResult 基础上标记 failedSegments，供 RunRound 累计为软警告。
// failedSegments 段不计数（executor 将其从 resolved 子集排除——未解决，软警告承担可见性）。
// 依赖 h.Retry 与 round.Retry 同源，故不再返回 retry。
func (h *SemanticQAHandler) terminalFailure(doc *Document, idxs []int) batchResult {
	pr := h.preserveResult(doc, idxs)
	pr.failedSegments = idxs
	return pr
}
