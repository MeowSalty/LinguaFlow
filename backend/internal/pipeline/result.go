package pipeline

import (
	"strconv"
	"strings"
)

// SegmentResult 描述单个段落的翻译结果。
type SegmentResult struct {
	Index      int
	SourceText string
	TargetText string
	Failed     bool // true 表示该段在所有轮次中均未成功翻译
}

// TranslateResult 描述一次翻译任务的总体结果。
type TranslateResult struct {
	SegmentCount    int
	Segments        []SegmentResult
	UnresolvedCount int // 所有轮次结束后仍未解决（被原文填充）的段数量
}

// TranslateResultFromDocument 从已完成翻译的 Document 中提取结果。
// 在翻译完成后调用。
func TranslateResultFromDocument(doc *Document) TranslateResult {
	var result TranslateResult
	if doc == nil {
		return result
	}
	result.SegmentCount = len(doc.Segments)
	if v, ok := doc.Vars["_translate_unresolved_count"]; ok {
		if n, ok := v.(int); ok {
			result.UnresolvedCount = n
		}
	}
	result.Segments = buildSegmentResults(doc.Segments, doc.Vars)
	return result
}

// buildSegmentResults 从 segments 构建结果列表。
// vars 用于解析失败索引集合（_translate_failed_indices）。
func buildSegmentResults(segments []Segment, vars map[string]any) []SegmentResult {
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
