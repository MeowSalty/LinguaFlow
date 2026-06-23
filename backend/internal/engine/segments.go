package engine

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline/stages"
	"github.com/MeowSalty/LinguaFlow/backend/internal/protect"
)

// pipelineOptions 控制管道构建的配置选项。
type pipelineOptions struct {
	// skipSplit 为 true 时不包含 Split stage。
	// Web 场景：segments 已在上传时由 parser.Parse() 按自然分割策略产生，无需再次拆分。
	skipSplit bool
}

// buildPipeline 构建翻译管道。CLI 调用时 skipSplit=false，Web 调用时 skipSplit=true。
//
// 返回管道和 limiter 二元组。调用方必须 defer limiter.Close() 以停止 refill goroutine。
// limiter 作为局部变量，不存入 Engine 字段，避免并发场景下的竞态问题。
func (e *Engine) buildPipeline(opts pipelineOptions) (*pipeline.Pipeline, backend.RateLimiter) {
	pc := e.cfg.Pipeline

	// 构建 protector 列表：RubyProtector 从 rules 中独立出来，由 ruby.enabled 控制。
	// RubyProtector 必须在其他 protector 之前运行（剥离 ruby 标签后再处理剩余 XML）。
	var ps []protect.Protector
	if pc.Protect.Ruby.Enabled {
		ps = append(ps, &protect.RubyProtector{})
	}
	ps = append(ps, protect.FromRules(pc.Protect.Rules))
	protector := protect.Compose(ps...)

	// limiter 作为局部变量，不存入 e.limiter 字段，避免并发竞态。
	limiter := backend.NewRateLimiter(pc.Translate.RateLimitPerSec)

	retry := backend.RetryPolicy{
		MaxAttempts: pc.Translate.Retry.MaxAttempts,
		Backoff:     time.Duration(pc.Translate.Retry.BackoffMs) * time.Millisecond,
		Jitter:      pc.Translate.Retry.Jitter,
	}

	var s []pipeline.Stage
	if !opts.skipSplit && pc.Split.Enabled {
		s = append(s, stages.NewSplit(pc.Split.MaxChars))
	}
	if pc.Protect.Enabled {
		s = append(s, stages.NewProtect(protector))
	}

	bootstrapMode := e.cfg.Glossary.Bootstrap.Mode
	inlineBootstrap := e.cfg.Glossary.Enabled && bootstrapMode == config.BootstrapModeInline
	repairOpts := toRepairOptions(pc.Translate.Repair)

	if e.cfg.Glossary.Enabled && bootstrapMode == config.BootstrapModePre && e.bootstrapRenderer != nil {
		s = append(s, &stages.Bootstrap{
			Backends:         e.bootstrapBackends,
			Renderer:         e.bootstrapRenderer,
			Glossary:         e.glossary,
			Limiter:          limiter,
			Retry:            retry,
			Concurrency:      pc.Translate.Concurrency,
			BatchSize:        pc.Translate.BatchSize,
			MaxTermsPerBatch: e.cfg.Glossary.Bootstrap.MaxTermsPerBatch,
			MinSourceLen:     e.cfg.Glossary.Bootstrap.MinSourceLen,
			Logger:           e.logger,
			Reporter:         e.reporter,
			Repair:           repairOpts,
		})
	}
	s = append(s, &stages.Translate{
		Rounds:                    e.rounds,
		Renderer:                  e.renderer,
		Glossary:                  e.glossary,
		TM:                        e.tm,
		Limiter:                   limiter,
		Retry:                     retry,
		Logger:                    e.logger,
		Reporter:                  e.reporter,
		InlineBootstrap:           inlineBootstrap,
		MaxBootstrapTermsPerBatch: e.cfg.Glossary.Bootstrap.MaxTermsPerBatch,
		MinBootstrapSourceLen:     e.cfg.Glossary.Bootstrap.MinSourceLen,
		InlineConflictStrategy:    e.cfg.Glossary.Bootstrap.InlineConflictStrategy,
		Repair:                    repairOpts,
		RubyOutputFormat:          pc.Protect.Ruby.OutputFormat,
	})
	if pc.Protect.Enabled {
		s = append(s, stages.NewUnprotect(protector))
	}
	// 如果启用 ruby 注音保护，在 unprotect 之后添加 restore stage。
	if pc.Protect.Ruby.Enabled {
		restorer := protect.NewRubyRestorer(pc.Protect.Ruby.OutputFormat)
		s = append(s, stages.NewRubyRestore(restorer, e.logger))
	}
	return pipeline.New(e.logger, s...), limiter
}

// SegmentInput 表示从 DB 加载的待翻译段落。
type SegmentInput struct {
	// ID 使用 segmentIndex 的字符串形式作为稳定标识。
	// 不使用 hash.Short(sourceText)，因为 DB 主键是 segmentIndex，
	// 且 hash 在 Source 含 protect 占位符时会变化。
	ID         string         // strconv.Itoa(segmentIndex)
	SourceText string         // 原文
	Meta       map[string]any // 从 DB meta 字段反序列化的格式元数据
	TargetText string         // 目标文本（下载渲染时使用）
}

// TranslateSegmentsInput 纯翻译输入，不涉及文件 I/O。
type TranslateSegmentsInput struct {
	// Document 已解析的文档，Segments 中至少需填充 Source 字段。
	// 调用方负责将 DB 数据映射到 pipeline.Segment。
	Document *pipeline.Document

	// SegmentIndexes 非空时仅翻译这些索引对应的段落；
	// 未选段落保持原样，已有的 Target 不会被覆盖。
	SegmentIndexes []int

	// ExistingTargets 未选段落的已有译文，用于恢复。
	ExistingTargets map[int]string
}

