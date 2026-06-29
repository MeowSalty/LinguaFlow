package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/protect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tm"
)

// Round 描述一轮翻译的执行配置（纯数据，无后端名称引用）。
type Round struct {
	Name           string
	Backend        backend.Backend
	BatchSize      int
	Concurrency    int
	FallbackShrink float64
	Retry          backend.RetryPolicy

	// Renderer 本轮使用的提示词渲染器。
	// nil 时回退到 Translate 级别的 Renderer。
	Renderer *prompt.Renderer

	// Repair 本轮的修复策略。
	// nil 时回退到 Translate 级别的 Repair。
	// 使用指针以区分"未设置"（nil）和"显式设为零值"（&repair.Options{}）。
	Repair *repair.Options
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
	Retry    backend.RetryPolicy

	// 以下字段保留供外部直接构造时使用，stage 内部使用 Round 级别字段。
	Concurrency    int
	BatchSize      int     // <=1 表示禁用批量
	FallbackShrink float64 // (0,1) 启用递归缩小；0 表示失败后直接降到单段

	Logger   *slog.Logger
	Reporter progress.Reporter

	// Inline 模式：翻译时同时让 LLM 抽术语。
	InlineBootstrap       bool
	MaxTermsPer1000Chars  float64 // 每 1000 字符的术语缩放系数；<=0 默认 3.0
	MinBootstrapSourceLen int     // 抽出的术语短于此值则丢弃；<=0 默认 2
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

	// RubyOutputFormat 控制 LLM 返回注音的方式：
	//   - "ruby_output"：LLM 在 ruby_output 字段返回结构化注音数据
	//   - "inline_markers"：LLM 在译文中插入 ⟦ruby:base/text⟧ 标记
	//   - ""（空）：不启用注音处理
	RubyOutputFormat string

	// PreserveKinds 控制保留哪些类型的注音（phonetic/semantic/creative）。
	// nil 时保留全部（向后兼容）；空切片表示不保留任何注音。
	PreserveKinds []string
	// RubyRetryBackends 注音对齐重试后端列表；为空时不重试。
	RubyRetryBackends []backend.Backend

	// Context 控制翻译上下文窗口。
	Context config.ContextConfig

	// BatchHandler 每批翻译完成后的回调。由 Engine 注入，Pipeline 调用。
	// 回调可能被并发调用（多个批同时完成时），实现必须并发安全。
	BatchHandler func(ctx context.Context, result BatchResult) error

	// Protector 用于翻译后立即还原占位符。
	Protector protect.Protector
	// Restorer 用于翻译后立即还原 ruby 注音。
	Restorer *protect.RubyRestorer
}

func (*Translate) Name() string { return "translate" }

// reporter 返回非 nil 的 progress.Reporter；Reporter 字段为空时回退 Nop。
func (s *Translate) reporter() progress.Reporter {
	if s.Reporter == nil {
		return progress.Nop{}
	}
	return s.Reporter
}

