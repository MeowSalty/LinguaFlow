package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
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
	SegmentScope      string   // "all"(默认) | "with_issues" | "with_issue_codes"
	IssueCodes        []string // 仅 with_issue_codes 生效
	Reporter          progress.Reporter
	Logger            *slog.Logger
	RoundIndex        int // execution plan round index, set by caller
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

func (h *SemanticQAHandler) reporter() progress.Reporter {
	if h.Reporter == nil {
		return progress.Nop{}
	}
	return h.Reporter
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
	if h.SegmentScope == "" {
		return "all"
	}
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
		return true // 未知 scope 兜底为 all
	}
}

// BuildBatches 选 status∈{translated,edited} 且 Target 非空、并落入 segment_scope 的段，按约束分批。
func (h *SemanticQAHandler) BuildBatches(_ context.Context, doc *Document) ([][]int, error) {
	logger := h.logger()
	if h.Renderer == nil {
		logger.Warn("semantic_qa handler: renderer is nil, skipping")
		return nil, nil
	}
	if h.Backend == nil {
		logger.Warn("semantic_qa handler: backend is nil, skipping")
		return nil, nil
	}

	codes := h.issueCodeSet()
	scope := h.segmentScope()
	var pending []int
	for i := range doc.Segments {
		seg := &doc.Segments[i]
		if !seg.Translate {
			continue
		}
		if seg.Status != "translated" && seg.Status != "edited" {
			continue
		}
		if seg.Target == "" {
			continue
		}
		if !segmentInScope(*seg, scope, codes) {
			continue
		}
		pending = append(pending, i)
	}

	if len(pending) == 0 {
		logger.Info("semantic_qa handler: no translated segments")
		return nil, nil
	}

	constraint := BatchConstraint{
		MaxSegments: h.BatchSize,
		MaxWords:    h.MaxWordsPerBatch,
		WordCount: func(seg Segment) int {
			return CountWords(seg.Source) + CountWords(seg.Target)
		},
	}
	if constraint.MaxSegments <= 0 && constraint.MaxWords <= 0 && h.MaxBatchIndexSpan <= 0 {
		logger.Info("semantic_qa handler: no batch limit, sending all segments at once",
			"segments", len(pending))
		return [][]int{pending}, nil
	}

	batches := BuildPackedPendingBatches(doc, pending, constraint, h.MaxBatchIndexSpan)
	logger.Info("semantic_qa handler: batches built",
		"segments", len(pending),
		"batches", len(batches),
		"batch_size", h.BatchSize,
		"max_words_per_batch", h.MaxWordsPerBatch,
		"max_batch_index_span", h.MaxBatchIndexSpan)
	return batches, nil
}

