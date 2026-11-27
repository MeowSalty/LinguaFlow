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

func runStoredHandlerCPU(ctx context.Context, fn func() error) error {
	if runtime := ExecutionRuntimeFromContext(ctx); runtime != nil {
		return runtime.Run(ctx, fn)
	}
	return backend.RunBuild(ctx, fn)
}

type storedBatchDiagnostic struct {
	event  progress.BatchEvent
	bounds *backend.BuildBounds
}

func (d *storedBatchDiagnostic) close() {
	d.event = progress.BatchEvent{}
	d.bounds.CloseRetired()
	d.bounds = nil
}

func (d *storedBatchDiagnostic) captureRequest(ctx context.Context, reporter progress.Reporter, req backend.Request) {
	if ExecutionRuntimeFromContext(ctx) == nil {
		return
	}
	if _, ok := reporter.(progress.BatchObserver); !ok {
		return
	}
	d.event.SystemPromptLength, d.event.SentLength = len(req.System), len(req.User)
	// Diagnostics are best effort and carry their own finite copies. A preview
	// allocation failure must leave the request outcome and its accounting intact.
	_ = runStoredHandlerCPU(ctx, func() error {
		b, err := backend.NewBuildBoundsFromContext(ctx, 0, 0)
		if err != nil {
			return err
		}
		d.bounds = b
		d.event.SystemPrompt, err = cloneBatchContent(b, req.System)
		if err != nil {
			return err
		}
		d.event.UserMessage, err = cloneBatchContent(b, req.User)
		if err != nil {
			return err
		}
		return b.CloneInto(req.JSONSchema, &d.event.JSONSchema)
	})
}

func (d *storedBatchDiagnostic) captureResponse(ctx context.Context, req *backend.Request, resp *backend.Response, system, user *string) {
	if ExecutionRuntimeFromContext(ctx) == nil {
		return
	}
	req.System, req.User = "", ""
	*system, *user = "", ""
	if resp == nil || d.bounds == nil {
		return
	}
	d.event.ReceivedLength = len(resp.Text)
	_ = runStoredHandlerCPU(ctx, func() error {
		var err error
		d.event.ResponseContent, err = cloneBatchContent(d.bounds, resp.Text)
		return err
	})
}

