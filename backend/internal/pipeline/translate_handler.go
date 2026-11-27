package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/protect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tm"
)

// TranslateHandler 实现 RoundHandler，执行翻译批次处理。
type TranslateHandler struct {
	RubyBatch        AlignmentBatchConfig
	Backend          backend.Backend
	BatchSize        int
	MaxWordsPerBatch int
	FallbackShrink   float64
	Retry            backend.RetryPolicy
	ResponseMode     string

	Renderer              *prompt.Renderer
	Glossary              glossary.Glossary
	TM                    tm.TranslationMemory
	Repair                repair.Options
	RetryReminderTemplate string
	Context               ContextConfig

	Protector         protect.Protector
	RubyEnabled       bool
	RubyPreserveKinds []string
	RubyMode          string
	Postprocess       *PostprocessConfig

	RubyRetryBackends []backend.Backend
	RubyTemplates     prompt.RubyTemplates
	RubyRetryAttempts int // 已解析的注音对齐重试轮数；0 表示禁用。

	InlineBootstrap        bool
	MaxTermsPer1000Words   float64
	MinBootstrapSourceLen  int
	InlineConflictStrategy string

	Reporter progress.Reporter
	Logger   *slog.Logger

	RoundIndex          int // execution plan round index, set by caller
	buildBounds         *backend.BuildBounds
	buildClaim          func(backend.Request, backend.PromptMeasure) error
	buildMaxBytes       int64
	buildSystemSuffix   string
	RubyProtocolVersion int
}

func (h *TranslateHandler) ModeName() string { return RoundModeTranslate }

func (*TranslateHandler) stagedRound() {}

func (h *TranslateHandler) Finalize(_ context.Context, doc *Document, unresolved []int) error {
	sort.Ints(unresolved)
	if len(unresolved) > 0 {
		failedIndices := make([]string, 0, len(unresolved))
		for _, idx := range unresolved {
			failedIndices = append(failedIndices, strconv.Itoa(idx))
		}
		if doc.Vars == nil {
			doc.Vars = map[string]any{}
		}
		doc.Vars["_translate_failed_indices"] = strings.Join(failedIndices, ",")
		h.logger().Warn("translate round exhausted", "count", len(unresolved))
	} else {
		if doc.Vars != nil {
			delete(doc.Vars, "_translate_failed_indices")
		}
	}
	return nil
}

func (h *TranslateHandler) logger() *slog.Logger {
	if h.Logger == nil {
		return slog.Default()
	}
	return h.Logger
}