// ProcessBatch 渲染语义质检 prompt → 调用 LLM → 解析 issues。
// 失败时 Issues==nil（batchHandler 跳过写库）；终态扫描失败额外设置 failedSegments。
// 不变量：h.Retry.MaxAttempts 与 round.Retry.MaxAttempts 同源，handler 自限重试，
// 终态失败时不再返回 retry，executor exhausted 分支对 semantic_qa 永不触发。
func (h *SemanticQAHandler) ProcessBatch(ctx context.Context, doc *Document, idxs []int, attempt int, logger *slog.Logger) batchResult {
	batchStart := time.Now()
	rep := h.reporter()
	tried := []string{h.Backend.Name()}
	// canRetry：attempt 从 0 起，MaxAttempts 为额外重试次数，与 executor totalAttempts=MaxAttempts+1 一致。
	canRetry := attempt < h.Retry.MaxAttempts

	segments := make([]prompt.SemanticQASegment, 0, len(idxs))
	for _, idx := range idxs {
		seg := &doc.Segments[idx]
		segments = append(segments, prompt.SemanticQASegment{
			ID:     seg.ID,
			Source: seg.Source,
			Target: seg.Target,
		})
	}

	proto := prompt.ProtocolFromResponseMode(h.ResponseMode)
	isTextMode := proto.IsText()
	sys, usr, renderErr := h.Renderer.Render(prompt.SemanticQAData{
		SourceLang: doc.SourceLang,
		TargetLang: doc.TargetLang,
		Segments:   segments,
		Protocol:   proto,
	})
	if renderErr != nil {
		logger.Error("semantic_qa render failed", "err", renderErr)
		h.emitBatchOutcome(progress.BatchEvent{
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
		return h.terminalFailure(doc, idxs, rep)
	}

	req := backend.Request{
		System: sys,
		User:   usr,
	}
	if isTextMode {
		req.ResponseFormat = "none"
	} else {
		req.JSONSchema = prompt.SemanticQAIssueSchema()
	}

	callStart := time.Now()
	resp, callErr := h.Backend.Translate(ctx, req)
	if callErr != nil {
		// 外部中断：Canceled，或 DeadlineExceeded 且父 ctx 已死（job 取消/外层 deadline）。
		// 本地 backend timeout 产出 DeadlineExceeded 但父 ctx 仍活，不走此分支。
		isExternalInterrupt := errors.Is(callErr, context.Canceled) ||
			(errors.Is(callErr, context.DeadlineExceeded) && ctx.Err() != nil)
		if isExternalInterrupt {
			logger.Info("semantic_qa backend interrupted by context",
				"backend", h.Backend.Name(), "batch_size", len(idxs), "err", callErr)
			h.emitBatchOutcome(progress.BatchEvent{
				Stage:          RoundModeSemanticQA,
				SegmentIDs:     segmentIDStringsFromDoc(doc, idxs),
				SegmentCount:   len(idxs),
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
			return h.preserveResult(doc, idxs, rep)
		}

		// 本地 backend timeout：父 ctx 仍活，按可重试 backend 错误处理。
		// IsRetryable 对 DeadlineExceeded 恒返 false，须在此主动闸为可重试。
		isLocalTimeout := errors.Is(callErr, context.DeadlineExceeded) && ctx.Err() == nil

		h.emitBatchOutcome(progress.BatchEvent{
			Stage:          RoundModeSemanticQA,
			SegmentIDs:     segmentIDStringsFromDoc(doc, idxs),
			SegmentCount:   len(idxs),
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

		// 本地超时 / 5xx / 429 / 裸网络错误可退避重试；401/403 等 4xx 立即终态。
		// 401/403 计入扫描失败（真实配置/权限问题）；外部中断不计入。
		if (isLocalTimeout || backend.IsRetryable(callErr)) && canRetry {
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
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				// 退避期间取消：不计入扫描失败。
				return h.preserveResult(doc, idxs, rep)
			case <-timer.C:
			}
			return batchResult{retry: &batchJob{idxs: idxs, attempt: attempt + 1}}
		}

		if isLocalTimeout {
			logger.Warn("semantic_qa backend local timeout exhausted, producing no issues",
				"backend", h.Backend.Name(), "batch_size", len(idxs),
				"attempt", attempt, "err", callErr)
		} else {
			logger.Warn("semantic_qa backend failed terminally, producing no issues",
				"backend", h.Backend.Name(), "batch_size", len(idxs),
				"attempt", attempt, "err", callErr)
		}
		return h.terminalFailure(doc, idxs, rep)
	}

	atomic.AddInt64(&doc.InputTokens, resp.Usage.PromptTokens)
	atomic.AddInt64(&doc.OutputTokens, resp.Usage.CompletionTokens)

	issues, parseErr := prompt.ParseSemanticQAByMode(resp.Text, isTextMode)
	if parseErr != nil {
		logger.Warn("semantic_qa parse failed",
			"backend", h.Backend.Name(), "batch_size", len(idxs), "err", parseErr,
			"attempt", attempt, "resp_len", len(resp.Text), "resp_head", headSnippet(resp.Text, 200))
		h.emitBatchOutcome(progress.BatchEvent{
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
			RoundIndex:      h.RoundIndex,
			Attempt:         attempt,
			SystemPrompt:    sys,
			UserMessage:     usr,
			ResponseFormat:  req.ResponseFormat,
			JSONSchema:      req.JSONSchema,
			ResponseContent: resp.Text,
		})
		// parse 失败立即再入队（无退避）；受 MaxAttempts 上界约束。
		if canRetry {
			return batchResult{retry: &batchJob{idxs: idxs, attempt: attempt + 1}}
		}
		return h.terminalFailure(doc, idxs, rep)
	}

	// id → []issue（snippet → Span；定位失败仍保留 MatchedText）
	byID := make(map[string][]qa.QualityIssue, len(idxs))
	segByID := make(map[string]*Segment, len(idxs))
	for _, idx := range idxs {
		segByID[doc.Segments[idx].ID] = &doc.Segments[idx]
	}
	for _, iss := range issues {
		var span *qa.Span
		if iss.Snippet != "" {
			if seg, ok := segByID[iss.ID]; ok {
				// 优先在译文中定位，其次源文
				span = qa.LocateSpan(seg.Target, iss.Snippet)
				if span == nil || span.TargetStart == nil {
					if srcSpan := qa.LocateSpan(seg.Source, iss.Snippet); srcSpan != nil {
						// 源文命中：只保留 MatchedText（偏移相对源文，不写入 target_*）
						span = &qa.Span{MatchedText: srcSpan.MatchedText}
					} else if span == nil {
						span = &qa.Span{MatchedText: iss.Snippet}
					}
				}
			} else {
				span = &qa.Span{MatchedText: iss.Snippet}
			}
		}
		byID[iss.ID] = append(byID[iss.ID], qa.QualityIssue{
			Code:     iss.Code,
			Message:  iss.Message,
			Severity: qa.SeverityWarning,
			Span:     span,
		})
	}

	callbackSegs := make([]TranslatedSegment, 0, len(idxs))
	producedTotal := 0
	for _, idx := range idxs {
		seg := &doc.Segments[idx]
		// 非 nil 空切片表示本段已成功扫描且没有问题；nil 留给失败路径表示不写库。
		newIssues := qa.DedupIssues(append([]qa.QualityIssue{}, byID[seg.ID]...))
		for i := range newIssues {
			newIssues[i].SegmentIndex = idx
		}
		producedTotal += len(newIssues)
		// 内存 doc 追加（便于同轮内后续逻辑可见）；DB 合并在 batchHandler
		if len(newIssues) > 0 {
			seg.Issues = append(seg.Issues, newIssues...)
		}
		callbackSegs = append(callbackSegs, TranslatedSegment{
			Index:      idx,
			ID:         seg.ID,
			SourceText: seg.Source,
			TargetText: seg.Target,
			Issues:     newIssues, // 仅本批新产出，batchHandler 负责与 DB 合并
		})
		rep.SegmentDone()
	}

	logger.Info("semantic_qa batch ok",
		"backend", h.Backend.Name(),
		"segments", len(idxs),
		"issues", producedTotal,
		"prompt_tokens", resp.Usage.PromptTokens,
		"completion_tokens", resp.Usage.CompletionTokens)

	h.emitBatchOutcome(progress.BatchEvent{
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
		RoundIndex:      h.RoundIndex,
		Attempt:         attempt,
		SystemPrompt:    sys,
		UserMessage:     usr,
		ResponseFormat:  req.ResponseFormat,
		JSONSchema:      req.JSONSchema,
		ResponseContent: resp.Text,
	})

	return batchResult{
		callbackResult: &BatchResult{Segments: callbackSegs},
	}
}

// preserveResult 不产出新 issue（Issues 为 nil），batchHandler 跳过写库，原 issue 保留。
// 不设置 failedSegments（用于 ctx 取消等外部中断，不计入扫描失败）。
func (h *SemanticQAHandler) preserveResult(doc *Document, idxs []int, rep progress.Reporter) batchResult {
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
		rep.SegmentDone()
	}
	return batchResult{
		callbackResult: &BatchResult{Segments: callbackSegs},
	}
}

// terminalFailure 在 preserveResult 基础上标记 failedSegments，供 RunRound 累计为软警告。
// 依赖 h.Retry 与 round.Retry 同源，故不再返回 retry。
func (h *SemanticQAHandler) terminalFailure(doc *Document, idxs []int, rep progress.Reporter) batchResult {
	pr := h.preserveResult(doc, idxs, rep)
	pr.failedSegments = idxs
	return pr
}