func (d *storedBatchDiagnostic) apply(ctx context.Context, evt progress.BatchEvent) progress.BatchEvent {
	if ExecutionRuntimeFromContext(ctx) == nil {
		return evt
	}
	evt.SystemPrompt = d.event.SystemPrompt
	evt.SentContent, evt.UserMessage = d.event.UserMessage, d.event.UserMessage
	evt.ReceivedContent, evt.ResponseContent = d.event.ResponseContent, d.event.ResponseContent
	evt.SystemPromptLength, evt.SentLength, evt.ReceivedLength = d.event.SystemPromptLength, d.event.SentLength, d.event.ReceivedLength
	evt.JSONSchema = d.event.JSONSchema
	if session, ok := ctx.Value(requestSessionKey{}).(*requestSession); ok {
		pool := session.intent.Pool
		evt.PoolIndex = &pool
		evt.ParentRequestID = session.lastID
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
		b, err := backend.NewBuildBoundsFromContext(ctx, output, storedHandlerInputLimit(ctx))
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
		if e := backend.ReserveSlice[int](b, int64(count)); e != nil {
			return e
		}
		if e := backend.ReserveSlice[[]int](b, 1); e != nil {
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
		if e := backend.ReserveSlice[[]int](b, int64(count)); e != nil {
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
		if e := backend.ReserveSlice[[]int](b, int64(count)); e != nil {
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
	Backend              backend.Backend
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
	if h.Renderer == nil || h.Backend == nil || h.Glossary == nil {
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

// ProcessBatch 构建术语抽取请求。成功的响应交由执行器从归档解析和提交。
func (h *ExtractHandler) ProcessBatch(ctx context.Context, doc *Document, idxs []int, attempt int, logger *slog.Logger) batchResult {
	var diagnostic storedBatchDiagnostic
	defer diagnostic.close()
	emit := func(evt progress.BatchEvent) { h.emitBatchOutcome(diagnostic.apply(ctx, evt)) }
	start := time.Now()

	// 从索引取文本（优先用 OriginalSource）
	var texts []string
	inputErr := runStoredHandlerBuild(ctx, func(b *backend.BuildBounds) error {
		if err := backend.ReserveSlice[string](b, int64(len(idxs))); err != nil {
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
		if storedDispatchDeferred(ctx, err) {
			return batchResult{deferred: true}
		}
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

	// text 强制 ResponseFormat=none，其他模式由后端决定是否使用 JSONSchema。
	b := h.Backend
	callStart := time.Now()
	req, err := RenderStoredRequestBounded(ctx, b, requestBase, func(bounds *backend.BuildBounds, claim func(backend.PromptMeasure) error) (string, string, error) {
		return h.Renderer.RenderWithCreditsBounded(bounds, prompt.BootstrapData{SourceLang: doc.SourceLang, TargetLang: doc.TargetLang, Texts: texts, Existing: existing, MaxTerms: h.calcMaxTerms(texts), Protocol: proto}, storedHandlerInputLimit(ctx), claim)
	})
	if err != nil {
		if storedDispatchDeferred(ctx, err) {
			return batchResult{deferred: true}
		}
		if d := workDeferred(err); d != nil {
			return batchResult{waitCapacity: d, deferred: true}
		}
		if storageFailure(err) {
			return batchResult{err: err}
		}
		if errors.Is(err, backend.ErrBuildCapacity) {
			return batchResult{failedSegments: idxs}
		}
		logger.Warn("extract render failed", "backend", b.Name(), "err", err)
		emit(progress.BatchEvent{
			Stage:          RoundModeExtract,
			SegmentIDs:     segmentIDStringsFromDoc(doc, idxs),
			SegmentCount:   len(idxs),
			Status:         "failed",
			DurationMs:     time.Since(start).Milliseconds(),
			ErrorType:      "render_error",
			ErrorMessage:   err.Error(),
			BackendName:    b.Name(),
			RoundIndex:     h.RoundIndex,
			Attempt:        attempt,
			ResponseFormat: req.ResponseFormat,
			JSONSchema:     req.JSONSchema,
		})
		return batchResult{unresolved: idxs}
	}
	sys, usr := req.System, req.User
	diagnostic.captureRequest(ctx, h.Reporter, req)
	resp, callErr := b.Translate(ctx, req)
	if callErr == nil {
		if retainStoredBatchDiagnostic(ctx, doc, idxs, h.Reporter, &diagnostic, RoundModeExtract, b.Name(), req.ResponseFormat, start) {
			return batchResult{}
		}
		return batchResult{err: errors.New("extract: provider returned without a captured response")}
	}
	diagnostic.captureResponse(ctx, &req, resp, &sys, &usr)
	emitBackendFailure := func(err error, started time.Time) {
		if observedStoredProviderFailure(ctx, b) {
			emit(backendErrorBatchEvent(RoundModeExtract, doc, idxs, b.Name(), nil, err, attempt, h.RoundIndex, time.Since(started).Milliseconds(), sys, usr, req))
		}
	}
	if storedDispatchDeferred(ctx, callErr) {
		return batchResult{deferred: true}
	}
	if storageFailure(callErr) {
		if providerErr := storedProviderFailure(ctx); providerErr != nil {
			emitBackendFailure(providerErr, callStart)
		}
		return batchResult{err: callErr}
	}
	if d := workDeferred(callErr); d != nil {
		return batchResult{waitCapacity: d, deferred: true}
	}
	// 致命 401/403 跳过剩余池，供后续轮次处理。
	if isFatalBackendError(callErr) {
		logger.Warn("extract backend fatal error, deferring to cross-round", "backend", b.Name(), "err", callErr)
		emitBackendFailure(callErr, callStart)
		return batchResult{fatalUnresolved: idxs}
	}
	if backend.IsRetryable(callErr) {
		logger.Warn("extract backend retryable error, will backoff and retry", "backend", b.Name(), "err", callErr)
		emitBackendFailure(callErr, callStart)
		retry := storedBackoffRetry(ctx, idxs, attempt, backoffDuration(attempt, h.Retry, callErr))
		if retry == nil {
			return batchResult{err: fmt.Errorf("extract: retry requires a stored request session: %w", callErr)}
		}
		return batchResult{retry: retry}
	}

	logger.Warn("extract LLM call failed", "backend", b.Name(), "err", callErr)
	emitBackendFailure(callErr, start)
	return batchResult{unresolved: idxs}
}

// collectExistingBounded 合并并去重各段的术语命中，作为抽取提示。
func (h *ExtractHandler) collectExistingBounded(ctx context.Context, texts []string, doc *Document, logger *slog.Logger) ([]string, error) {
	if h.Glossary == nil {
		return nil, nil
	}
	var budget *backend.BuildBounds
	var out []string
	err := runStoredHandlerBuild(ctx, func(b *backend.BuildBounds) error {
		budget = b
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
				if slices.Contains(out, source) {
					return nil
				}
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
