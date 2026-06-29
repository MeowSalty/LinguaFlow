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

// TranslatedSegment 描述单个段落的翻译结果（含 Meta）。
type TranslatedSegment struct {
	Index      int
	ID         string
	SourceText string
	TargetText string
	Failed     bool
	Meta       map[string]any
}

// BatchResult 描述一批翻译的结果。
type BatchResult struct {
	Segments   []TranslatedSegment
	BatchIndex int
}

// TranslateResult 描述一次翻译任务的总体结果。
type TranslateResult struct {
	SegmentCount    int
	Segments        []SegmentResult
	UnresolvedCount int   // 所有轮次结束后仍未解决（被原文填充）的段数量
	InputTokens     int64 // LLM 调用消耗的 input token 总数
	OutputTokens    int64 // LLM 调用消耗的 output token 总数
}

// ParseFailedIndices 从 doc.Vars 解析失败索引集合。
// 读取 _translate_failed_indices 键（逗号分隔的索引字符串）。
func ParseFailedIndices(vars map[string]any) map[int]struct{} {
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
