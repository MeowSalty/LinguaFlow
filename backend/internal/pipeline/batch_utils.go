package pipeline

import (
	"strings"
	"unicode"
)

// ExpandBatchWithContext 为批次扩展上下文段落。
// 返回扩展后的索引列表，包含原始批次段落和上下文段落。
func ExpandBatchWithContext(doc *Document, idxs []int, totalSegments, ctxWindow int) []int {
	if ctxWindow <= 0 || len(idxs) == 0 {
		return idxs
	}
	batchSet := make(map[int]struct{}, len(idxs))
	for _, idx := range idxs {
		batchSet[idx] = struct{}{}
	}
	firstIdx, lastIdx := idxs[0], idxs[len(idxs)-1]
	expandFrom := max(firstIdx-ctxWindow, 0)
	expandTo := min(lastIdx+ctxWindow, totalSegments-1)
	expanded := make([]int, 0, expandTo-expandFrom+1)
	for i := expandFrom; i <= expandTo; i++ {
		if _, inBatch := batchSet[i]; inBatch {
			expanded = append(expanded, i)
			continue
		}
		seg := &doc.Segments[i]
		if seg.Skip {
			continue
		}
		if IsPlaceholderOnly(seg) || IsDecorativeSeparator(seg) || strings.TrimSpace(seg.Source) == "" {
			continue
		}
		expanded = append(expanded, i)
	}
	return expanded
}

// BuildContextSet 从扩展后的索引列表中构建上下文集合。
// 返回的集合只包含非批次内的上下文索引。
func BuildContextSet(expandedIdxs []int, batchSet map[int]struct{}) map[int]struct{} {
	ctxSet := make(map[int]struct{})
	for _, idx := range expandedIdxs {
		if _, inBatch := batchSet[idx]; !inBatch {
			ctxSet[idx] = struct{}{}
		}
	}
	return ctxSet
}

// BuildBatchResult 从文档段落状态构建 BatchResult，供 BatchHandler 回调使用。
// 过滤掉 contextSet 中的上下文段落，只保留需要翻译的段落。
func BuildBatchResult(doc *Document, idxs []int, contextSet map[int]struct{}) BatchResult {
	translated := make([]TranslatedSegment, 0, len(idxs))
	for _, idx := range idxs {
		seg := doc.Segments[idx]
		if IsContext(contextSet, idx) {
			continue
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
	return BatchResult{Segments: translated}
}

// FilterPendingIdxs 过滤掉上下文索引，只保留待处理的索引。
func FilterPendingIdxs(idxs []int, contextSet map[int]struct{}) []int {
	if len(contextSet) == 0 {
		return idxs
	}
	var pending []int
	for _, idx := range idxs {
		if !IsContext(contextSet, idx) {
			pending = append(pending, idx)
		}
	}
	return pending
}

// IsContext 检查 idx 是否在 contextSet 中。
func IsContext(contextSet map[int]struct{}, idx int) bool {
	if len(contextSet) == 0 {
		return false
	}
	_, ok := contextSet[idx]
	return ok
}

// CountWords 计算文本的字词数。CJK 字符每个计为一个词。
func CountWords(text string) int {
	count := 0
	inWord := false
	for _, r := range text {
		if IsCJK(r) {
			count++
			inWord = false
		} else if unicode.IsSpace(r) {
			inWord = false
		} else {
			if !inWord {
				count++
				inWord = true
			}
		}
	}
	return count
}

// IsCJK 判断字符是否为 CJK 字符。
func IsCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r)
}

// IsDecorativeSeparator 判断段落是否为装饰性分隔符（如 ◇ ◇ ◇）。
func IsDecorativeSeparator(seg *Segment) bool {
	text := strings.TrimSpace(seg.Source)
	if text == "" {
		return false
	}
	text = strings.ReplaceAll(text, " ", "")
	text = strings.ReplaceAll(text, "\t", "")
	text = strings.ReplaceAll(text, "\n", "")
	text = strings.ReplaceAll(text, "\r", "")
	if text == "" {
		return false
	}
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// IsPlaceholderOnly 判断段落是否仅包含占位符（无实际文本）。
func IsPlaceholderOnly(seg *Segment) bool {
	if len(seg.Protected) == 0 {
		return false
	}
	text := seg.Source
	for key := range seg.Protected {
		text = strings.ReplaceAll(text, key, "")
	}
	return strings.TrimSpace(text) == ""
}
