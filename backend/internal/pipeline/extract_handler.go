package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync/atomic"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
)

// ExtractHandler 实现 RoundHandler，执行术语抽取批次处理。
// 整个 handler 是「尽力而为」：单批失败仅 warn，不阻断 pipeline。
type ExtractHandler struct {
	Backends             []backend.Backend
	Renderer             *prompt.BootstrapRenderer
	Glossary             glossary.Glossary
	Retry                backend.RetryPolicy
	BatchSize            int
	MaxWordsPerBatch     int
	MaxTermsPer1000Chars float64
	MinSourceLen         int
	Repair               repair.Options
	ResponseMode         string // 与后端 options.response_format 对齐；"text" 走纯文本协议

	Logger   *slog.Logger
	Reporter progress.Reporter

	scannedSegments atomic.Int64 // 池 0 扫描的去重段数（best-effort all-failed 检测分母，与重试/池数解耦）

	// Gate 是任务级暂停闸门（退避重试等待中止信号）；nil 时无暂停语义。
	Gate *PauseGate

	RoundIndex int // execution plan round index, set by caller
}

func (h *ExtractHandler) ModeName() string { return "extract" }

func (h *ExtractHandler) Finalize(_ context.Context, _ *Document, unresolved []int) error {
	scanned := h.scannedSegments.Load()
	// 全部扫描段最终仍未解决（含 fatalUnresolved）即视为全批失败。
	// 基于扫描集合而非批处理计数，避免被在途重试/多池放大（旧 totalBatches/failedBatches 会随重试膨胀）。
	if scanned > 0 && len(unresolved) >= int(scanned) {
		return fmt.Errorf("extract: all %d scanned segment(s) failed", scanned)
	}
	return nil
}

func (h *ExtractHandler) logger() *slog.Logger {
	if h.Logger == nil {
		return slog.Default()
	}
	return h.Logger
}

