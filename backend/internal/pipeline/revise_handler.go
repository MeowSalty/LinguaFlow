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
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
)

// ReviseHandler 实现 RoundHandler，对带待处理语义问题的译文做 LLM 修订。
// 不改译文、不改段落状态；内存与数据库写回由批次处理器负责。
type ReviseHandler struct {
	Backend          backend.Backend
	Renderer         *prompt.ReviseRenderer
	BatchSize        int
	MaxWordsPerBatch int
	// MaxBatchIndexSpan 同批段落文档索引跨度上限（max-min）；<=0 不限制（默认）。
	MaxBatchIndexSpan int
	Retry             backend.RetryPolicy
	ResponseMode      string
	Repair            repair.Options
	IssueCodes        []string
	Reporter          progress.Reporter
	Logger            *slog.Logger
	RoundIndex        int // execution plan round index, set by caller
}

func (h *ReviseHandler) ModeName() string { return RoundModeRevise }

func (h *ReviseHandler) Finalize(_ context.Context, _ *Document, _ []int) error { return nil }

func (h *ReviseHandler) logger() *slog.Logger {
	if h.Logger == nil {
		return slog.Default()
	}
	return h.Logger
}

func (h *ReviseHandler) reporter() progress.Reporter {
	if h.Reporter == nil {
		return progress.Nop{}
	}
	return h.Reporter
}

func (h *ReviseHandler) emitBatchOutcome(evt progress.BatchEvent) {
	if h.Reporter == nil {
		return
	}
	obs, ok := h.Reporter.(progress.BatchObserver)
	if ok {
		obs.OnBatchEvent(evt)
	}
}

func (h *ReviseHandler) issueCodeSet() map[string]struct{} {
	codes := h.IssueCodes
	if len(codes) == 0 {
		codes = qa.SemanticQACodes()
	}
	set := make(map[string]struct{}, len(codes))
	for _, code := range codes {
		set[code] = struct{}{}
	}
	return set
}

func reviseSegmentInScope(seg Segment, codes map[string]struct{}) bool {
	for _, issue := range seg.Issues {
		if issue.IsPending() {
			if _, ok := codes[issue.Code]; ok {
				return true
			}
		}
	}
	return false
}

// BuildBatches 选 Translate 标记、status∈{translated,edited}、Target 非空且含待修订
// 语义问题的段，按约束分批。pending==nil 时自行扫描 doc 并排除跨轮已解决段。
func (h *ReviseHandler) BuildBatches(_ context.Context, doc *Document, pending []int, _ int) ([][]int, error) {
	logger := h.logger()
	if h.Renderer == nil {
		logger.Warn("revise handler: renderer is nil, skipping")
		return nil, nil
	}
	if h.Backend == nil {
		logger.Warn("revise handler: backend is nil, skipping")
		return nil, nil
	}

	codes := h.issueCodeSet()
	var scan []int
	if pending != nil {
		// 池>0：仅处理给定 unresolved，避免全量重扫。
		scan = pending
	} else {
		for i := range doc.Segments {
			seg := &doc.Segments[i]
			if !seg.Translate || (seg.Status != "translated" && seg.Status != "edited") || seg.Target == "" {
				continue
			}
			if !reviseSegmentInScope(*seg, codes) {
				continue
			}
			if doc.ResolvedIndices != nil {
				if _, ok := doc.ResolvedIndices[i]; ok {
					continue
				}
			}
			scan = append(scan, i)
		}
	}
	if len(scan) == 0 {
		logger.Info("revise handler: no segments with pending issues")
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
		logger.Info("revise handler: no batch limit, sending all segments at once", "segments", len(scan))
		return [][]int{scan}, nil
	}
	batches := BuildPackedPendingBatches(doc, scan, constraint, h.MaxBatchIndexSpan)
	logger.Info("revise handler: batches built", "segments", len(scan), "batches", len(batches),
		"batch_size", h.BatchSize, "max_words_per_batch", h.MaxWordsPerBatch,
		"max_batch_index_span", h.MaxBatchIndexSpan)
	return batches, nil
}

