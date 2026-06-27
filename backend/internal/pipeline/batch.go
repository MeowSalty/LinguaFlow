package pipeline

// BuildContextAwareBatches 根据上下文窗口合并重叠段落的 batch。
// enabled=false 或 ctxWindow<=0 时退化为连续分组。
func BuildContextAwareBatches(pending []int, batchSize, ctxWindow int, enabled bool) [][]int {
	if !enabled || ctxWindow <= 0 {
		return BuildContinuousPendingBatches(pending, batchSize)
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

	// 2. 每组内按 batchSize 切分
	var batches [][]int
	for _, group := range groups {
		for len(group) > batchSize {
			batches = append(batches, group[:batchSize])
			group = group[batchSize:]
		}
		if len(group) > 0 {
			batches = append(batches, group)
		}
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
