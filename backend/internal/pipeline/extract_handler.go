package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strconv"
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

	totalBatches  atomic.Int64
	failedBatches atomic.Int64
}

func (h *ExtractHandler) ModeName() string { return "extract" }

func (h *ExtractHandler) Finalize(_ context.Context, _ *Document, _ []int) error {
	total := h.totalBatches.Load()
	failed := h.failedBatches.Load()
	if total > 0 && failed == total {
		return fmt.Errorf("extract: all %d batch(es) failed", total)
	}
	return nil
}

func (h *ExtractHandler) logger() *slog.Logger {
	if h.Logger == nil {
		return slog.Default()
	}
	return h.Logger
}

func (h *ExtractHandler) reporter() progress.Reporter {
	if h.Reporter == nil {
		return progress.Nop{}
	}
	return h.Reporter
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
func (h *ExtractHandler) BuildBatches(_ context.Context, doc *Document) ([][]int, error) {
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

	var pending []int
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
		pending = append(pending, i)
	}

	if len(pending) == 0 {
		logger.Info("extract handler: no text to scan")
		return nil, nil
	}

	constraint := BatchConstraint{
		MaxSegments: h.BatchSize,
		MaxWords:    h.MaxWordsPerBatch,
	}

	// 两者都为 0 → 不分批，全部一次发送
	if constraint.MaxSegments <= 0 && constraint.MaxWords <= 0 {
		logger.Info("extract handler: no batch limit, sending all segments at once",
			"segments", len(pending))
		return [][]int{pending}, nil
	}

	batches := BuildContinuousPendingBatches(doc, pending, constraint)

	logger.Info("extract handler: batches built",
		"segments", len(pending),
		"batches", len(batches),
		"batch_size", h.BatchSize,
		"max_words_per_batch", h.MaxWordsPerBatch)

	return batches, nil
}

// ProcessBatch 处理单个抽取批次。
// 从索引取文本 → collectExisting → render → call LLM → parse → glossary.Add。
// 失败时返回空 batchResult（尽力而为，不阻断）。
func (h *ExtractHandler) ProcessBatch(ctx context.Context, doc *Document, idxs []int, _ int, logger *slog.Logger) batchResult {
	h.totalBatches.Add(1)
	rep := h.reporter()
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
			SegmentIDs:   segmentIDStrings(idxs),
			SegmentCount: len(idxs),
			Status:       "failed",
			DurationMs:   time.Since(start).Milliseconds(),
			ErrorType:    "render_error",
			ErrorMessage: err.Error(),
		})
		for range idxs {
			rep.SegmentDone()
		}
		h.failedBatches.Add(1)
		return batchResult{}
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

	var lastErr error
	for _, b := range h.Backends {
		var resp *backend.Response
		callErr := backend.WithRetry(ctx, h.Retry, func() error {
			var rerr error
			resp, rerr = b.Translate(ctx, req)
			return rerr
		})
		if callErr != nil {
			logger.Warn("extract LLM call failed", "backend", b.Name(), "err", callErr)
			lastErr = callErr
			continue
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
				Source: e.Source,
				Target: e.Target,
				Notes:  e.Notes,
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
			SegmentIDs:      segmentIDStrings(idxs),
			SegmentCount:    len(idxs),
			BackendName:     b.Name(),
			Status:          status,
			DurationMs:      time.Since(start).Milliseconds(),
			InputTokens:     resp.Usage.PromptTokens,
			OutputTokens:    resp.Usage.CompletionTokens,
			ReceivedContent: resp.Text,
			AddedGlossary:   toBootstrapEntries(res.Added),
		})

		for range idxs {
			rep.SegmentDone()
		}
		return batchResult{}
	}

	if lastErr != nil {
		logger.Warn("extract batch failed (all backends exhausted)", "err", lastErr)
	}
	h.emitBatchOutcome(progress.BatchEvent{
		Stage:        "extract",
		SegmentIDs:   segmentIDStrings(idxs),
		SegmentCount: len(idxs),
		Status:       "failed",
		DurationMs:   time.Since(start).Milliseconds(),
		ErrorType:    "backend_error",
		ErrorMessage: lastErr.Error(),
	})
	for range idxs {
		rep.SegmentDone()
	}
	h.failedBatches.Add(1)
	return batchResult{}
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

// segmentIDStrings 将段落索引切片转为字符串切片。
func segmentIDStrings(idxs []int) []string {
	out := make([]string, len(idxs))
	for i, idx := range idxs {
		out[i] = strconv.Itoa(idx)
	}
	return out
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