func (s *Translate) Run(ctx context.Context, doc *Document) error {
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

	// 1. 收集 pending（Translate=true 的段落）
	// 上下文段落（Translate=false）不在这里收集，由后续分批逻辑扩展
	var pending []int
	for i := range doc.Segments {
		seg := &doc.Segments[i]
		if seg.Skip {
			seg.Target = seg.Source
			continue
		}
		if !seg.Translate {
			continue
		}
		if strings.TrimSpace(seg.Source) == "" || isPlaceholderOnly(seg) || isDecorativeSeparator(seg) {
			seg.Target = seg.Source
			continue
		}
		pending = append(pending, i)
	}

	if len(pending) == 0 {
		return nil
	}

	// 上报进度：总数为待翻译段落数量
	rep := s.reporter()
	rep.StageStart("translate", len(pending))
	defer rep.StageDone()

	// 2. 多轮处理
	remaining := append([]int(nil), pending...)
	for ridx, round := range s.Rounds {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if len(remaining) == 0 {
			break
		}

		// 3. 上下文感知分批
		ctxWindow := max(s.Context.Before, s.Context.After)
		if !s.Context.Enabled {
			ctxWindow = 0
		}
		batches := BuildContextAwareBatches(remaining, round.BatchSize, ctxWindow, s.Context.Enabled)
		logger.Info("translate round start",
			"round", ridx+1, "name", round.Name,
			"pending", len(remaining), "batches", len(batches),
			"batch_size", round.BatchSize, "concurrency", round.Concurrency,
			"context_enabled", s.Context.Enabled, "context_window", ctxWindow)

		var (
			mu          sync.Mutex
			nextPending []int
		)
		if err := RunConcurrent(ctx, len(batches), round.Concurrency, func(ctx context.Context, bidx int) error {
			idxs := batches[bidx]

			// 构建当前批次自身的索引集合
			batchSet := make(map[int]struct{}, len(idxs))
			for _, idx := range idxs {
				batchSet[idx] = struct{}{}
			}

			// 4. 扩展上下文：为每个批次添加前后文段落
			expandedIdxs := expandBatchWithContext(doc, idxs, len(doc.Segments), ctxWindow)
			contextSet := buildContextSet(expandedIdxs, batchSet)

			// 5. 处理批次
			unresolved, err := s.processBatchInRound(ctx, doc, expandedIdxs, round, bidx, logger, contextSet)
			if err != nil {
				return err
			}

			// 6. 回调 batchHandler
			if s.BatchHandler != nil {
				batchResult := buildBatchResult(doc, expandedIdxs, bidx, contextSet)
				if herr := s.BatchHandler(ctx, batchResult); herr != nil {
					return fmt.Errorf("batch handler: %w", herr)
				}
			}

			if len(unresolved) > 0 {
				mu.Lock()
				nextPending = append(nextPending, unresolved...)
				mu.Unlock()
			}
			return nil
		}); err != nil {
			return err
		}

		sort.Ints(nextPending)
		logger.Info("translate round done",
			"round", ridx+1, "name", round.Name,
			"resolved", len(remaining)-len(nextPending),
			"pending_next", len(nextPending))
		remaining = nextPending
	}

	if len(remaining) > 0 {
		failedIndices := make([]string, 0, len(remaining))
		for _, idx := range remaining {
			failedIndices = append(failedIndices, strconv.Itoa(idx))
		}
		if doc.Vars == nil {
			doc.Vars = map[string]any{}
		}
		doc.Vars["_translate_failed_indices"] = strings.Join(failedIndices, ",")
		logger.Warn("translate plan exhausted", "count", len(remaining))
	}
	return nil
}

