package pipeline

import (
	"context"
	"log/slog"
	"math"
	"strings"
	"sync/atomic"

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
	MaxTermsPer1000Chars float64
	MinSourceLen         int
	Repair               repair.Options

	Logger   *slog.Logger
	Reporter progress.Reporter
}

func (h *ExtractHandler) ModeName() string { return "extract" }

func (h *ExtractHandler) Finalize(_ context.Context, _ *Document, _ []int) error { return nil }

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

// BuildBatches 收集待抽取的段落索引，按 BatchSize 分批。
// 跳过 Skip 和空白段落。不扩展上下文。
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

	bs := max(h.BatchSize, 1)
	var batches [][]int
	for i := 0; i < len(pending); i += bs {
		end := min(i+bs, len(pending))
		batches = append(batches, pending[i:end])
	}

	logger.Info("extract handler: batches built",
		"segments", len(pending),
		"batches", len(batches),
		"batch_size", bs)

	return batches, nil
}

// ProcessBatch 处理单个抽取批次。
// 从索引取文本 → collectExisting → render → call LLM → parse → glossary.Add。
// 失败时返回空 batchResult（尽力而为，不阻断）。
func (h *ExtractHandler) ProcessBatch(ctx context.Context, doc *Document, idxs []int, _ int, logger *slog.Logger) batchResult {
	rep := h.reporter()

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

	sys, usr, err := h.Renderer.Render(prompt.BootstrapData{
		SourceLang: doc.SourceLang,
		TargetLang: doc.TargetLang,
		Texts:      texts,
		Existing:   existing,
		MaxTerms:   h.calcMaxTerms(texts),
	})
	if err != nil {
		logger.Warn("extract render failed", "err", err)
		rep.SegmentDone()
		return batchResult{}
	}

	req := backend.Request{
		System:         sys,
		User:           usr,
		ResponseFormat: "json_schema",
		JSONSchema:     prompt.BootstrapSchema(),
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

		parsed, parseRepaired, perr := repair.TryRepairBootstrap(resp.Text, h.Repair)
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
		rep.SegmentDone()
		return batchResult{}
	}

	if lastErr != nil {
		logger.Warn("extract batch failed (all backends exhausted)", "err", lastErr)
	}
	rep.SegmentDone()
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