// BuildBatches 收集待翻译段落、执行 Protect、分批、上下文扩展。
// pending==nil：池 0，先对全部非 Skip 段做一次保护链分析（结构段标记 + 保护产物，
// 含 Translate=false 的上下文候选段），再扫描 doc；保护产物由 Protect 循环落盘，
// 保护链对每段恰好执行一次。pending!=nil：池间重切，不重扫不 Protect，复用池 0 标记。
func (h *TranslateHandler) BuildBatches(ctx context.Context, doc *Document, pending []int, poolIndex int) ([][]int, error) {
	logger := h.logger()

	if pending == nil {
		// 结构段标记 + 保护分析：必须是本分支第一条语句——先于扫描循环与下方 buildEligibleWordPrefix
		// （前缀和直接消费 isContextEligible），顺序颠倒会导致字数预估与实际发送静默偏离。
		// 判定与实际保护是同一次链执行：翻译目标段的 AnalyzeStructural 产物缓存进 analyses，
		// 下方 Protect 循环经 Apply 落盘、不重跑链；判定随本轮保护配置浮动，
		// rawSource 保证跨轮持久 doc（成功段 Source 已 key 化）与 DB 每轮重建
		// 两条路径输入均为原始文本。
		// Skip 段不标记：两个消费方（扫描循环、isContextEligible）都先被 Skip 短路，
		// 标记从不被读，零值 false 即 fail-open；也免去 ASS 头部等大 Skip 段的整链成本。
		analyses := make([]protect.Analysis, len(doc.Segments))
		for i := range doc.Segments {
			seg := &doc.Segments[i]
			if seg.Skip {
				continue
			}
			if seg.Translate {
				// 翻译目标段：保护产物留待 Protect 循环落盘。
				analyses[i] = protect.AnalyzeStructural(h.Protector, rawSource(seg))
				seg.StructuralOnly = analyses[i].Structural
				continue
			}
			// 上下文候选段（Translate=false）：上下文恒发原文、保护形态从不落盘，只取判定结论。
			seg.StructuralOnly = protect.IsStructuralOnly(h.Protector, rawSource(seg))
		}
		var scan []int
		skippedCount := 0
		for i := range doc.Segments {
			seg := &doc.Segments[i]
			if seg.Skip {
				seg.Target = seg.Source
				skippedCount++
				continue
			}
			if !seg.Translate {
				continue
			}
			// 结构段（纯占位符/纯标点/空段/占位符+标点混合）无可译文本：不送翻，
			// 原文透传，也不进入任何批次的上下文（isContextEligible 同源排除）。
			if seg.StructuralOnly {
				seg.Target = rawSource(seg)
				skippedCount++
				continue
			}
			scan = append(scan, i)
		}

		if len(scan) == 0 {
			h.writeSkippedCount(doc, skippedCount)
			return nil, nil
		}

		if h.Protector != nil {
			for _, idx := range scan {
				seg := &doc.Segments[idx]
				if seg.OriginalSource == "" {
					seg.OriginalSource = seg.Source
				}
				// 池 0 的标记分析已对同一段文本跑过同一条保护链（scan 段 Source 恒等于
				// rawSource），此处落盘缓存产物而非重跑链；分析失败与直接 Protect 失败同语义。
				if err := analyses[idx].Apply(seg); err != nil {
					return nil, fmt.Errorf("protect segment %d: %w", idx, err)
				}
			}
		}

		h.writeSkippedCount(doc, skippedCount)
		pending = scan
	}

	if len(pending) == 0 {
		return nil, nil
	}

	ctxWindow := max(h.Context.Before, h.Context.After)
	if !h.Context.Enabled {
		ctxWindow = 0
	}

	constraint := BatchConstraint{
		MaxSegments: h.BatchSize,
		MaxWords:    h.MaxWordsPerBatch,
	}
	if constraint.MaxSegments <= 0 && constraint.MaxWords <= 0 {
		constraint.MaxSegments = 1
	}
	constraint = shrinkConstraint(constraint, h.FallbackShrink, poolIndex)
	var estimator contextWordEstimator
	if h.Context.Enabled && ctxWindow > 0 && constraint.MaxWords > 0 {
		eligiblePrefix := buildEligibleWordPrefix(doc)
		estimator = func(candidate []int) int {
			return estimateContextWordsWithPrefix(doc, candidate, ctxWindow, eligiblePrefix)
		}
	}
	batches := BuildContextAwareBatches(doc, pending, constraint, ctxWindow, h.Context.Enabled, estimator)

	logger.Info("translate handler: batches built",
		"pending", len(pending), "batches", len(batches),
		"pool", poolIndex,
		"max_segments", constraint.MaxSegments, "max_words", constraint.MaxWords,
		"context_enabled", h.Context.Enabled, "context_window", ctxWindow)

	return batches, nil
}

// shrinkConstraint 按池索引缩放批次约束：floor(orig * shrink^poolIndex)，下限 clamp 到 1。
func shrinkConstraint(orig BatchConstraint, shrink float64, poolIndex int) BatchConstraint {
	if poolIndex <= 0 || shrink <= 0 || shrink >= 1 || math.IsNaN(shrink) || math.IsInf(shrink, 0) {
		return orig
	}
	factor := math.Pow(shrink, float64(poolIndex))
	out := orig
	if orig.MaxSegments > 0 {
		next := int(math.Floor(float64(orig.MaxSegments) * factor))
		if next < 1 {
			next = 1
		}
		out.MaxSegments = next
	}
	if orig.MaxWords > 0 {
		next := int(math.Floor(float64(orig.MaxWords) * factor))
		if next < 1 {
			next = 1
		}
		out.MaxWords = next
	}
	return out
}

// writeSkippedCount 将跳过计数写入 doc.Vars，保持单调递增语义。
func (h *TranslateHandler) writeSkippedCount(doc *Document, skippedCount int) {
	if doc.Vars == nil {
		doc.Vars = map[string]any{}
	}
	if prev, ok := doc.Vars["_skipped_count"].(int); ok {
		if skippedCount > prev {
			doc.Vars["_skipped_count"] = skippedCount
		}
	} else {
		doc.Vars["_skipped_count"] = skippedCount
	}
}

