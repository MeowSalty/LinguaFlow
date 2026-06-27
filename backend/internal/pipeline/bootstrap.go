package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
)

// Bootstrap 在 translate 之前用 LLM 抽取并翻译领域术语，把增量写入运行时 Glossary。
// 整个 stage 是「尽力而为」：单批失败仅 warn，不阻断 pipeline——下游 translate 仍能
// 在没有增量术语的情况下跑完。
type Bootstrap struct {
	Backends         []backend.Backend
	Renderer         *prompt.BootstrapRenderer
	Glossary         glossary.Glossary
	Limiter          backend.RateLimiter
	Retry            backend.RetryPolicy
	Concurrency      int
	BatchSize        int
	MaxTermsPerBatch int
	MinSourceLen     int
	Logger           *slog.Logger
	Reporter         progress.Reporter

	// Repair 控制 LLM 响应解析的主动修复行为；零值等同 prompt.ParseBootstrapResponse 旧行为。
	Repair repair.Options
}

func (*Bootstrap) Name() string { return "bootstrap" }

func (s *Bootstrap) reporter() progress.Reporter {
	if s.Reporter == nil {
		return progress.Nop{}
	}
	return s.Reporter
}

func (s *Bootstrap) Run(ctx context.Context, doc *Document) error {
	logger := s.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if s.Renderer == nil {
		return errors.New("bootstrap: renderer is nil")
	}
	if len(s.Backends) == 0 {
		return errors.New("bootstrap: no backends provided")
	}
	if s.Glossary == nil {
		// 没有可写的 Glossary 等于自举无意义；安全地早退。
		logger.Warn("bootstrap: glossary is nil, skipping stage")
		return nil
	}

	// 收集源文：跳过 Skip/空白；优先用 OriginalSource（protect 之前的原文），
	// 让 LLM 看到可读上下文而非 __LF_xxxx__。
	var texts []string
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
		texts = append(texts, t)
	}
	if len(texts) == 0 {
		logger.Info("bootstrap: no text to scan")
		return nil
	}

	bs := max(s.BatchSize, 1)
	var batches [][]string
	for i := 0; i < len(texts); i += bs {
		end := min(i+bs, len(texts))
		batches = append(batches, texts[i:end])
	}

	logger.Info("bootstrap scanning",
		"segments", len(texts),
		"batches", len(batches),
		"concurrency", s.Concurrency,
		"batch_size", bs)

	rep := s.reporter()
	rep.StageStart("bootstrap", len(batches))
	defer rep.StageDone()

	var (
		mu          sync.Mutex
		totalAdded  int
		totalFailed int
	)

	err := RunConcurrent(ctx, len(batches), s.Concurrency, func(ctx context.Context, bidx int) error {
		added, berr := s.processBatch(ctx, batches[bidx], doc, logger)
		mu.Lock()
		if berr != nil {
			totalFailed++
		} else {
			totalAdded += added
		}
		mu.Unlock()
		rep.SegmentDone()
		return nil
	})
	if err != nil {
		// runConcurrent 只在 fn 返回 error 时退出；这里 fn 永远返回 nil（吞掉单批错误），
		// 因此 err 只可能是 ctx 取消。透传。
		return err
	}

	logger.Info("bootstrap done",
		"batches", len(batches),
		"failed_batches", totalFailed,
		"added", totalAdded,
		"glossary_size", s.glossarySize())
	return nil
}

// processBatch 处理一个批次：lookup existing → 渲染 → 调 LLM → 解析 → 过滤 → Add。
// 返回 (added, error)；error 仅用于诊断，调用方不据此终止 stage。
func (s *Bootstrap) processBatch(ctx context.Context, texts []string, doc *Document, logger *slog.Logger) (int, error) {
	if s.Limiter != nil {
		if err := s.Limiter.Wait(ctx); err != nil {
			return 0, err
		}
	}

	existing := s.collectExisting(ctx, texts, doc, logger)

	sys, usr, err := s.Renderer.Render(prompt.BootstrapData{
		SourceLang: doc.SourceLang,
		TargetLang: doc.TargetLang,
		Texts:      texts,
		Existing:   existing,
		MaxTerms:   s.MaxTermsPerBatch,
	})
	if err != nil {
		logger.Warn("bootstrap render failed", "err", err)
		return 0, err
	}

	req := backend.Request{
		System:         sys,
		User:           usr,
		ResponseFormat: "json_schema",
		JSONSchema:     prompt.BootstrapSchema(),
	}
	var lastErr error
	for _, b := range s.Backends {
		var resp *backend.Response
		callErr := backend.WithRetry(ctx, s.Retry, func() error {
			var rerr error
			resp, rerr = b.Translate(ctx, req)
			return rerr
		})
		if callErr != nil {
			logger.Warn("bootstrap LLM call failed", "backend", b.Name(), "err", callErr)
			lastErr = callErr
			continue
		}

		parsed, parseRepaired, perr := repair.TryRepairBootstrap(resp.Text, s.Repair)
		if perr != nil {
			logger.Warn("bootstrap parse failed",
				"backend", b.Name(), "err", perr,
				"resp_len", len(resp.Text), "resp_head", headSnippet(resp.Text, 200),
				"repaired", parseRepaired)
			lastErr = perr
			continue
		}
		if len(parseRepaired) > 0 {
			logger.Info("bootstrap response repaired",
				"backend", b.Name(), "ops", parseRepaired)
		}

		candidates := make([]glossary.Entry, 0, len(parsed))
		for _, e := range parsed {
			if len([]rune(e.Source)) < s.MinSourceLen {
				continue
			}
			candidates = append(candidates, glossary.Entry{
				Source: e.Source,
				Target: e.Target,
				Notes:  e.Notes,
			})
		}
		res, addErr := s.Glossary.Add(ctx, candidates...)
		if addErr != nil {
			logger.Warn("glossary add failed", "err", addErr)
		}
		added := len(res.Added)
		logger.Debug("bootstrap batch ok",
			"backend", b.Name(),
			"batch_segments", len(texts),
			"parsed", len(parsed),
			"added", added,
			"skipped", len(res.Skipped),
			"prompt_tokens", resp.Usage.PromptTokens,
			"completion_tokens", resp.Usage.CompletionTokens)
		atomic.AddInt64(&doc.InputTokens, resp.Usage.PromptTokens)
		atomic.AddInt64(&doc.OutputTokens, resp.Usage.CompletionTokens)
		return added, nil
	}
	return 0, lastErr
}

// collectExisting 把所有 texts 上的 Lookup 命中合并去重，作为 existing 提示给 LLM。
// 加这一步是为了避免 LLM 反复抽取已经在表里的术语，浪费 token。
func (s *Bootstrap) collectExisting(ctx context.Context, texts []string, doc *Document, logger *slog.Logger) []string {
	if s.Glossary == nil {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	for _, t := range texts {
		hits, err := s.Glossary.Lookup(ctx, t, doc.SourceLang, doc.TargetLang)
		if err != nil {
			logger.Warn("glossary lookup failed during bootstrap", "err", err)
			continue
		}
		for _, h := range hits {
			if _, dup := seen[h.Source]; dup {
				continue
			}
			seen[h.Source] = struct{}{}
			out = append(out, h.Source)
		}
	}
	return out
}

// glossarySize 报告当前术语总数，便于日志诊断。FileGlossary 暴露 Len()；其他实现返回 -1。
func (s *Bootstrap) glossarySize() int {
	type lener interface{ Len() int }
	if l, ok := s.Glossary.(lener); ok {
		return l.Len()
	}
	return -1
}