// emitBatchOutcome 发送批次事件到 Reporter。
func (h *ExtractHandler) emitBatchOutcome(evt progress.BatchEvent) {
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

// BuildBatches 收集待抽取的段落索引，按 BatchConstraint 分批。
// 跳过 Skip 和空白段落。不扩展上下文。
// batch_size 和 max_words_per_batch 都为 0 时，不分批，全部一次发送。
// pending==nil（池 0）时自行扫描 doc（排除跨轮已解决段）；pending!=nil（池>0）时
// 仅对给定 unresolved 重切，避免全量重扫。返回的批次交给 executor 执行。
func (h *ExtractHandler) BuildBatches(_ context.Context, doc *Document, pending []int, _ int) ([][]int, error) {
	logger := h.logger()

	if h.Renderer == nil {
		logger.Warn("extract handler: renderer is nil, skipping")
		return nil, nil
	}
	if len(h.Backends) == 0 {
		logger.Warn("extract handler: no backends, skipping")
		return nil, nil
	}
	if h.Glossary == nil {
		logger.Warn("extract handler: glossary is nil, skipping")
		return nil, nil
	}

	var scan []int
	if pending != nil {
		// 池>0：仅处理给定 unresolved（已通过池 0 过滤），避免全量重扫。
		scan = pending
	} else {
		for i := range doc.Segments {
			seg := &doc.Segments[i]
			if seg.Skip {
				continue
			}
			t := seg.OriginalSource
			if t == "" {
				t = seg.Source
			}
			if strings.TrimSpace(t) == "" {
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
		logger.Info("extract handler: no text to scan")
		return nil, nil
	}

	// 记录池 0 扫描的去重段数，供 Finalize 的 all-failed 检测使用。
	// pending==nil 唯一标识池 0；后续池的重切不再覆盖此值。
	if pending == nil {
		h.scannedSegments.Store(int64(len(scan)))
	}

	constraint := BatchConstraint{
		MaxSegments: h.BatchSize,
		MaxWords:    h.MaxWordsPerBatch,
	}

	// 两者都为 0 → 不分批，全部一次发送
	if constraint.MaxSegments <= 0 && constraint.MaxWords <= 0 {
		logger.Info("extract handler: no batch limit, sending all segments at once",
			"segments", len(scan))
		return [][]int{scan}, nil
	}

	batches := BuildContinuousPendingBatches(doc, scan, constraint)

	logger.Info("extract handler: batches built",
		"segments", len(scan),
		"batches", len(batches),
		"batch_size", h.BatchSize,
		"max_words_per_batch", h.MaxWordsPerBatch)

	return batches, nil
}

// ProcessBatch 处理单个抽取批次。
// 从索引取文本 → collectExisting → render → call LLM → parse → glossary.Add。
// 错误路由（按两层错误路由表）：
//   - 渲染失败 → unresolved（进下一池重切）
//   - 致命 401/403 → fatalUnresolved（跳池+跨轮传播）
//   - 可重试 429/503/网络 → retry（同批退避重试）
//   - 解析失败 → unresolved（进下一池重切）
//   - 全 backend 失败（非致命）→ unresolved（进下一池重试）
//   - 成功 → 空 batchResult（resolved 由 executor 统计）
func (h *ExtractHandler) ProcessBatch(ctx context.Context, doc *Document, idxs []int, attempt int, logger *slog.Logger) batchResult {
	start := time.Now()

	// 从索引取文本（优先用 OriginalSource）
	texts := make([]string, 0, len(idxs))
	for _, idx := range idxs {
		seg := &doc.Segments[idx]
		t := seg.OriginalSource
		if t == "" {
			t = seg.Source
		}
		texts = append(texts, t)
	}

	existing := h.collectExisting(ctx, texts, doc, logger)

	proto := prompt.ProtocolFromResponseMode(h.ResponseMode)
	isTextMode := proto.IsText()
	sys, usr, err := h.Renderer.Render(prompt.BootstrapData{
		SourceLang: doc.SourceLang,
		TargetLang: doc.TargetLang,
		Texts:      texts,
		Existing:   existing,
		MaxTerms:   h.calcMaxTerms(texts),
		Protocol:   proto,
	})
	if err != nil {
		logger.Warn("extract render failed", "err", err)
		h.emitBatchOutcome(progress.BatchEvent{
			Stage:        "extract",
			SegmentIDs:   segmentIDStringsFromDoc(doc, idxs),
			SegmentCount: len(idxs),
			Status:       "failed",
			DurationMs:   time.Since(start).Milliseconds(),
			ErrorType:    "render_error",
			ErrorMessage: err.Error(),
			RoundIndex:   h.RoundIndex,
		})
		return batchResult{unresolved: idxs}
	}

	// 非 text：只挂 JSONSchema，不强制 ResponseFormat，由 backend 默认决定是否用 schema。
	// text：强制 ResponseFormat=none，不挂 schema。
	req := backend.Request{
		System: sys,
		User:   usr,
	}
	if isTextMode {
		req.ResponseFormat = "none"
	} else {
		req.JSONSchema = prompt.BootstrapSchema()
	}

	// 单后端：直接调用 b.Translate（不再用 backend.WithRetry 包裹）。
	// 在途重试由 executor 的 retry 通道统一处理。
	var lastErr error
	for _, b := range h.Backends {
		callStart := time.Now()
		resp, callErr := b.Translate(ctx, req)
		if callErr != nil {
			// 致命 401/403：跳池（同 backend 重试无意义），跨轮传播换 backend。
			if isFatalBackendError(callErr) {
				logger.Warn("extract backend fatal error, deferring to cross-round",
					"backend", b.Name(), "err", callErr)
				h.emitBatchOutcome(backendErrorBatchEvent("extract", doc, idxs, b.Name(), nil, callErr, attempt, h.RoundIndex, time.Since(callStart).Milliseconds(), sys, usr, req))
				return batchResult{fatalUnresolved: idxs}
			}

			// 可重试 429/503/网络：退避后同批重试。
			if backend.IsRetryable(callErr) {
				logger.Warn("extract backend retryable error, will backoff and retry",
					"backend", b.Name(), "err", callErr)
				h.emitBatchOutcome(backendErrorBatchEvent("extract", doc, idxs, b.Name(), nil, callErr, attempt, h.RoundIndex, time.Since(callStart).Milliseconds(), sys, usr, req))
				wait := backoffDuration(attempt, h.Retry, callErr)
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					// 段保持未解决：不计数（executor 对 unresolved 段不推进度）——
					// 未解决段计入 segment_completed 后，恢复/重试重跑会二次计数。
					return batchResult{unresolved: idxs}
				case <-h.Gate.Done():
					// 暂停时中止退避等待：段保持未解决，由断点集合覆盖。
					timer.Stop()
					return batchResult{unresolved: idxs}
				case <-timer.C:
				}
				return batchResult{retry: &batchJob{idxs: idxs, attempt: attempt + 1}}
			}

			// 其余非致命 backend 错误：换下一个 backend（若有）或进下一池重试。
			logger.Warn("extract LLM call failed", "backend", b.Name(), "err", callErr)
			lastErr = callErr
			continue
		}

		if resp.Truncated {
			logTruncatedResponse(logger, b.Name())
		}

		parsed, parseRepaired, perr := repair.ParseBootstrapByMode(resp.Text, isTextMode, h.Repair, false)
		if perr != nil {
			logger.Warn("extract parse failed",
				"backend", b.Name(), "err", perr,
				"resp_len", len(resp.Text), "resp_head", headSnippet(resp.Text, 200),
				"repaired", parseRepaired)
			lastErr = perr
			continue
		}
		if len(parseRepaired) > 0 {
			logger.Info("extract response repaired",
				"backend", b.Name(), "ops", parseRepaired)
		}

		candidates := make([]glossary.Entry, 0, len(parsed))
		for _, e := range parsed {
			if len([]rune(e.Source)) < h.MinSourceLen {
				continue
			}
			candidates = append(candidates, glossary.Entry{
				Source:    e.Source,
				Target:    e.Target,
				Mandatory: false,
				Notes:     e.Notes,
			})
		}
		res, addErr := h.Glossary.Add(ctx, candidates...)
		if addErr != nil {
			logger.Warn("glossary add failed", "err", addErr)
		}
		added := len(res.Added)
		logger.Debug("extract batch ok",
			"backend", b.Name(),
			"batch_segments", len(texts),
			"parsed", len(parsed),
			"added", added,
			"skipped", len(res.Skipped),
			"prompt_tokens", resp.Usage.PromptTokens,
			"completion_tokens", resp.Usage.CompletionTokens)
		atomic.AddInt64(&doc.InputTokens, resp.Usage.PromptTokens)
		atomic.AddInt64(&doc.OutputTokens, resp.Usage.CompletionTokens)

		status := "success"
		if addErr != nil || len(res.Skipped) > 0 {
			status = "partial"
		}
		h.emitBatchOutcome(progress.BatchEvent{
			Stage:           "extract",
			SegmentIDs:      segmentIDStringsFromDoc(doc, idxs),
			SegmentCount:    len(idxs),
			BackendName:     b.Name(),
			Status:          status,
			DurationMs:      time.Since(start).Milliseconds(),
			InputTokens:     resp.Usage.PromptTokens,
			OutputTokens:    resp.Usage.CompletionTokens,
			SentContent:     usr,
			ReceivedContent: resp.Text,
			AddedGlossary:   toBootstrapEntries(res.Added),
			Truncated:       resp.Truncated,
			Repaired:        parseRepaired,
			RoundIndex:      h.RoundIndex,
			SystemPrompt:    sys,
			UserMessage:     usr,
			ResponseFormat:  req.ResponseFormat,
			JSONSchema:      req.JSONSchema,
			ResponseContent: resp.Text,
		})

		return batchResult{}
	}

	if lastErr != nil {
		logger.Warn("extract batch failed (all backends exhausted)", "err", lastErr)
	}
	h.emitBatchOutcome(progress.BatchEvent{
		Stage:          "extract",
		SegmentIDs:     segmentIDStringsFromDoc(doc, idxs),
		SegmentCount:   len(idxs),
		Status:         "failed",
		DurationMs:     time.Since(start).Milliseconds(),
		ErrorType:      "backend_error",
		ErrorMessage:   lastErr.Error(),
		RoundIndex:     h.RoundIndex,
		SystemPrompt:   sys,
		UserMessage:    usr,
		ResponseFormat: req.ResponseFormat,
		JSONSchema:     req.JSONSchema,
	})
	return batchResult{unresolved: idxs}
}

// collectExisting 把所有 texts 上的 Lookup 命中合并去重，作为 existing 提示给 LLM。
func (h *ExtractHandler) collectExisting(ctx context.Context, texts []string, doc *Document, logger *slog.Logger) []string {
	if h.Glossary == nil {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	for _, t := range texts {
		hits, err := h.Glossary.Lookup(ctx, t, doc.SourceLang, doc.TargetLang)
		if err != nil {
			logger.Warn("glossary lookup failed during extract", "err", err)
			continue
		}
		for _, hit := range hits {
			if hit.Forbidden {
				continue
			}
			if _, dup := seen[hit.Source]; dup {
				continue
			}
			seen[hit.Source] = struct{}{}
			out = append(out, hit.Source)
		}
	}
	return out
}

// calcMaxTerms 基于文本字词数动态计算本批最大术语抽取数。
func (h *ExtractHandler) calcMaxTerms(texts []string) int {
	coeff := h.MaxTermsPer1000Chars
	if coeff <= 0 {
		coeff = 25.0
	}
	totalWords := 0
	for _, t := range texts {
		totalWords += CountWords(t)
	}
	maxTerms := int(math.Ceil(float64(totalWords) / 1000.0 * coeff))
	return max(maxTerms, 1)
}

// toBootstrapEntries 将 glossary.Entry 转换为 prompt.BootstrapEntry。
func toBootstrapEntries(entries []glossary.Entry) []prompt.BootstrapEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]prompt.BootstrapEntry, len(entries))
	for i, e := range entries {
		out[i] = prompt.BootstrapEntry{
			Source: e.Source,
			Target: e.Target,
			Notes:  e.Notes,
		}
	}
	return out
}
