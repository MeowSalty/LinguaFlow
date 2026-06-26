package pipeline

// BuildContext 从 Document 中提取目标段落的前/后段作为上下文。
// 优先返回 OriginalSource（protect 前的原文），让 LLM 看到可读上下文而非 __LF_xxxx__。
// idx 越界返回空字符串。
func BuildContext(doc *Document, idx int) (prev, next string) {
	return BuildContextRange(doc, idx, idx)
}

// BuildContextRange 给批量翻译用：取 [from, to] 区间外侧最近的段作为上下文。
// from / to 是 doc.Segments 的下标，要求 from <= to。
func BuildContextRange(doc *Document, from, to int) (prev, next string) {
	if from > 0 {
		prev = contextText(&doc.Segments[from-1])
	}
	if to+1 < len(doc.Segments) {
		next = contextText(&doc.Segments[to+1])
	}
	return
}

func contextText(s *Segment) string {
	if s.OriginalSource != "" {
		return s.OriginalSource
	}
	return s.Source
}
