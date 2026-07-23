package pipeline

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

// 默认可裁决 code；untranslated / duplicate 为硬规则，永不交给 AI。
var defaultAdjudicateCodes = []string{"source_residual"}

// AdjudicateHandler 实现 RoundHandler，对已标出问题的段落做 AI 裁决，剔除误报。
// 不改译文、不改段落状态；失败时一律保留原 issue。
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
	AdjudicateCodes   []string
	Reporter          progress.Reporter
	Logger            *slog.Logger
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

func (h *AdjudicateHandler) reporter() progress.Reporter {
	if h.Reporter == nil {
		return progress.Nop{}
	}
	return h.Reporter
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
	if len(h.AdjudicateCodes) == 0 {
		return defaultAdjudicateCodes
	}
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
		if _, ok := codes[iss.Code]; ok {
			return true
		}
	}
	return false
}

// BuildBatches 选 status∈{translated,edited} 且 Issues 含可裁决 code 的段，按约束分批。
func (h *AdjudicateHandler) BuildBatches(_ context.Context, doc *Document) ([][]int, error) {
	logger := h.logger()
	if h.Renderer == nil {
		logger.Warn("adjudicate handler: renderer is nil, skipping")
		return nil, nil
	}
	if h.Backend == nil {
		logger.Warn("adjudicate handler: backend is nil, skipping")
		return nil, nil
	}

	codes := adjudicateCodeSet(h.adjudicateCodes())
	var pending []int
	for i := range doc.Segments {
		seg := &doc.Segments[i]
		if seg.Status != "translated" && seg.Status != "edited" {
			continue
		}
		if !segmentHasAdjudicableIssue(seg.Issues, codes) {
			continue
		}
		pending = append(pending, i)
	}

	if len(pending) == 0 {
		logger.Info("adjudicate handler: no adjudicable segments")
		return nil, nil
	}

	constraint := BatchConstraint{
		MaxSegments: h.BatchSize,
		MaxWords:    h.MaxWordsPerBatch,
	}
	if constraint.MaxSegments <= 0 && constraint.MaxWords <= 0 && h.MaxBatchIndexSpan <= 0 {
		logger.Info("adjudicate handler: no batch limit, sending all segments at once",
			"segments", len(pending))
		return [][]int{pending}, nil
	}

	// 顺序贪心打包：允许索引不连续的段落同批，提高字词预算利用率。
	// 注意：MaxBatchIndexSpan 为预埋特性，当前未接入 schema/OpenAPI/执行计划配置，
	// 生产环境恒为 0（即仅按段落数/字词数约束打包，不限制索引跨度）；
	// 后续如需启用，须在 ent schema、OpenAPI 规范及 handler 映射中同步补齐字段。
	batches := BuildPackedPendingBatches(doc, pending, constraint, h.MaxBatchIndexSpan)
	logger.Info("adjudicate handler: batches built",
		"segments", len(pending),
		"batches", len(batches),
		"batch_size", h.BatchSize,
		"max_words_per_batch", h.MaxWordsPerBatch,
		"max_batch_index_span", h.MaxBatchIndexSpan)
	return batches, nil
}

