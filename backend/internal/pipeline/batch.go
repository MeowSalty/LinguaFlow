package pipeline

import "sort"

// BatchConstraint 定义批次的大小约束。
// 两个条件同时生效（AND），任一达到上限即切批。
// MaxSegments <= 0 表示不限段落数；MaxWords <= 0 表示不限字词数。
// 两者都 <= 0 时，调用方应回退到默认 MaxSegments=1。
type BatchConstraint struct {
	MaxSegments int // 段落数上限（原 BatchSize）
	MaxWords    int // 字词数上限（0=不限制）
}

// BuildContextAwareBatches 根据上下文窗口合并重叠段落的 batch。
// enabled=false 或 ctxWindow<=0 时退化为连续分组。
func BuildContextAwareBatches(doc *Document, pending []int, constraint BatchConstraint, ctxWindow int, enabled bool) [][]int {
	if !enabled || ctxWindow <= 0 {
		return BuildContinuousPendingBatches(doc, pending, constraint)
	}
	if len(pending) == 0 {
		return nil
	}
	// 1. 按上下文覆盖范围分组
	var groups [][]int
	groupStart := pending[0] - ctxWindow
	groupEnd := pending[0] + ctxWindow

	for i := 1; i < len(pending); i++ {
		segStart := pending[i] - ctxWindow
		segEnd := pending[i] + ctxWindow
		if segStart <= groupEnd+1 {
			groupEnd = segEnd
		} else {
			groups = append(groups, filterInRange(pending, groupStart, groupEnd, i))
			groupStart = segStart
			groupEnd = segEnd
		}
	}
	groups = append(groups, filterInRange(pending, groupStart, groupEnd, len(pending)))

	// 2. 每组内按约束切分
	var batches [][]int
	for _, group := range groups {
		batches = append(batches, splitByConstraint(doc, group, constraint)...)
	}
	return batches
}

// BuildContinuousPendingBatches 将 pending 段索引按连续性分组，
// 每组内再按约束切批。分散段落会被拆到不同 batch，
// 避免上下文断裂。
func BuildContinuousPendingBatches(doc *Document, pending []int, constraint BatchConstraint) [][]int {
	if len(pending) == 0 {
		return nil
	}
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
		sub := splitByConstraint(doc, run, constraint)
		if len(sub) > 1 {
			batches = append(batches, sub[:len(sub)-1]...)
			leftovers = append(leftovers, sub[len(sub)-1])
		} else if len(sub) == 1 {
			leftovers = append(leftovers, sub[0])
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

// BuildPackedPendingBatches 按文档顺序贪心填充批次，允许索引不连续的段落同批。
// 适用于裁决等段落独立、无需连续上下文的场景。
//
// maxIndexSpan <= 0：不限制同批首尾索引跨度（默认关闭）。
// maxIndexSpan > 0：要求同批内 max(idx)-min(idx) <= maxIndexSpan，超限则切批。
// 段落数 / 字词数约束与 splitByConstraint 相同；单段超限仍独占一批。
func BuildPackedPendingBatches(doc *Document, pending []int, constraint BatchConstraint, maxIndexSpan int) [][]int {
	if len(pending) == 0 {
		return nil
	}
	if maxIndexSpan <= 0 {
		return splitByConstraint(doc, pending, constraint)
	}
	return splitByConstraintAndSpan(doc, pending, constraint, maxIndexSpan)
}

// splitByConstraint 按段落数和字词数双重约束切分一组段落索引（不必连续）。
// 超限时，触发超限的段落不加入当前批次，成为下一批次的第一段。
// 单段超限时独占一个批次（不截断），后续由 shrink 机制处理。
func splitByConstraint(doc *Document, group []int, constraint BatchConstraint) [][]int {
	return splitByConstraintAndSpan(doc, group, constraint, 0)
}

// splitByConstraintAndSpan 在 splitByConstraint 基础上可选索引跨度上限。
// maxIndexSpan <= 0 表示不限制跨度。
func splitByConstraintAndSpan(doc *Document, group []int, constraint BatchConstraint, maxIndexSpan int) [][]int {
	if len(group) == 0 {
		return nil
	}
	noSegLimit := constraint.MaxSegments <= 0
	noWordLimit := constraint.MaxWords <= 0
	noSpanLimit := maxIndexSpan <= 0
	if noSegLimit && noWordLimit && noSpanLimit {
		return [][]int{append([]int(nil), group...)}
	}

	var batches [][]int
	start := 0
	wordCount := 0
	for i, idx := range group {
		segWords := CountWords(doc.Segments[idx].Source)
		if i > start {
			segCount := i - start
			exceedSegments := !noSegLimit && segCount >= constraint.MaxSegments
			exceedWords := !noWordLimit && wordCount+segWords > constraint.MaxWords
			exceedSpan := !noSpanLimit && idx-group[start] > maxIndexSpan
			if exceedSegments || exceedWords || exceedSpan {
				batches = append(batches, append([]int(nil), group[start:i]...))
				start = i
				wordCount = 0
			}
		}
		wordCount += segWords
	}
	if start < len(group) {
		batches = append(batches, append([]int(nil), group[start:]...))
	}
	return batches
}

// filterInRange 返回 pending[0:endIdx] 中值在 [lo, hi] 范围内的元素。
func filterInRange(pending []int, lo, hi, endIdx int) []int {
	var result []int
	for i := 0; i < endIdx; i++ {
		if pending[i] >= lo && pending[i] <= hi {
			result = append(result, pending[i])
		}
	}
	return result
}
