package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/protect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

// ReviseHandler 对带待处理语义问题的译文做 LLM 修订。
// 正式执行器先暂存候选结果，再由轮次存储确认译文。
//
// 输入保护与 translate 轮同构：修订 prompt 中的 target 为占位符化、注音剥离形态，
// LLM 返回后经占位符完整性校验 → 还原 → 注音对齐还原，最终译文才进入 callback。
// 保护/还原全部在批内局部状态完成，doc 的 Source/Target/Protected/Meta 全程不被
// 污染（worker 的 CAS 写回以 doc 原始 Target 为 baseline）。
type ReviseHandler struct {
	RubyBatch        AlignmentBatchConfig
	Backend          backend.Backend
	Renderer         *prompt.ReviseRenderer
	BatchSize        int
	MaxWordsPerBatch int
	// MaxBatchIndexSpan 同批段落文档索引跨度上限（max-min）；<=0 不限制（默认）。
	MaxBatchIndexSpan int
	Retry             backend.RetryPolicy
	ResponseMode      string
	Repair            repair.Options
	IssueCodes        []string
	Reporter          progress.Reporter
	Logger            *slog.Logger

	RoundIndex          int // execution plan round index, set by caller
	buildBounds         *backend.BuildBounds
	RubyProtocolVersion int

	// Protector/Ruby* 与 TranslateHandler 同名同语义（protect 规则与 ruby 配置
	// 取计划级策略快照，由 worker 引擎工厂从 JobExecutionSnapshot.Strategy 统一
	// 注入）；零值时降级为原文直发（无策略的计划）。
	Protector         protect.Protector
	RubyEnabled       bool
	RubyPreserveKinds []string
	RubyMode          string
	RubyRetryBackends []backend.Backend
	RubyTemplates     prompt.RubyTemplates
	RubyRetryAttempts int // 已解析的注音对齐重试轮数；0 表示禁用。
}

func (h *ReviseHandler) ModeName() string { return RoundModeRevise }

func (*ReviseHandler) stagedRound() {}

func (h *ReviseHandler) Finalize(_ context.Context, _ *Document, _ []int) error { return nil }

func (h *ReviseHandler) logger() *slog.Logger {
	if h.Logger == nil {
		return slog.Default()
	}
	return h.Logger
}

func (h *ReviseHandler) issueCodeSet() map[string]struct{} {
	codes := h.IssueCodes
	set := make(map[string]struct{}, len(codes))
	for _, code := range codes {
		set[code] = struct{}{}
	}
	return set
}

func reviseSegmentInScope(seg Segment, codes map[string]struct{}) bool {
	for _, issue := range seg.Issues {
		if issue.IsPending() {
			if _, ok := codes[issue.Code]; ok {
				return true
			}
		}
	}
	return false
}

// reviseProtectState 是单段在修订批内的保护上下文（批内局部状态，不落 doc）。
type reviseProtectState struct {
	mapping   map[string]string // target 侧「占位符 → 原片段」映射
	rubyItems []ruby.Item       // 从 target 提取的注音条目（尚无 LLM 回填）
}

