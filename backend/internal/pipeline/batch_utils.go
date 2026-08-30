package pipeline

import (
	"unicode"
	"unicode/utf8"

	"github.com/MeowSalty/LinguaFlow/backend/internal/model"
)

// rawSource 返回段落的原始文本：优先 protect 之前的原文快照，未保护过时回退 Source。
// Source 在 protect 之后是含占位符的版本，任何需要"人类可读原文"的场合（上下文展示、
// 字数统计、结果落库、结构段判定）都必须走本函数。
func rawSource(seg *model.Segment) string {
	if seg.OriginalSource != "" {
		return seg.OriginalSource
	}
	return seg.Source
}

// expandedContext 是上下文扩展结果：Idxs 为扩展后索引；TruncatedSrc 记录被 max_chars 截断的上下文段文本（key=idx）。
type expandedContext struct {
	Idxs         []int
	TruncatedSrc map[int]string // idx -> 截断后文本；key 存在表示该段被截断
}

// isContextEligible 判断段落是否可作为上下文段（与 ExpandBatchWithContext 选段规则同源）。
// 契约：StructuralOnly 标记由 translate 轮 BuildBatches 池 0 对全部非 Skip 段统一计算
// （含 Translate=false 的段；Skip 段不标记——本函数先被 Skip 短路，零值即 fail-open），
// 直接调用 ExpandBatchWithContext 的调用方须先完成标记；
// 未标记（零值 false）按"有内容"处理（fail-open）。
func isContextEligible(seg *Segment) bool {
	return !seg.Skip && !seg.StructuralOnly
}

// ExpandBatchWithContext 为批次扩展上下文段落，并按 maxChars 对上下文段做 rune 截断。
// maxChars <= 0 表示不截断。批次内段（pending）永不截断。
func ExpandBatchWithContext(doc *Document, idxs []int, totalSegments, ctxWindow, maxChars int) expandedContext {
	truncated := map[int]string{}
	if ctxWindow <= 0 || len(idxs) == 0 {
		return expandedContext{Idxs: idxs, TruncatedSrc: truncated}
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
		if !isContextEligible(seg) {
			continue
		}
		if maxChars > 0 {
			src := rawSource(seg)
			if utf8.RuneCountInString(src) > maxChars {
				truncated[i] = string([]rune(src)[:maxChars]) + "…"
			}
		}
		expanded = append(expanded, i)
	}
	return expandedContext{Idxs: expanded, TruncatedSrc: truncated}
}

// estimateContextWords 预估给定候选批次（pending 索引）在 ctxWindow 下会拉入的上下文字词数。
// 直接复用 ExpandBatchWithContext 的选段结果（maxChars=0，仅统计非批次段），
// 保证选段区间与过滤规则单一来源，预估与实际发送不可偏离。
func estimateContextWords(doc *Document, batchIdxs []int, ctxWindow int) int {
	if ctxWindow <= 0 || len(batchIdxs) == 0 {
		return 0
	}
	expanded := ExpandBatchWithContext(doc, batchIdxs, len(doc.Segments), ctxWindow, 0)
	batchSet := make(map[int]struct{}, len(batchIdxs))
	for _, idx := range batchIdxs {
		batchSet[idx] = struct{}{}
	}
	words := 0
	for _, idx := range expanded.Idxs {
		if _, inBatch := batchSet[idx]; inBatch {
			continue
		}
		seg := &doc.Segments[idx]
		words += CountWords(rawSource(seg))
	}
	return words
}

// buildEligibleWordPrefix 预计算 eligible 上下文段的字数前缀和。
// prefix[i] = sum of CountWords(OriginalSource ?? Source) for eligible segs in [0, i)。
// 供 estimateContextWordsWithPrefix 做 O(1) 区间求和，避免巨大 ctxWindow 下的 O(n²) 退化。
func buildEligibleWordPrefix(doc *Document) []int {
	prefix := make([]int, len(doc.Segments)+1)
	for i := range doc.Segments {
		prefix[i+1] = prefix[i]
		if isContextEligible(&doc.Segments[i]) {
			prefix[i+1] += CountWords(rawSource(&doc.Segments[i]))
		}
	}
	return prefix
}

// estimateContextWordsWithPrefix 用前缀和数组快速预估候选批次的上下文字词数。
// 与 estimateContextWords 同源（同区间、同过滤 isContextEligible、同源文本回退），
// 但每次调用 O(batchSize) 而非 O(ctxWindow + batchSize)，与 ctxWindow 大小无关。
// 批内段一定落在 [lo, hi] 内（lo≤batchIdxs[0]、hi≥batchIdxs[-1]），扣除其字数即可。
func estimateContextWordsWithPrefix(doc *Document, batchIdxs []int, ctxWindow int, eligiblePrefix []int) int {
	if ctxWindow <= 0 || len(batchIdxs) == 0 {
		return 0
	}
	docLen := len(doc.Segments)
	lo := max(batchIdxs[0]-ctxWindow, 0)
	hi := min(batchIdxs[len(batchIdxs)-1]+ctxWindow, docLen-1)
	total := eligiblePrefix[hi+1] - eligiblePrefix[lo]
	for _, idx := range batchIdxs {
		seg := &doc.Segments[idx]
		if isContextEligible(seg) {
			total -= CountWords(rawSource(seg))
		}
	}
	return total
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
		translated = append(translated, TranslatedSegment{
			Index:      idx,
			ID:         seg.ID,
			SourceText: rawSource(&seg),
			TargetText: seg.Target,
			Failed:     seg.Target == "",
			Meta:       seg.Meta,
			Issues:     seg.Issues,
			Protected:  seg.Protected,
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