// TranslateSegments 对已解析的 Document 执行纯翻译，不涉及解析和渲染。
//
// 使用场景：
//   - Web：从 DB 加载 segments → 构建 Document → 调用此方法 → 写回 DB
//   - 测试：直接构造 Document 进行翻译测试
func (e *Engine) TranslateSegments(ctx context.Context, input TranslateSegmentsInput) (TranslateResult, error) {
	start := time.Now()
	var result TranslateResult

	doc := input.Document
	if doc == nil {
		return result, fmt.Errorf("engine: document is nil")
	}

	if len(doc.Segments) == 0 {
		return result, nil
	}

	result.SegmentCount = len(doc.Segments)

	// 应用段落选择
	selectedSegments := selectedSegmentIndexSet(input.SegmentIndexes)
	if len(selectedSegments) > 0 {
		applySegmentSelection(doc, selectedSegments)
	}

	// 语言：输入优先，再用配置默认
	doc.SourceLang = firstNonEmpty(doc.SourceLang, e.cfg.SourceLang)
	doc.TargetLang = firstNonEmpty(doc.TargetLang, e.cfg.TargetLang)
	if doc.Vars == nil {
		doc.Vars = map[string]any{}
	}
	for k, v := range e.cfg.Prompt.Vars {
		if _, exists := doc.Vars[k]; !exists {
			doc.Vars[k] = v
		}
	}

	e.logger.Info("translate segments start",
		"segments", len(doc.Segments),
		"source_lang", doc.SourceLang,
		"target_lang", doc.TargetLang)

	// 构建翻译管道（skipSplit=true：Web 场景 segments 已分割完毕）
	pipe, limiter := e.buildPipeline(pipelineOptions{skipSplit: true})
	defer limiter.Close()
	e.logger.Info("pipeline start", "stages", stageNames(pipe.Stages()))
	if err := pipe.Run(ctx, doc); err != nil {
		return result, err
	}

	// 读取未解决段数量
	if v, ok := doc.Vars["_translate_unresolved_count"]; ok {
		if n, ok := v.(int); ok {
			result.UnresolvedCount = n
		}
	}

	// 恢复未选段落的已有译文
	if len(selectedSegments) > 0 {
		restoreUnselectedTargets(doc, selectedSegments, input.ExistingTargets)
	}

	// 回写术语表（与 TranslateWithResult 行为一致）
	e.maybeSaveGlossary(ctx)

	// 构建结果
	result.Segments = buildSegmentResults(doc.Segments, doc.Vars)

	e.logger.Info("translate segments done",
		"segments", len(doc.Segments),
		"unresolved", result.UnresolvedCount,
		"duration", time.Since(start).Round(time.Millisecond))

	return result, nil
}

// BuildDocumentFromSegments 从 DB segments 构建 pipeline.Document。
// 用于 Web 场景：翻译时从 DB 加载 segments 构建 Document，下载时从 DB + Target 构建 Document。
//
// 字段映射说明：
//   - Segment.ID      → strconv.Itoa(segmentIndex)，与 DB 记录对应
//   - Segment.Source   → seg.SourceText
//   - Segment.OriginalSource → seg.SourceText（从 DB 加载时 Source 就是原文，无 protect 变换）
//   - Segment.Meta     → seg.Meta（从 DB meta 字段反序列化，上传时序列化存入）
//   - Segment.Target   → 翻译前为空；下载渲染时填入 DB 中的 TargetText
func BuildDocumentFromSegments(
	segments []SegmentInput,
	sourceLang, targetLang string,
	resourceFormat string,
) *pipeline.Document {
	doc := &pipeline.Document{
		SourceLang: sourceLang,
		TargetLang: targetLang,
		Format:     resourceFormat,
		Segments:   make([]pipeline.Segment, len(segments)),
		Vars:       map[string]any{},
	}
	for i, seg := range segments {
		doc.Segments[i] = pipeline.Segment{
			ID:             seg.ID,
			Source:         seg.SourceText,
			OriginalSource: seg.SourceText,
			Meta:           seg.Meta,
			Target:         seg.TargetText,
		}
	}
	return doc
}

// buildSegmentResults 从 segments 构建结果列表。
// vars 用于解析失败索引集合（_translate_failed_indices）。
func buildSegmentResults(segments []pipeline.Segment, vars map[string]any) []SegmentResult {
	failedSet := parseFailedIndices(vars)

	results := make([]SegmentResult, 0, len(segments))
	for i, seg := range segments {
		source := seg.OriginalSource
		if source == "" {
			source = seg.Source
		}
		_, isFailed := failedSet[i]
		results = append(results, SegmentResult{
			Index:      i,
			SourceText: source,
			TargetText: seg.Target,
			Failed:     isFailed,
		})
	}
	return results
}

// parseFailedIndices 从 doc.Vars 解析失败索引。
func parseFailedIndices(vars map[string]any) map[int]struct{} {
	failedSet := make(map[int]struct{})
	if vars == nil {
		return failedSet
	}
	if v, ok := vars["_translate_failed_indices"]; ok {
		if s, ok := v.(string); ok && s != "" {
			for _, idxStr := range strings.Split(s, ",") {
				if idx, err := strconv.Atoi(strings.TrimSpace(idxStr)); err == nil {
					failedSet[idx] = struct{}{}
				}
			}
		}
	}
	return failedSet
}