// buildProtectedInputs 构建修订 prompt 的输入段：target 经注音剥离 + 占位符化，
// source 仅剥离注音标签（source 仅供参考无守恒约束，占位符化反而会引入"LLM 拷贝
// source 占位符进 target"的 invented 误判风险），snippet 同步映射到保护形态以保
// 证在 prompt 内可定位。全部变换只落在返回的局部状态上，doc 不被修改。
func (h *ReviseHandler) buildProtectedInputs(
	doc *Document,
	idxs []int,
	codes map[string]struct{},
) ([]prompt.ReviseSegment, []reviseProtectState, map[string][]prompt.RubyAnnotation, error) {
	b, err := candidateBounds(h.buildBounds)
	if err != nil {
		return nil, nil, nil, err
	}
	if err = backend.ReserveSlice[prompt.ReviseSegment](b, int64(len(idxs))); err != nil {
		return nil, nil, nil, err
	}
	if err = backend.ReserveSlice[reviseProtectState](b, int64(len(idxs))); err != nil {
		return nil, nil, nil, err
	}
	segments := make([]prompt.ReviseSegment, 0, len(idxs))
	states := make([]reviseProtectState, len(idxs))
	var rubyAnns map[string][]prompt.RubyAnnotation
	for k, idx := range idxs {
		seg := &doc.Segments[idx]
		st := &states[k]

		target := seg.Target
		if h.RubyEnabled {
			var items []ruby.Item
			var extractErr error
			items, target, extractErr = ruby.ExtractBounded(b, target)
			if extractErr != nil {
				return nil, nil, nil, extractErr
			}
			items = slices.DeleteFunc(items, func(item ruby.Item) bool { return !item.Restorable() })
			if len(items) > 0 {
				st.rubyItems = items
				if err := backend.ReserveSlice[prompt.RubyAnnotation](b, int64(len(items))); err != nil {
					return nil, nil, nil, err
				}
				anns := make([]prompt.RubyAnnotation, len(items))
				for i, it := range items {
					anns[i] = prompt.RubyAnnotation{ID: it.ID, Base: it.SourceBase, Text: it.SourceText}
				}
				if rubyAnns == nil {
					rubyAnns = make(map[string][]prompt.RubyAnnotation)
				}
				rubyAnns[seg.ID] = anns
			}
		}

		protectedTarget, mapping, err := protect.ProtectTextBounded(b, h.Protector, target, b.OutputLimit())
		if err != nil {
			return nil, nil, nil, fmt.Errorf("protect target segment %d: %w", idx, err)
		}
		st.mapping = mapping

		source := seg.Source
		if h.RubyEnabled {
			var stripErr error
			source, stripErr = ruby.StripRubyTextBounded(b, source)
			if stripErr != nil {
				return nil, nil, nil, stripErr
			}
		}

		issues := make([]prompt.ReviseIssue, 0)
		for _, issue := range seg.Issues {
			if !issue.IsPending() {
				continue
			}
			if _, ok := codes[issue.Code]; !ok {
				continue
			}
			snippet := ""
			if issue.Span != nil {
				var snippetErr error
				snippet, snippetErr = mapSnippetToProtectedFormBounded(b, issue.Span.MatchedText, mapping)
				if snippetErr != nil {
					return nil, nil, nil, snippetErr
				}
			}
			issues, err = backend.AppendNode(b, issues, prompt.ReviseIssue{Code: issue.Code, Message: issue.Message, Snippet: snippet})
			if err != nil {
				return nil, nil, nil, err
			}
		}
		segments = append(segments, prompt.ReviseSegment{ID: seg.ID, Source: source, Target: protectedTarget, Issues: issues})
	}
	return segments, states, rubyAnns, nil
}

// BuildBatches 选 Translate 标记、status∈{translated,edited}、Target 非空且含待修订
// 语义问题的段，按约束分批。pending==nil 时自行扫描 doc 并排除跨轮已解决段。
func (h *ReviseHandler) BuildBatches(_ context.Context, doc *Document, pending []int, _ int) ([][]int, error) {
	logger := h.logger()
	if h.Renderer == nil {
		logger.Warn("revise handler: renderer is nil, skipping")
		return nil, nil
	}
	if h.Backend == nil {
		logger.Warn("revise handler: backend is nil, skipping")
		return nil, nil
	}

	codes := h.issueCodeSet()
	var scan []int
	if pending != nil {
		// 池>0：仅处理给定 unresolved，避免全量重扫。
		scan = pending
	} else {
		for i := range doc.Segments {
			seg := &doc.Segments[i]
			if !seg.Translate || (seg.Status != "translated" && seg.Status != "edited") || seg.Target == "" {
				continue
			}
			if !reviseSegmentInScope(*seg, codes) {
				continue
			}
			if doc.ResolvedIndices != nil {
				if _, ok := doc.ResolvedIndices[i]; ok {
					continue
				}
			}
			scan = append(scan, i)
		}
	}
	if len(scan) == 0 {
		logger.Info("revise handler: no segments with pending issues")
		return nil, nil
	}

	constraint := BatchConstraint{
		MaxSegments: h.BatchSize,
		MaxWords:    h.MaxWordsPerBatch,
		WordCount: func(seg Segment) int {
			return CountWords(seg.Source) + CountWords(seg.Target)
		},
	}
	if constraint.MaxSegments <= 0 && constraint.MaxWords <= 0 && h.MaxBatchIndexSpan <= 0 {
		logger.Info("revise handler: no batch limit, sending all segments at once", "segments", len(scan))
		return [][]int{scan}, nil
	}
	batches := BuildPackedPendingBatches(doc, scan, constraint, h.MaxBatchIndexSpan)
	logger.Info("revise handler: batches built", "segments", len(scan), "batches", len(batches),
		"batch_size", h.BatchSize, "max_words_per_batch", h.MaxWordsPerBatch,
		"max_batch_index_span", h.MaxBatchIndexSpan)
	return batches, nil
}
