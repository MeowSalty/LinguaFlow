package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
)

func storedHandlerInputLimit(ctx context.Context) int64 {
	if runtime := ExecutionRuntimeFromContext(ctx); runtime != nil {
		return runtime.handoffLimit()
	}
	return 64 << 20
}

func storedHandlerOutputLimit(ctx context.Context) int64 {
	if runtime := ExecutionRuntimeFromContext(ctx); runtime != nil {
		return runtime.responseLimit()
	}
	return backend.DefaultMaxResponseBytes
}

func runStoredHandlerCPU(ctx context.Context, fn func() error) error {
	if runtime := ExecutionRuntimeFromContext(ctx); runtime != nil {
		return runtime.Run(ctx, fn)
	}
	return backend.RunBuild(ctx, fn)
}

func releaseStoredRequestText(ctx context.Context, req *backend.Request, system, user *string) {
	if ExecutionRuntimeFromContext(ctx) != nil {
		req.System, req.User = "", ""
		*system, *user = "", ""
	}
}

func storedProgressEvent(ctx context.Context, evt progress.BatchEvent) progress.BatchEvent {
	if ExecutionRuntimeFromContext(ctx) != nil {
		evt.SystemPrompt, evt.UserMessage, evt.SentContent, evt.ReceivedContent, evt.ResponseContent = "", "", "", "", ""
		evt.JSONSchema, evt.AddedGlossary = nil, nil
	}
	return evt
}

// Only finite CPU work runs under this permit. Network and storage stay at the
// caller, while every default stored handler uses the same R/J build limits.
func runStoredHandlerBuild(ctx context.Context, fn func(*backend.BuildBounds) error) error {
	runtime := ExecutionRuntimeFromContext(ctx)
	output := backend.DefaultMaxResponseBytes
	if runtime != nil {
		output = runtime.responseLimit()
	}
	build := func() error {
		b, err := backend.NewBuildBounds(output, storedHandlerInputLimit(ctx))
		if err != nil {
			return err
		}
		return fn(b)
	}
	if runtime != nil {
		return runtime.Run(ctx, build)
	}
	return backend.RunBuild(ctx, build)
}