// ProcessBatch 渲染修订 prompt → 调用 LLM → 解析整段修订译文。
// 失败时不产出 callbackResult；成功时 callback 仅包含 LLM 返回的合法段。
func (h *ReviseHandler) ProcessBatch(ctx context.Context, doc *Document, idxs []int, attempt int, logger *slog.Logger) batchResult {
	batchStart := time.Now()
	rep := h.reporter()
	tried := []string{h.Backend.Name()}
	codes := h.issueCodeSet()

	segments := make([]prompt.ReviseSegment, 0, len(idxs))
	for _, idx := range idxs {
		seg := &doc.Segments[idx]
		issues := make([]prompt.ReviseIssue, 0)
		for _, issue := range seg.Issues {
			if !issue.IsPending() {
				continue
			}
			if _, ok := codes[issue.Code]; !ok {
				continue
			}
			snippet := ""
			if issue.Span != nil {
				snippet = issue.Span.MatchedText
			}
			issues = append(issues, prompt.ReviseIssue{Code: issue.Code, Message: issue.Message, Snippet: snippet})
		}
		segments = append(segments, prompt.ReviseSegment{ID: seg.ID, Source: seg.Source, Target: seg.Target, Issues: issues})
	}

	proto := prompt.ProtocolFromResponseMode(h.ResponseMode)
	isTextMode := proto.IsText()
	sys, usr, renderErr := h.Renderer.Render(prompt.ReviseData{
		SourceLang: doc.SourceLang,
		TargetLang: doc.TargetLang,
		Segments:   segments,
		Protocol:   proto,
	})
	if renderErr != nil {
		logger.Error("revise render failed", "err", renderErr)
		h.emitBatchOutcome(progress.BatchEvent{
			Stage: RoundModeRevise, SegmentIDs: segmentIDStringsFromDoc(doc, idxs), SegmentCount: len(idxs),
			BackendName: h.Backend.Name(), Status: "failed", DurationMs: time.Since(batchStart).Milliseconds(),
			TriedBackends: tried, ErrorType: "render_error", ErrorMessage: renderErr.Error(), RoundIndex: h.RoundIndex,
		})
		return h.terminalFailure(doc, idxs, rep)
	}

	req := backend.Request{System: sys, User: usr}
	if isTextMode {
		req.ResponseFormat = "none"
	} else {
		req.JSONSchema = prompt.ReviseRevisionSchema()
	}

	callStart := time.Now()
	resp, callErr := h.Backend.Translate(ctx, req)
	if callErr != nil {
		isExternalInterrupt := errors.Is(callErr, context.Canceled) ||
			(errors.Is(callErr, context.DeadlineExceeded) && ctx.Err() != nil)
		if isExternalInterrupt {
			logger.Info("revise backend interrupted by context", "backend", h.Backend.Name(), "batch_size", len(idxs), "err", callErr)
			h.emitBatchOutcome(backendErrorBatchEvent(RoundModeRevise, doc, idxs, h.Backend.Name(), tried, callErr, attempt, h.RoundIndex, time.Since(callStart).Milliseconds(), sys, usr, req))
			return h.preserveResult(doc, idxs, rep)
		}
		isLocalTimeout := errors.Is(callErr, context.DeadlineExceeded) && ctx.Err() == nil
		if isFatalBackendError(callErr) {
			logger.Warn("revise backend fatal error, deferring to cross-round", "backend", h.Backend.Name(), "batch_size", len(idxs), "attempt", attempt, "err", callErr)
			h.emitBatchOutcome(backendErrorBatchEvent(RoundModeRevise, doc, idxs, h.Backend.Name(), tried, callErr, attempt, h.RoundIndex, time.Since(callStart).Milliseconds(), sys, usr, req))
			for range idxs {
				rep.SegmentDone()
			}
			return batchResult{fatalUnresolved: idxs}
		}
		h.emitBatchOutcome(backendErrorBatchEvent(RoundModeRevise, doc, idxs, h.Backend.Name(), tried, callErr, attempt, h.RoundIndex, time.Since(callStart).Milliseconds(), sys, usr, req))
		if (isLocalTimeout || backend.IsRetryable(callErr)) && attempt+1 < transientBudgetFor(h.Retry) {
			logger.Warn("revise backend error, will backoff and retry", "backend", h.Backend.Name(), "batch_size", len(idxs), "attempt", attempt, "err", callErr)
			wait := backoffDuration(attempt, h.Retry, callErr)
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return h.preserveResult(doc, idxs, rep)
			case <-timer.C:
			}
			return batchResult{retry: &batchJob{idxs: idxs, attempt: attempt + 1}}
		}
		logger.Warn("revise backend failed terminally, deferring to next pool", "backend", h.Backend.Name(), "batch_size", len(idxs), "attempt", attempt, "err", callErr)
		return batchResult{unresolved: idxs}
	}

	atomic.AddInt64(&doc.InputTokens, resp.Usage.PromptTokens)
	atomic.AddInt64(&doc.OutputTokens, resp.Usage.CompletionTokens)
	revisions, parseRepaired, parseErr := repair.ParseReviseByMode(resp.Text, isTextMode, h.Repair)
	if parseErr != nil {
		logger.Warn("revise parse failed", "backend", h.Backend.Name(), "batch_size", len(idxs), "err", parseErr, "attempt", attempt, "resp_len", len(resp.Text), "resp_head", headSnippet(resp.Text, 200))
		h.emitBatchOutcome(progress.BatchEvent{
			Stage: RoundModeRevise, SegmentIDs: segmentIDStringsFromDoc(doc, idxs), SegmentCount: len(idxs), BackendName: h.Backend.Name(), Status: "failed",
			DurationMs: time.Since(callStart).Milliseconds(), InputTokens: resp.Usage.PromptTokens, OutputTokens: resp.Usage.CompletionTokens,
			SentContent: usr, ReceivedContent: resp.Text, TriedBackends: tried, ErrorType: "parse_error", ErrorMessage: parseErr.Error(), RoundIndex: h.RoundIndex,
			Attempt: attempt, SystemPrompt: sys, UserMessage: usr, ResponseFormat: req.ResponseFormat, JSONSchema: req.JSONSchema, ResponseContent: resp.Text,
		})
		if attempt+1 < transientBudgetFor(h.Retry) {
			return batchResult{retry: &batchJob{idxs: idxs, attempt: attempt + 1}}
		}
		return batchResult{unresolved: idxs}
	}
	if len(parseRepaired) > 0 {
		logger.Info("revise response repaired", "backend", h.Backend.Name(), "ops", parseRepaired)
	}

	segByID := make(map[string]*Segment, len(idxs))
	for _, idx := range idxs {
		segByID[doc.Segments[idx].ID] = &doc.Segments[idx]
	}
	callbackSegs := make([]TranslatedSegment, 0, len(idxs))
	returned := make(map[int]struct{}, len(idxs))
	seen := make(map[string]struct{}, len(revisions))
	for _, revision := range revisions {
		seg, ok := segByID[revision.ID]
		if !ok {
			continue
		}
		if _, duplicate := seen[revision.ID]; duplicate {
			continue
		}
		seen[revision.ID] = struct{}{}
		for _, idx := range idxs {
			if doc.Segments[idx].ID == revision.ID {
				callbackSegs = append(callbackSegs, TranslatedSegment{Index: idx, ID: seg.ID, SourceText: seg.Source, TargetText: revision.Target})
				returned[idx] = struct{}{}
				break
			}
		}
	}
	// LLM 漏返的段计入 unresolved（与 translate 轮 trans 缺失即 unresolved 的约定
	// 一致），交由下一池重试、末轮跨轮传播并触发软警告。若只按返回段回调，
	// computeResolved 会把漏返段计为已解决，后续同模式轮将跳过它们，pending
	// issue 无人修订且不产生任何失败信号。
	missing := make([]int, 0)
	for _, idx := range idxs {
		if _, ok := returned[idx]; ok {
			rep.SegmentDone()
			continue
		}
		missing = append(missing, idx)
	}
	if len(missing) > 0 {
		logger.Warn("revise response missing segment revisions", "backend", h.Backend.Name(), "batch_size", len(idxs), "missing", len(missing))
	}

	logger.Info("revise batch ok", "backend", h.Backend.Name(), "segments", len(idxs), "revised", len(callbackSegs),
		"missing", len(missing),
		"prompt_tokens", resp.Usage.PromptTokens, "completion_tokens", resp.Usage.CompletionTokens)
	h.emitBatchOutcome(progress.BatchEvent{
		Stage: RoundModeRevise, SegmentIDs: segmentIDStringsFromDoc(doc, idxs), SegmentCount: len(idxs), BackendName: h.Backend.Name(), Status: "success",
		DurationMs: time.Since(batchStart).Milliseconds(), InputTokens: resp.Usage.PromptTokens, OutputTokens: resp.Usage.CompletionTokens,
		SentContent: usr, ReceivedContent: resp.Text, TriedBackends: tried, RoundIndex: h.RoundIndex, Attempt: attempt,
		SystemPrompt: sys, UserMessage: usr, ResponseFormat: req.ResponseFormat, JSONSchema: req.JSONSchema, ResponseContent: resp.Text,
	})
	return batchResult{callbackResult: &BatchResult{Segments: callbackSegs}, unresolved: missing}
}

// preserveResult 保留原译文；修订 handler 不在内存中改写段落。
func (h *ReviseHandler) preserveResult(doc *Document, idxs []int, rep progress.Reporter) batchResult {
	callbackSegs := make([]TranslatedSegment, 0, len(idxs))
	for _, idx := range idxs {
		seg := &doc.Segments[idx]
		callbackSegs = append(callbackSegs, TranslatedSegment{Index: idx, ID: seg.ID, SourceText: seg.Source, TargetText: seg.Target})
		rep.SegmentDone()
	}
	return batchResult{callbackResult: &BatchResult{Segments: callbackSegs}}
}

// terminalFailure 在 preserveResult 基础上标记 failedSegments，供 RunRound 累计为软警告。
func (h *ReviseHandler) terminalFailure(doc *Document, idxs []int, rep progress.Reporter) batchResult {
	result := h.preserveResult(doc, idxs, rep)
	result.failedSegments = idxs
	return result
}
