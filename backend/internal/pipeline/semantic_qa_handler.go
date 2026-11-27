package pipeline

import (
	"context"
	"errors"
	"log/slog"
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

// ProcessBatch renders and dispatches a batch. The response is applied through its capture.
func (h *SemanticQAHandler) ProcessBatch(ctx context.Context, doc *Document, idxs []int, attempt int, logger *slog.Logger) batchResult {
	session, _ := ctx.Value(requestSessionKey{}).(*requestSession)
	if session == nil || !session.stored || len(session.intent.InputMembers) == 0 {
		return batchResult{err: errors.New("semantic QA requires a frozen request")}
	}
	var diagnostic storedBatchDiagnostic
	defer diagnostic.close()
	emit := func(evt progress.BatchEvent) { h.emitBatchOutcome(diagnostic.apply(ctx, evt)) }
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
			if e := backend.ReserveSlice[prompt.SemanticQASegment](b, int64(len(idxs))); e != nil {
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
		if storedDispatchDeferred(ctx, renderErr) {
			return batchResult{deferred: true}
		}
		if ctx.Err() != nil {
			return batchResult{deferred: true}
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
		return batchResult{failedSegments: idxs}
	}

	diagnostic.captureRequest(ctx, h.Reporter, req)
	callStart := time.Now()
	resp, callErr := h.Backend.Translate(ctx, req)
	if callErr == nil {
		if !retainStoredBatchDiagnostic(ctx, doc, idxs, h.Reporter, &diagnostic, RoundModeSemanticQA, h.Backend.Name(), req.ResponseFormat, batchStart) {
			return batchResult{err: errors.New("model response was not captured")}
		}
		return batchResult{}
	}
	diagnostic.captureResponse(ctx, &req, resp, &sys, &usr)
	emitBackendFailure := func(err error) {
		if observedStoredProviderFailure(ctx, h.Backend) {
			emit(backendErrorBatchEvent(RoundModeSemanticQA, doc, idxs, h.Backend.Name(), tried, err, attempt, h.RoundIndex, time.Since(callStart).Milliseconds(), sys, usr, req))
		}
	}
	if storedDispatchDeferred(ctx, callErr) {
		return batchResult{deferred: true}
	}
	if storageFailure(callErr) {
		if providerErr := storedProviderFailure(ctx); providerErr != nil {
			emitBackendFailure(providerErr)
		}
		return batchResult{err: callErr}
	}
	if d := workDeferred(callErr); d != nil {
		return batchResult{waitCapacity: d, deferred: true}
	}
	logger.Warn("semantic_qa backend failed", "backend", h.Backend.Name(), "batch_size", len(idxs), "attempt", attempt, "err", callErr)
	emitBackendFailure(callErr)
	return batchResult{unresolved: idxs}
}

func (h *SemanticQAHandler) processStoredResponse(ctx context.Context, doc *Document, idxs []int, resp *backend.Response, attempt int, logger *slog.Logger) batchResult {
	if resp.Truncated {
		logTruncatedResponse(logger, h.Backend.Name())
	}
	isTextMode := prompt.ProtocolFromResponseMode(h.ResponseMode).IsText()

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
		return SealStoredBatchResult(b, callback, b.OutputLimit())
	})
	// 截断响应对 fail-closed stage 恒不采纳：issues 的 partial 会被下游解释为
	// 「缺失段=已扫描无问题」（假阴性质检）。JSON 模式下 WithoutSalvage 已拒绝
	// 可检测的截断形态，此处封住两个残余通道——text 协议逐行解析无完整性信号
	// （截断的已完成行被当作完整结果）、以及截断点恰在完整边界导致解析成功；
	// 截断即报错走重试/下一池，代价由池预算约束。
	if parseErr != nil {
		if wait := workDeferred(parseErr); wait != nil {
			return batchResult{deferred: true, waitCapacity: wait}
		}
		if ctx.Err() != nil || storedDispatchDeferred(ctx, parseErr) {
			return batchResult{deferred: true}
		}
		logger.Warn("semantic_qa parse failed",
			"backend", h.Backend.Name(), "batch_size", len(idxs), "err", parseErr,
			"attempt", attempt, "resp_len", len(resp.Text), "resp_head", headSnippet(resp.Text, 200))
		emitStoredBatchResponse(ctx, resp, parseErr, parseRepaired, false)
		// parse 失败：预算内立即再入队（无退避）；耗尽则交下一池重切。
		// LLM 响应畸形可能因随机性恢复，故预算内走重试通道（与路由表"解析失败→retry"一致）。
		// 预算耗尽后落 unresolved：换更小批次重切可能成功，末轮仍未解决则跨轮传播换 backend；
		// 可见性由 job_runner 的 warning_message 通道承担，不依赖 failedSegments 软警告。
		if errors.Is(parseErr, backend.ErrBuildCapacity) {
			return batchResult{failedSegments: idxs, storedParseErr: parseErr}
		}
		if attempt+1 < transientBudgetFor(h.Retry) {
			return batchResult{retry: &batchJob{idxs: idxs, attempt: attempt + 1}, storedParseErr: parseErr}
		}
		// 不计数（段尚未解决，executor 在终态批次时计数），不返回 callbackResult（避免写回空 issue）。
		return batchResult{unresolved: idxs, storedParseErr: parseErr}
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

	emitStoredBatchResponse(ctx, resp, nil, parseRepaired, false)

	return batchResult{
		callbackResult: callback,
	}
}

func (h *SemanticQAHandler) buildSemanticResultBounded(b *backend.BuildBounds, doc *Document, idxs []int, issues []prompt.SemanticQAIssue) (*BatchResult, int, error) {
	byID := make(map[string][]qa.QualityIssue, len(idxs))
	segByID := make(map[string]*Segment, len(idxs))
	for _, idx := range idxs {
		segByID[doc.Segments[idx].ID] = &doc.Segments[idx]
	}
	for _, issue := range issues {
		if e := backend.ReserveSlice[qa.Span](b, 3); e != nil {
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
	if e := backend.ReserveSlice[TranslatedSegment](b, int64(len(idxs))); e != nil {
		return nil, 0, e
	}
	result := &BatchResult{Segments: make([]TranslatedSegment, 0, len(idxs))}
	total := 0
	for _, idx := range idxs {
		seg := &doc.Segments[idx]
		items := byID[seg.ID]
		if e := backend.ReserveSlice[qa.QualityIssue](b, int64(len(items))); e != nil {
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