// This planner allocates one exact index backing and exact outer view arrays.
// Views have capacity=len so later append cannot overwrite another batch.
// The private predicate and WordCount are fixed handler code, with no I/O.
func buildStoredScopeBounded(ctx context.Context, doc *Document, pending []int, codes []string, include func(*Segment, map[string]struct{}) bool, constraint BatchConstraint, span int, continuous bool) ([][]int, int, error) {
	var result [][]int
	count := 0
	err := runStoredHandlerBuild(ctx, func(b *backend.BuildBounds) error {
		if e := b.ReserveNodes(int64(len(codes))+4, 1024); e != nil {
			return e
		}
		codeSet := make(map[string]struct{}, len(codes))
		for _, code := range codes {
			codeSet[code] = struct{}{}
		}
		eligible := func(i int) bool {
			if doc.ResolvedIndices != nil {
				if _, done := doc.ResolvedIndices[i]; done {
					return false
				}
			}
			return include(&doc.Segments[i], codeSet)
		}
		if pending != nil {
			count = len(pending)
		} else {
			for i := range doc.Segments {
				if eligible(i) {
					count++
				}
			}
		}
		if count == 0 {
			return nil
		}
		if e := b.ReserveNodes(int64(count), 8); e != nil {
			return e
		}
		if e := b.ReserveNodes(1, 24); e != nil {
			return e
		}
		scan := make([]int, count)
		if pending != nil {
			copy(scan, pending)
		} else {
			at := 0
			for i := range doc.Segments {
				if eligible(i) {
					scan[at] = i
					at++
				}
			}
		}
		if e := b.ReserveNodes(int64(count)+1, 24); e != nil {
			return e
		}
		result = make([][]int, 0, count)
		if constraint.MaxSegments <= 0 && constraint.MaxWords <= 0 && span <= 0 {
			result = append(result, scan[:len(scan):len(scan)])
			return nil
		}
		split := func(lo, hi int, keepLast bool) []int {
			start, words := lo, 0
			for i := lo; i < hi; i++ {
				idx := scan[i]
				next := CountWords(doc.Segments[idx].Source)
				if constraint.WordCount != nil {
					next = constraint.WordCount(doc.Segments[idx])
				}
				if i > start && (constraint.exceeds(i-start, words, next) || (span > 0 && idx-scan[start] > span)) {
					result = append(result, scan[start:i:i])
					start, words = i, 0
				}
				words += next
			}
			last := scan[start:hi:hi]
			if !keepLast {
				result = append(result, last)
			}
			return last
		}
		if !continuous {
			split(0, count, false)
			return nil
		}
		if e := b.ReserveNodes(int64(count)+1, 24); e != nil {
			return e
		}
		leftovers := make([][]int, 0, count)
		start := 0
		for i := 1; i <= count; i++ {
			if i != count && scan[i] == scan[i-1]+1 {
				continue
			}
			last := split(start, i, true)
			leftovers = append(leftovers, last)
			start = i
		}
		slices.SortStableFunc(leftovers, func(a, z []int) int {
			if len(a) != len(z) {
				if len(a) > len(z) {
					return -1
				}
				return 1
			}
			if a[0] < z[0] {
				return -1
			}
			if a[0] > z[0] {
				return 1
			}
			return 0
		})
		result = append(result, leftovers...)
		return nil
	})
	return result, count, err
}

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
func (h *ExtractHandler) BuildBatches(ctx context.Context, doc *Document, pending []int, _ int) ([][]int, error) {
	if h.Renderer == nil || len(h.Backends) == 0 || h.Glossary == nil {
		return nil, nil
	}
	batches, count, err := buildStoredScopeBounded(ctx, doc, pending, nil, func(seg *Segment, _ map[string]struct{}) bool {
		if seg.Skip {
			return false
		}
		text := seg.OriginalSource
		if text == "" {
			text = seg.Source
		}
		return strings.TrimSpace(text) != ""
	}, BatchConstraint{MaxSegments: h.BatchSize, MaxWords: h.MaxWordsPerBatch}, 0, true)
	if err != nil {
		return nil, err
	}
	if pending == nil {
		h.scannedSegments.Store(int64(count))
	}
	h.logger().Info("extract handler: batches built", "segments", count, "batches", len(batches), "batch_size", h.BatchSize, "max_words_per_batch", h.MaxWordsPerBatch)
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
	emit := func(evt progress.BatchEvent) { h.emitBatchOutcome(storedProgressEvent(ctx, evt)) }
	start := time.Now()

	// 从索引取文本（优先用 OriginalSource）
	var texts []string
	inputErr := runStoredHandlerBuild(ctx, func(b *backend.BuildBounds) error {
		if err := b.ReserveNodes(int64(len(idxs))+1, 1024); err != nil {
			return err
		}
		texts = make([]string, 0, len(idxs))
		for _, idx := range idxs {
			seg := &doc.Segments[idx]
			t := seg.OriginalSource
			if t == "" {
				t = seg.Source
			}
			texts = append(texts, t)
		}
		return nil
	})
	if inputErr != nil {
		if errors.Is(inputErr, backend.ErrBuildCapacity) {
			return batchResult{failedSegments: idxs}
		}
		return batchResult{unresolved: idxs}
	}
	existing, inputErr := h.collectExistingBounded(ctx, texts, doc, logger)
	if inputErr != nil {
		if storageFailure(inputErr) {
			return batchResult{err: inputErr}
		}
		if errors.Is(inputErr, backend.ErrBuildCapacity) {
			return batchResult{failedSegments: idxs}
		}
		return batchResult{unresolved: idxs}
	}

	proto := prompt.ProtocolFromResponseMode(h.ResponseMode)
	isTextMode := proto.IsText()
	requestBase := backend.Request{}
	err := runStoredHandlerBuild(ctx, func(b *backend.BuildBounds) (err error) {
		if isTextMode {
			requestBase.ResponseFormat = "none"
			return nil
		}
		requestBase.JSONSchema, err = prompt.BootstrapSchemaBounded(b)
		return err
	})
	if err != nil {
		logger.Warn("extract render failed", "err", err)
		emit(progress.BatchEvent{
			Stage:        "extract",
			SegmentIDs:   segmentIDStringsFromDoc(doc, idxs),
			SegmentCount: len(idxs),
			Status:       "failed",
			DurationMs:   time.Since(start).Milliseconds(),
			ErrorType:    "render_error",
			ErrorMessage: err.Error(),
			RoundIndex:   h.RoundIndex,
		})
		if errors.Is(err, backend.ErrBuildCapacity) {
			return batchResult{failedSegments: idxs}
		}
		return batchResult{unresolved: idxs}
	}

	// 非 text：只挂 JSONSchema，不强制 ResponseFormat，由 backend 默认决定是否用 schema。
	// text：强制 ResponseFormat=none，不挂 schema。
	var req backend.Request
	var sys, usr string

	// 单后端：直接调用 b.Translate（不再用 backend.WithRetry 包裹）。
	// 在途重试由 executor 的 retry 通道统一处理。
	var lastErr error
	var lastBackend, lastResponse string
	for _, b := range h.Backends {
		lastBackend = b.Name()
		callStart := time.Now()
		req, err = RenderStoredRequestBounded(ctx, b, requestBase, func(bounds *backend.BuildBounds, claim func(backend.PromptMeasure) error) (string, string, error) {
			return h.Renderer.RenderWithCreditsBounded(bounds, prompt.BootstrapData{SourceLang: doc.SourceLang, TargetLang: doc.TargetLang, Texts: texts, Existing: existing, MaxTerms: h.calcMaxTerms(texts), Protocol: proto}, storedHandlerInputLimit(ctx), claim)
		})
		if err != nil {
			if d := workDeferred(err); d != nil {
				return batchResult{waitCapacity: d, deferred: true}
			}
			if storageFailure(err) {
				return batchResult{err: err}
			}
			if errors.Is(err, backend.ErrBuildCapacity) {
				return batchResult{failedSegments: idxs}
			}
			lastErr = err
			continue
		}
		sys, usr = req.System, req.User
		resp, callErr := b.Translate(ctx, req)
		releaseStoredRequestText(ctx, &req, &sys, &usr)
		if storageFailure(callErr) {
			return batchResult{err: callErr}
		}
		if resp != nil {
			lastResponse = resp.Text
		}
		if d := workDeferred(callErr); d != nil {
			return batchResult{waitCapacity: d, deferred: true}
		}
		if callErr != nil {
			// 致命 401/403：跳池（同 backend 重试无意义），跨轮传播换 backend。
			if isFatalBackendError(callErr) {
				logger.Warn("extract backend fatal error, deferring to cross-round",
					"backend", b.Name(), "err", callErr)
				emit(backendErrorBatchEvent("extract", doc, idxs, b.Name(), nil, callErr, attempt, h.RoundIndex, time.Since(callStart).Milliseconds(), sys, usr, req))
				return batchResult{fatalUnresolved: idxs}
			}

			// 可重试 429/503/网络：退避后同批重试。
			if backend.IsRetryable(callErr) {
				logger.Warn("extract backend retryable error, will backoff and retry",
					"backend", b.Name(), "err", callErr)
				emit(backendErrorBatchEvent("extract", doc, idxs, b.Name(), nil, callErr, attempt, h.RoundIndex, time.Since(callStart).Milliseconds(), sys, usr, req))
				wait := backoffDuration(attempt, h.Retry, callErr)
				if retry := storedBackoffRetry(ctx, idxs, attempt, wait); retry != nil {
					return batchResult{retry: retry}
				}
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

		var entriesPayload []byte
		var parsedCount int
		var parseRepaired []string
		perr := runStoredHandlerBuild(ctx, func(b *backend.BuildBounds) error {
			if int64(len(resp.Text)) > b.OutputLimit() {
				return backend.ErrBuildCapacity
			}
			opt := h.Repair
			opt.Bounds = b
			parsed, repaired, e := repair.ParseBootstrapByMode(resp.Text, isTextMode, opt, false)
			parseRepaired = repaired
			if e != nil {
				return e
			}
			parsedCount = len(parsed)
			if e = b.ReserveNodes(int64(len(parsed))+1, 1024); e != nil {
				return e
			}
			candidates := make([]glossary.Entry, 0, len(parsed))
			for _, entry := range parsed {
				if utf8.RuneCountInString(entry.Source) < h.MinSourceLen {
					continue
				}
				candidates = append(candidates, glossary.Entry{Source: entry.Source, Target: entry.Target, Mandatory: false, Notes: entry.Notes})
			}
			entriesPayload, e = b.EncodeJSON(candidates, b.OutputLimit())
			return e
		})
		if perr != nil {
			logger.Warn("extract parse failed",
				"backend", b.Name(), "err", perr,
				"resp_len", len(resp.Text), "resp_head", headSnippet(resp.Text, 200),
				"repaired", parseRepaired)
			lastErr = perr
			if errors.Is(perr, backend.ErrBuildCapacity) {
				return batchResult{failedSegments: idxs}
			}
			continue
		}
		if len(parseRepaired) > 0 {
			logger.Info("extract response repaired",
				"backend", b.Name(), "ops", parseRepaired)
		}

		if ExecutionRuntimeFromContext(ctx) != nil {
			resp.Text = ""
			lastResponse = ""
		}
		res, addErr := glossary.AddEncoded(ctx, h.Glossary, entriesPayload, storedHandlerOutputLimit(ctx), runStoredHandlerCPU)
		entriesPayload = nil
		if storageFailure(addErr) {
			return batchResult{err: addErr}
		}
		if addErr != nil {
			logger.Warn("glossary add failed", "err", addErr)
		}
		added := res.Added
		logger.Debug("extract batch ok",
			"backend", b.Name(),
			"batch_segments", len(texts),
			"parsed", parsedCount,
			"added", added,
			"skipped", res.Skipped,
			"prompt_tokens", resp.Usage.PromptTokens,
			"completion_tokens", resp.Usage.CompletionTokens)
		atomic.AddInt64(&doc.InputTokens, resp.Usage.PromptTokens)
		atomic.AddInt64(&doc.OutputTokens, resp.Usage.CompletionTokens)

		status := "success"
		if addErr != nil || res.Skipped > 0 {
			status = "partial"
		}
		emit(progress.BatchEvent{
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
			AddedGlossary:   nil, // Compact result keeps counts, not complete entries.
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
	emit(progress.BatchEvent{
		Stage:           "extract",
		SegmentIDs:      segmentIDStringsFromDoc(doc, idxs),
		SegmentCount:    len(idxs),
		Status:          "failed",
		DurationMs:      time.Since(start).Milliseconds(),
		ErrorType:       "backend_error",
		ErrorMessage:    fmt.Sprint(lastErr),
		BackendName:     lastBackend,
		SentContent:     usr,
		ReceivedContent: lastResponse,
		RoundIndex:      h.RoundIndex,
		SystemPrompt:    sys,
		UserMessage:     usr,
		ResponseFormat:  req.ResponseFormat,
		JSONSchema:      req.JSONSchema,
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

func (h *ExtractHandler) collectExistingBounded(ctx context.Context, texts []string, doc *Document, logger *slog.Logger) ([]string, error) {
	if h.Glossary == nil {
		return nil, nil
	}
	var budget *backend.BuildBounds
	var seen map[string]struct{}
	var out []string
	err := runStoredHandlerBuild(ctx, func(b *backend.BuildBounds) error {
		budget = b
		if e := b.ReserveNodes(1, 1024); e != nil {
			return e
		}
		seen = make(map[string]struct{})
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, text := range texts {
		var sources []string
		var hits []glossary.Entry
		var e error
		if sourceLookup, ok := h.Glossary.(glossary.SourcesLookup); ok {
			sources, e = sourceLookup.LookupSourcesBounded(ctx, text, doc.SourceLang, doc.TargetLang, storedHandlerInputLimit(ctx), runStoredHandlerCPU)
		} else {
			hits, e = h.Glossary.Lookup(ctx, text, doc.SourceLang, doc.TargetLang)
		}
		if e != nil {
			if errors.Is(e, backend.ErrBuildCapacity) || storageFailure(e) {
				return nil, e
			}
			logger.Warn("glossary lookup failed during extract", "err", e)
			continue
		}
		build := func() error {
			appendSource := func(source string) error {
				if _, ok := seen[source]; ok {
					return nil
				}
				if e := budget.ReserveNodes(1, 1024); e != nil {
					return e
				}
				if e := budget.ReserveText(int64(len(source)), int64(len(source))); e != nil {
					return e
				}
				seen[source] = struct{}{}
				var e error
				out, e = backend.AppendNode(budget, out, source)
				if e != nil {
					return e
				}
				return nil
			}
			for _, source := range sources {
				if e := appendSource(source); e != nil {
					return e
				}
			}
			for _, hit := range hits {
				if hit.Forbidden {
					continue
				}
				if e := appendSource(hit.Source); e != nil {
					return e
				}
			}
			return nil
		}
		if runtime := ExecutionRuntimeFromContext(ctx); runtime != nil {
			e = runtime.Run(ctx, build)
		} else {
			e = backend.RunBuild(ctx, build)
		}
		hits = nil
		sources = nil
		if e != nil {
			return nil, e
		}
	}
	return out, nil
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