// BuildContinuousPendingBatches 将 pending 段索引按连续性分组，
// 每组内再按 target 大小切批。分散段落会被拆到不同 batch，
// 避免上下文断裂。
func BuildContinuousPendingBatches(pending []int, target int) [][]int {
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

// expandBatchWithContext 为批次扩展上下文段落。
// 返回扩展后的索引列表（包含上下文段落）。
// 跳过 Skip=true、纯占位符、装饰性分隔符、空内容的段落。
func expandBatchWithContext(doc *Document, idxs []int, totalSegments, ctxWindow int) []int {
	if ctxWindow <= 0 || len(idxs) == 0 {
		return idxs
	}
	firstIdx, lastIdx := idxs[0], idxs[len(idxs)-1]
	expandFrom := max(firstIdx-ctxWindow, 0)
	expandTo := min(lastIdx+ctxWindow, totalSegments-1)
	expanded := make([]int, 0, expandTo-expandFrom+1)
	for i := expandFrom; i <= expandTo; i++ {
		seg := &doc.Segments[i]
		if seg.Skip {
			continue
		}
		if isPlaceholderOnly(seg) || isDecorativeSeparator(seg) || strings.TrimSpace(seg.Source) == "" {
			continue
		}
		expanded = append(expanded, i)
	}
	return expanded
}

// buildContextSet 构建上下文段落集合：expandedIdxs 中不属于 batchSet 的段落。
// 返回的 map 仅包含上下文段落索引；batchSet 中的段落不包含在内。
func buildContextSet(expandedIdxs []int, batchSet map[int]struct{}) map[int]struct{} {
	ctxSet := make(map[int]struct{})
	for _, idx := range expandedIdxs {
		if _, inBatch := batchSet[idx]; !inBatch {
			ctxSet[idx] = struct{}{}
		}
	}
	return ctxSet
}

// buildBatchResult 构建批次结果（供 batchHandler 使用）。
func buildBatchResult(doc *Document, idxs []int, batchIndex int, contextSet map[int]struct{}) BatchResult {
	translated := make([]TranslatedSegment, 0, len(idxs))
	for _, idx := range idxs {
		seg := doc.Segments[idx]
		if isContext(contextSet, idx) {
			continue // 上下文段落不包含在结果中
		}
		source := seg.OriginalSource
		if source == "" {
			source = seg.Source
		}
		translated = append(translated, TranslatedSegment{
			Index:      idx,
			ID:         seg.ID,
			SourceText: source,
			TargetText: seg.Target,
			Failed:     seg.Target == "",
			Meta:       seg.Meta,
		})
	}
	return BatchResult{Segments: translated, BatchIndex: batchIndex}
}

// resolveRoundRenderer 返回轮次级 Renderer，nil 时回退到共享默认。
func (s *Translate) resolveRoundRenderer(round Round) *prompt.Renderer {
	if round.Renderer != nil {
		return round.Renderer
	}
	return s.Renderer
}

// resolveRoundRepair 返回轮次级 Repair，nil 时回退到共享默认。
func (s *Translate) resolveRoundRepair(round Round) repair.Options {
	if round.Repair != nil {
		return *round.Repair
	}
	return s.Repair
}

func (s *Translate) callOnce(ctx context.Context, b backend.Backend, req backend.Request, retry backend.RetryPolicy) (*backend.Response, error) {
	var resp *backend.Response
	err := backend.WithRetry(ctx, retry, func() error {
		var rerr error
		resp, rerr = b.Translate(ctx, req)
		return rerr
	})
	return resp, err
}

// calcMaxBootstrapTerms 根据系数和本批实际字符数计算 inline 术语上限。
func (s *Translate) calcMaxBootstrapTerms(segments []string) int {
	coeff := s.MaxTermsPer1000Chars
	if coeff <= 0 {
		coeff = 3.0
	}
	totalRunes := 0
	for _, seg := range segments {
		totalRunes += len([]rune(seg))
	}
	maxTerms := int(math.Ceil(float64(totalRunes) / 1000.0 * coeff))
	return max(maxTerms, 1)
}

func headSnippet(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// isDecorativeSeparator 检查段落是否仅包含装饰性/分隔符字符
// （非字母、非数字符号），没有实际的文本内容。
// 常见示例："◇ ◇ ◇ ◇"、"* * *"、"— — —"、"★ ★ ★"
func isDecorativeSeparator(seg *Segment) bool {
	text := strings.TrimSpace(seg.Source)
	if text == "" {
		return false // already handled by empty check
	}
	// Remove all whitespace
	text = strings.ReplaceAll(text, " ", "")
	text = strings.ReplaceAll(text, "\t", "")
	text = strings.ReplaceAll(text, "\n", "")
	text = strings.ReplaceAll(text, "\r", "")
	if text == "" {
		return false
	}
	// Check if all characters are non-letter, non-digit (i.e., only symbols/punctuation)
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// isPlaceholderOnly 检查段落是否仅包含占位符标记，
// 没有实际可翻译的文本内容。
func isPlaceholderOnly(seg *Segment) bool {
	if len(seg.Protected) == 0 {
		return false
	}
	text := seg.Source
	for key := range seg.Protected {
		text = strings.ReplaceAll(text, key, "")
	}
	return strings.TrimSpace(text) == ""
}

// extractRubyAnnotationsFromDoc 从 Document 中提取指定段的注音信息。
// idMap 为 idx → 批内 ID 的映射；为 nil 时使用 seg.ID。
// 返回 map[批内ID]annotations，供 prompt.Data 使用。
func extractRubyAnnotationsFromDoc(doc *Document, idxs []int, idMap map[int]string) map[string][]prompt.RubyAnnotation {
	result := make(map[string][]prompt.RubyAnnotation)
	for _, idx := range idxs {
		seg := doc.Segments[idx]
		raw, ok := seg.Meta["ruby_annotations"]
		if !ok {
			continue
		}
		annots, ok := raw.([]protect.RubyAnnotation)
		if !ok {
			continue
		}
		converted := make([]prompt.RubyAnnotation, len(annots))
		for i, a := range annots {
			converted[i] = prompt.RubyAnnotation{Base: a.Base, Text: a.Text}
		}
		if len(converted) > 0 {
			key := seg.ID
			if idMap != nil {
				if mapped, ok := idMap[idx]; ok {
					key = mapped
				}
			}
			result[key] = converted
		}
	}
	return result
}
