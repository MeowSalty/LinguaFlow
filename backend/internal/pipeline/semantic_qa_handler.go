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
// pending==nil（池 0）时自行扫描 doc（排除跨轮已解决段）；pending!=nil（池>0）时
// 仅对给定 unresolved 重切，避免全量重扫。
func (h *SemanticQAHandler) BuildBatches(_ context.Context, doc *Document, pending []int, _ int) ([][]int, error) {
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
	var scan []int
	if pending != nil {
		// 池>0：仅处理给定 unresolved（已通过池 0 过滤），避免全量重扫。
		scan = pending
	} else {
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
			// 跨轮增量：池 0 排除上一同模式轮已解决的段。
			if doc.ResolvedIndices != nil {
				if _, ok := doc.ResolvedIndices[i]; ok {
					continue
				}
			}
			scan = append(scan, i)
		}
	}

	if len(scan) == 0 {
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
			"segments", len(scan))
		return [][]int{scan}, nil
	}

	batches := BuildPackedPendingBatches(doc, scan, constraint, h.MaxBatchIndexSpan)
	logger.Info("semantic_qa handler: batches built",
		"segments", len(scan),
		"batches", len(batches),
		"batch_size", h.BatchSize,
		"max_words_per_batch", h.MaxWordsPerBatch,
		"max_batch_index_span", h.MaxBatchIndexSpan)
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
	batchStart := time.Now()
	rep := h.reporter()
	tried := []string{h.Backend.Name()}

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
			h.emitBatchOutcome(backendErrorBatchEvent(RoundModeSemanticQA, doc, idxs, h.Backend.Name(), tried, callErr, attempt, h.RoundIndex, time.Since(callStart).Milliseconds(), sys, usr, req))
			return h.preserveResult(doc, idxs, rep)
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
			h.emitBatchOutcome(backendErrorBatchEvent(RoundModeSemanticQA, doc, idxs, h.Backend.Name(), tried, callErr, attempt, h.RoundIndex, time.Since(callStart).Milliseconds(), sys, usr, req))
			for range idxs {
				rep.SegmentDone()
			}
			return batchResult{fatalUnresolved: idxs}
		}

		h.emitBatchOutcome(backendErrorBatchEvent(RoundModeSemanticQA, doc, idxs, h.Backend.Name(), tried, callErr, attempt, h.RoundIndex, time.Since(callStart).Milliseconds(), sys, usr, req))

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
		// 不调用 SegmentDone（段尚未解决），不返回 callbackResult（避免写回空 issue）。
		return batchResult{unresolved: idxs}
	}

	if resp.Truncated {
		logTruncatedResponse(logger, h.Backend.Name())
	}
	atomic.AddInt64(&doc.InputTokens, resp.Usage.PromptTokens)
	atomic.AddInt64(&doc.OutputTokens, resp.Usage.CompletionTokens)

	issues, parseRepaired, parseErr := repair.ParseSemanticQAByMode(resp.Text, isTextMode, h.Repair)
	// 截断响应对 fail-closed stage 恒不采纳：issues 的 partial 会被下游解释为
	// 「缺失段=已扫描无问题」（假阴性质检）。JSON 模式下 WithoutSalvage 已拒绝
	// 可检测的截断形态，此处封住两个残余通道——text 协议逐行解析无完整性信号
	// （截断的已完成行被当作完整结果）、以及截断点恰在完整边界导致解析成功；
	// 截断即报错走重试/下一池，代价由池预算约束。
	if parseErr == nil && resp.Truncated {
		parseErr = fmt.Errorf("response truncated by output token limit: refusing partial issues as complete")
	}
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
		if attempt+1 < transientBudgetFor(h.Retry) {
			return batchResult{retry: &batchJob{idxs: idxs, attempt: attempt + 1}}
		}
		// 不调用 SegmentDone（段尚未解决），不返回 callbackResult（避免写回空 issue）。
		return batchResult{unresolved: idxs}
	}

	// id → []issue（snippet → Span；定位失败仍保留 MatchedText）
	if len(parseRepaired) > 0 {
		logger.Info("semantic_qa response repaired",
			"backend", h.Backend.Name(), "ops", parseRepaired)
	}

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