// ProcessBatch 处理单个翻译批次。
func (h *TranslateHandler) buildRequestWithHints(doc *Document, idxs []int, contextSet map[int]struct{}, truncatedSrc map[int]string, glos []prompt.GlossaryEntry, tmHints []prompt.TMHint) (string, string, backend.Request, []string, map[int]string, []prompt.GlossaryEntry, error) {
	renderer := h.Renderer
	proto := prompt.ProtocolFromResponseMode(h.ResponseMode)
	isTextMode := proto.IsText()

	inputs := make([]prompt.SegmentInput, len(idxs))
	idMap := make(map[int]string, len(idxs))
	var wantIDs []string
	batchSources := make([]string, 0, len(idxs))
	transIdx := 0
	for k, idx := range idxs {
		seg := doc.Segments[idx]
		// 批次段发送 seg.Source（key 化保护形态）；仅上下文段回退保护前原文。
		source := seg.Source
		isCtx := IsContext(contextSet, idx)
		if isCtx {
			source = rawSource(&seg)
			if trunc, ok := truncatedSrc[idx]; ok {
				source = trunc
			}
		}

		var id string
		if isTextMode {
			if isCtx {
				id = "*"
			} else {
				transIdx++
				id = strconv.Itoa(transIdx)
			}
		} else {
			id = strconv.Itoa(k + 1)
		}
		idMap[idx] = id
		inputs[k] = prompt.SegmentInput{ID: id, Source: source, Translate: !isCtx}
		if !isCtx {
			wantIDs = append(wantIDs, id)
			batchSources = append(batchSources, seg.Source)
		}
	}

	rubyAnns := extractRubyAnnotationsFromDoc(doc, idxs, idMap)
	data := prompt.Data{
		SourceLang:        doc.SourceLang,
		TargetLang:        doc.TargetLang,
		Segments:          inputs,
		Glossary:          glos,
		TMHints:           tmHints,
		Vars:              doc.Vars,
		InlineBootstrap:   h.InlineBootstrap,
		MaxBootstrapTerms: h.calcMaxBootstrapTerms(batchSources),
		Protocol:          proto,
		RubyAnnotations:   rubyAnns,
		RubyMode:          h.RubyMode,
	}
	b, err := candidateBounds(h.buildBounds)
	if err != nil {
		return "", "", backend.Request{}, nil, nil, nil, err
	}
	req := backend.Request{}
	if isTextMode {
		req.ResponseFormat = "none"
	} else {
		req.JSONSchema = translationsSchema(wantIDs, h.InlineBootstrap, h.RubyMode != "", h.RubyProtocolVersion)
	}
	claim := func(m backend.PromptMeasure) error {
		if h.buildClaim != nil {
			return h.buildClaim(req, m)
		}
		return nil
	}
	sys, usr, err := renderer.RenderWithSystemSuffixBounded(b, data, h.buildSystemSuffix, max(b.OutputLimit(), h.buildMaxBytes), claim)
	if err != nil {
		return "", "", backend.Request{}, nil, nil, nil, fmt.Errorf("render batch prompt (%d segs): %w", len(idxs), err)
	}

	req.System, req.User = sys, usr

	return sys, usr, req, wantIDs, idMap, glos, nil
}

// calcMaxBootstrapTerms 基于文本字词数动态计算本批最大术语抽取数。
func (h *TranslateHandler) calcMaxBootstrapTerms(segments []string) int {
	coeff := h.MaxTermsPer1000Words
	if coeff <= 0 {
		coeff = 3.0
	}
	totalWords := 0
	for _, seg := range segments {
		totalWords += CountWords(seg)
	}
	maxTerms := int(math.Ceil(float64(totalWords) / 1000.0 * coeff))
	return max(maxTerms, 1)
}

func headSnippet(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// extractRubyAnnotationsFromDoc 从文档段落中提取注音注释（带段内条目 id）。
// 只读统一结构 seg.Meta["ruby_items"]（[]ruby.Item，带 ID；提取阶段恒写入）。
func extractRubyAnnotationsFromDoc(doc *Document, idxs []int, idMap map[int]string) map[string][]prompt.RubyAnnotation {
	result := make(map[string][]prompt.RubyAnnotation)
	for _, idx := range idxs {
		seg := doc.Segments[idx]
		var converted []prompt.RubyAnnotation
		if raw, ok := seg.Meta["ruby_items"]; ok {
			if items, ok := raw.([]ruby.Item); ok {
				converted = make([]prompt.RubyAnnotation, len(items))
				for i, it := range items {
					converted[i] = prompt.RubyAnnotation{ID: it.ID, Base: it.SourceBase, Text: it.SourceText}
				}
			}
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
