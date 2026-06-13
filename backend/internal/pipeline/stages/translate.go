package stages

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tm"
)

// Round 描述一轮翻译的执行配置（纯数据，无后端名称引用）。
type Round struct {
	Name            string
	Backends        []backend.Backend
	BatchSize       int
	Concurrency     int
	FallbackShrink  float64
	RateLimitPerSec int
	Retry           backend.RetryPolicy
}

// Translate 对每个 Segment 调用 Backend。具备：
//   - worker pool（Concurrency）
//   - 令牌桶限速（Limiter）
//   - 指数退避重试（Retry）
//   - 批量翻译（BatchSize > 1 时把多段拼成一次 LLM 调用）
//   - 批失败时按 FallbackShrink 系数递归缩小子批并发重试（直到收敛到单段）
//   - 占位符完整性校验 + 单段补救重试
//   - 单段失败时保留原文 + warn 日志，不阻塞整体
//   - 段级进度上报（Reporter；nil 时 fallback 为 progress.Nop）
//
// 协议：user message 是 JSON envelope（见 prompt 包），模型回复 {"translations":{"<id>":"<text>"}}。
// 当 InlineBootstrap=true 时，回复同时携带 {"glossary":[{"source","target","notes"},...]}，
// 解析后立刻 Add 到运行时 Glossary；严格合并去重，已存在的 source 不会被覆盖。
type Translate struct {
	Rounds   []Round
	Renderer *prompt.Renderer
	Glossary glossary.Glossary
	TM       tm.TranslationMemory
	Limiter  backend.RateLimiter
	Retry    backend.RetryPolicy

	// 以下字段保留供外部直接构造时使用，stage 内部使用 Round 级别字段。
	Concurrency    int
	BatchSize      int     // <=1 表示禁用批量
	FallbackShrink float64 // (0,1) 启用递归缩小；0 表示失败后直接降到单段

	Logger   *slog.Logger
	Reporter progress.Reporter

	// Inline 模式：翻译时同时让 LLM 抽术语。
	InlineBootstrap           bool
	MaxBootstrapTermsPerBatch int // 给 prompt 的术语数量上限；<=0 默认 20
	MinBootstrapSourceLen     int // 抽出的术语短于此值则丢弃；<=0 默认 2
	// InlineConflictStrategy 控制并发下后到 worker 提交同 source 不同 target 时的处理：
	//   - config.InlineConflictRewriteLocal（默认）：把本批译文里的冲突 target 字面值
	//     替换为权威表中已有版本，CJK 直替、拉丁系按词边界、歧义仅 Warn 不动。
	//   - config.InlineConflictOff：完全不处理，沿用旧行为。
	// 空字符串视同 off（防止配置未透传时崩溃）。
	InlineConflictStrategy string

	// Repair 控制 LLM 响应解析失败 / 部分缺失时的主动修复行为。零值等于不修复
	// （行为与旧 strict 路径一致）；启用后，processBatchInRound 改走 lenient 解析，
	// 在 fatal / partial 时分别决定 shrink 或仅对缺失段单独重试。
	Repair repair.Options
}

func (*Translate) Name() string { return "translate" }

// reporter 返回非 nil 的 progress.Reporter；Reporter 字段为空时回退 Nop。
func (s *Translate) reporter() progress.Reporter {
	if s.Reporter == nil {
		return progress.Nop{}
	}
	return s.Reporter
}

func (s *Translate) Run(ctx context.Context, doc *pipeline.Document) error {
	logger := s.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if s.Renderer == nil {
		return errors.New("translate: renderer is nil")
	}
	if len(s.Rounds) == 0 {
		return errors.New("translate: no rounds provided")
	}

	// 先把跳过段（Skip / 空白）直接落 Target，并收集需要翻译的 idx 列表。
	var pending []int
	for i := range doc.Segments {
		seg := &doc.Segments[i]
		if seg.Skip || strings.TrimSpace(seg.Source) == "" {
			seg.Target = seg.Source
			continue
		}
		pending = append(pending, i)
	}

	rep := s.reporter()
	rep.StageStart("translate", len(pending))
	defer rep.StageDone()

	return s.runRounds(ctx, doc, pending, logger)
}

func (s *Translate) runRounds(ctx context.Context, doc *pipeline.Document, pending []int, logger *slog.Logger) error {
	remaining := append([]int(nil), pending...)
	rep := s.reporter()

	for ridx, round := range s.Rounds {
		if len(remaining) == 0 {
			break
		}
		batches := buildContinuousPendingBatches(remaining, round.BatchSize)
		logger.Info("translate round start",
			"round", ridx+1,
			"name", round.Name,
			"pending", len(remaining),
			"batches", len(batches),
			"batch_size", round.BatchSize,
			"concurrency", round.Concurrency)

		var (
			mu          sync.Mutex
			nextPending []int
		)
		if err := runConcurrent(ctx, len(batches), round.Concurrency, func(ctx context.Context, bidx int) error {
			unresolved, err := s.processBatchInRound(ctx, doc, batches[bidx], round, logger)
			if err != nil {
				return err
			}
			if len(unresolved) == 0 {
				return nil
			}
			mu.Lock()
			nextPending = append(nextPending, unresolved...)
			mu.Unlock()
			return nil
		}); err != nil {
			return err
		}

		sort.Ints(nextPending)
		logger.Info("translate round done",
			"round", ridx+1,
			"name", round.Name,
			"resolved", len(remaining)-len(nextPending),
			"pending_next", len(nextPending))
		remaining = nextPending
	}

	for _, idx := range remaining {
		doc.Segments[idx].Target = doc.Segments[idx].Source
		rep.SegmentDone()
	}
	if len(remaining) > 0 {
		logger.Warn("translate plan exhausted, keep source for unresolved segments", "count", len(remaining))
	}
	return nil
}

func buildContinuousPendingBatches(pending []int, target int) [][]int {
	if len(pending) == 0 {
		return nil
	}
	target = max(target, 1)
	runs := make([][]int, 0)
	start := 0
	for i := 1; i <= len(pending); i++ {
		if i == len(pending) || pending[i] != pending[i-1]+1 {
			run := append([]int(nil), pending[start:i]...)
			runs = append(runs, run)
			start = i
		}
	}

	batches := make([][]int, 0, len(pending))
	leftovers := make([][]int, 0, len(runs))
	for _, run := range runs {
		for len(run) >= target {
			batches = append(batches, append([]int(nil), run[:target]...))
			run = run[target:]
		}
		if len(run) > 0 {
			leftovers = append(leftovers, append([]int(nil), run...))
		}
	}
	sort.SliceStable(leftovers, func(i, j int) bool {
		if len(leftovers[i]) == len(leftovers[j]) {
			return leftovers[i][0] < leftovers[j][0]
		}
		return len(leftovers[i]) > len(leftovers[j])
	})
	batches = append(batches, leftovers...)
	return batches
}

func (s *Translate) callOnce(ctx context.Context, b backend.Backend, req backend.Request) (*backend.Response, error) {
	var resp *backend.Response
	err := backend.WithRetry(ctx, s.Retry, func() error {
		var rerr error
		resp, rerr = b.Translate(ctx, req)
		return rerr
	})
	return resp, err
}

// maxBootstrapTerms 返回传给 prompt 的 inline 术语上限；<=0 用默认 20。
func (s *Translate) maxBootstrapTerms() int {
	if s.MaxBootstrapTermsPerBatch > 0 {
		return s.MaxBootstrapTermsPerBatch
	}
	return 20
}

func headSnippet(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