// ProcessBatch 渲染裁决 prompt → 调用 LLM → 解析 verdict → 剔除 false_positive。
// 失败/解析失败/缺失判定一律保留原 issue。
func (h *AdjudicateHandler) ProcessBatch(ctx context.Context, doc *Document, idxs []int, attempt int, logger *slog.Logger) batchResult {
	batchStart := time.Now()
	rep := h.reporter()
	codes := adjudicateCodeSet(h.adjudicateCodes())
	tried := []string{h.Backend.Name()}

	// 构建裁决输入（仅可裁决 issue 子集）
	segments := make([]prompt.AdjudicationSegment, 0, len(idxs))
	for _, idx := range idxs {
		seg := &doc.Segments[idx]
		var issues []prompt.AdjudicationIssue
		for _, iss := range seg.Issues {
			if _, ok := codes[iss.Code]; ok {
				issues = append(issues, prompt.AdjudicationIssue{
					Code:    iss.Code,
					Message: iss.Message,
				})
			}
		}
		segments = append(segments, prompt.AdjudicationSegment{
			ID:     seg.ID,
			Source: seg.Source,
			Target: seg.Target,
			Issues: issues,
		})
	}

	sys, usr, renderErr := h.Renderer.Render(prompt.AdjudicationData{
		SourceLang: doc.SourceLang,
		TargetLang: doc.TargetLang,
		Segments:   segments,
	})
	if renderErr != nil {
		logger.Error("adjudicate render failed", "err", renderErr)
		h.emitBatchOutcome(progress.BatchEvent{
			Stage:         RoundModeAdjudicate,
			SegmentIDs:    segmentIDStrings(idxs),
			SegmentCount:  len(idxs),
			BackendName:   h.Backend.Name(),
			Status:        "failed",
			DurationMs:    time.Since(batchStart).Milliseconds(),
			TriedBackends: tried,
			ErrorType:     "render_error",
			ErrorMessage:  renderErr.Error(),
		})
		return h.preserveResult(doc, idxs, rep)
	}

	// handler 强制 json_schema；不支持的后端忽略该字段 → 自由文本 → 解析失败 → 保留原 issue
	req := backend.Request{
		System:         sys,
		User:           usr,
		ResponseFormat: "json_schema",
		JSONSchema:     prompt.AdjudicationVerdictSchema(),
	}

	callStart := time.Now()
	resp, callErr := h.Backend.Translate(ctx, req)
	if callErr != nil {
		if isFatalBackendError(callErr) {
			logger.Error("adjudicate backend fatal error",
				"backend", h.Backend.Name(), "batch_size", len(idxs), "err", callErr)
			h.emitBatchOutcome(progress.BatchEvent{
				Stage:         RoundModeAdjudicate,
				SegmentIDs:    segmentIDStrings(idxs),
				SegmentCount:  len(idxs),
				BackendName:   h.Backend.Name(),
				Status:        "failed",
				DurationMs:    time.Since(callStart).Milliseconds(),
				SentContent:   usr,
				TriedBackends: tried,
				ErrorType:     "backend_error",
				ErrorMessage:  callErr.Error(),
				HTTPStatus:    httpStatusFromErr(callErr),
			})
			return h.preserveResult(doc, idxs, rep)
		}
		if isRetryableByBackoff(callErr) {
			logger.Warn("adjudicate rate limit, will backoff and retry",
				"backend", h.Backend.Name(), "batch_size", len(idxs), "err", callErr)
			h.emitBatchOutcome(progress.BatchEvent{
				Stage:         RoundModeAdjudicate,
				SegmentIDs:    segmentIDStrings(idxs),
				SegmentCount:  len(idxs),
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
				return h.preserveResult(doc, idxs, rep)
			case <-timer.C:
			}
			return batchResult{retry: &batchJob{idxs: idxs, attempt: attempt + 1}}
		}
		logger.Warn("adjudicate backend failed, preserving issues",
			"backend", h.Backend.Name(), "batch_size", len(idxs), "err", callErr)
		h.emitBatchOutcome(progress.BatchEvent{
			Stage:         RoundModeAdjudicate,
			SegmentIDs:    segmentIDStrings(idxs),
			SegmentCount:  len(idxs),
			BackendName:   h.Backend.Name(),
			Status:        "failed",
			DurationMs:    time.Since(callStart).Milliseconds(),
			SentContent:   usr,
			TriedBackends: tried,
			ErrorType:     "backend_error",
			ErrorMessage:  callErr.Error(),
			HTTPStatus:    httpStatusFromErr(callErr),
		})
		return h.preserveResult(doc, idxs, rep)
	}

	atomic.AddInt64(&doc.InputTokens, resp.Usage.PromptTokens)
	atomic.AddInt64(&doc.OutputTokens, resp.Usage.CompletionTokens)

	verdicts, parseErr := prompt.ParseAdjudicationResponse(resp.Text)
	if parseErr != nil {
		logger.Warn("adjudicate parse failed, preserving issues",
			"backend", h.Backend.Name(), "batch_size", len(idxs), "err", parseErr,
			"resp_len", len(resp.Text), "resp_head", headSnippet(resp.Text, 200))
		h.emitBatchOutcome(progress.BatchEvent{
			Stage:           RoundModeAdjudicate,
			SegmentIDs:      segmentIDStrings(idxs),
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
		})
		return h.preserveResult(doc, idxs, rep)
	}

	// (id, issue_code) → verdict
	verdictMap := make(map[string]string, len(verdicts))
	for _, v := range verdicts {
		key := v.ID + "\x00" + v.IssueCode
		verdictMap[key] = v.Verdict
	}

	callbackSegs := make([]TranslatedSegment, 0, len(idxs))
	dismissedTotal := 0
	for _, idx := range idxs {
		seg := &doc.Segments[idx]
		filtered := filterIssuesByVerdicts(seg.Issues, seg.ID, codes, verdictMap, logger)
		dismissedTotal += len(seg.Issues) - len(filtered)
		seg.Issues = filtered
		callbackSegs = append(callbackSegs, TranslatedSegment{
			Index:      idx,
			ID:         seg.ID,
			SourceText: seg.Source,
			TargetText: seg.Target,
			Issues:     filtered,
		})
		rep.SegmentDone()
	}

	logger.Info("adjudicate batch ok",
		"backend", h.Backend.Name(),
		"segments", len(idxs),
		"dismissed", dismissedTotal,
		"prompt_tokens", resp.Usage.PromptTokens,
		"completion_tokens", resp.Usage.CompletionTokens)

	h.emitBatchOutcome(progress.BatchEvent{
		Stage:           RoundModeAdjudicate,
		SegmentIDs:      segmentIDStrings(idxs),
		SegmentCount:    len(idxs),
		BackendName:     h.Backend.Name(),
		Status:          "success",
		DurationMs:      time.Since(batchStart).Milliseconds(),
		InputTokens:     resp.Usage.PromptTokens,
		OutputTokens:    resp.Usage.CompletionTokens,
		SentContent:     usr,
		ReceivedContent: resp.Text,
		TriedBackends:   tried,
	})

	return batchResult{
		callbackResult: &BatchResult{Segments: callbackSegs},
	}
}

// preserveResult 保留原 issues 并返回 callback（供 worker 无害 no-op 或重写同一列表）。
func (h *AdjudicateHandler) preserveResult(doc *Document, idxs []int, rep progress.Reporter) batchResult {
	callbackSegs := make([]TranslatedSegment, 0, len(idxs))
	for _, idx := range idxs {
		seg := &doc.Segments[idx]
		callbackSegs = append(callbackSegs, TranslatedSegment{
			Index:      idx,
			ID:         seg.ID,
			SourceText: seg.Source,
			TargetText: seg.Target,
			Issues:     append([]qa.QualityIssue(nil), seg.Issues...),
		})
		rep.SegmentDone()
	}
	return batchResult{
		callbackResult: &BatchResult{Segments: callbackSegs},
	}
}

// filterIssuesByVerdicts 仅剔除可裁决且 verdict==false_positive 的 issue；其余保留。
func filterIssuesByVerdicts(
	issues []qa.QualityIssue,
	segID string,
	codes map[string]struct{},
	verdictMap map[string]string,
	logger *slog.Logger,
) []qa.QualityIssue {
	if len(issues) == 0 {
		return nil
	}
	out := make([]qa.QualityIssue, 0, len(issues))
	for _, iss := range issues {
		if _, adjudicable := codes[iss.Code]; !adjudicable {
			out = append(out, iss)
			continue
		}
		key := segID + "\x00" + iss.Code
		v, ok := verdictMap[key]
		if !ok || v != "false_positive" {
			// 缺失 / real / 其他 → 保留
			out = append(out, iss)
			continue
		}
		logger.Info("adjudicate dismissed false_positive",
			"segment_id", segID, "code", iss.Code, "message", iss.Message)
	}
	return out
}
