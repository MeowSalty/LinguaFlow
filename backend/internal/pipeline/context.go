package pipeline

import (
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

// BuildContext 从 Document 中提取目标段落的前/后段作为上下文。
// 优先返回 OriginalSource（protect 前的原文），让 LLM 看到可读上下文而非 __LF_xxxx__。
// idx 越界返回空字符串。
func BuildContext(doc *Document, idx int, cfg config.ContextConfig) (prev, next string) {
	return BuildContextRange(doc, idx, idx, cfg)
}

// BuildContextRange 给批量翻译用：取 [from, to] 区间外侧的段作为上下文。
// from / to 是 doc.Segments 的下标，要求 from <= to。
// cfg.Enabled=false 时直接返回空字符串。
// cfg.Before/After 控制取前后各几段；cfg.MaxChars 控制每段最大字符数。
func BuildContextRange(doc *Document, from, to int, cfg config.ContextConfig) (prev, next string) {
	if !cfg.Enabled {
		return "", ""
	}
	before := max(cfg.Before, 1)
	after := max(cfg.After, 1)

	var prevParts []string
	for i := max(from-before, 0); i < from; i++ {
		prevParts = append(prevParts, contextText(&doc.Segments[i], cfg.MaxChars))
	}
	prev = strings.Join(prevParts, "\n\n")

	var nextParts []string
	for i := to + 1; i < min(to+1+after, len(doc.Segments)); i++ {
		nextParts = append(nextParts, contextText(&doc.Segments[i], cfg.MaxChars))
	}
	next = strings.Join(nextParts, "\n\n")
	return
}

// contextText 返回段落的可读原文。
// maxChars > 0 时在句子边界处截断。
func contextText(s *Segment, maxChars int) string {
	t := s.OriginalSource
	if t == "" {
		t = s.Source
	}
	if maxChars <= 0 || len([]rune(t)) <= maxChars {
		return t
	}
	rs := []rune(t)
	cut := maxChars
	for i := maxChars - 1; i >= maxChars/2; i-- {
		if isSentenceEnd(rs[i]) {
			cut = i + 1
			break
		}
	}
	return string(rs[:cut]) + "..."
}

var sentenceEndSet = map[rune]bool{
	'.': true, '!': true, '?': true,
	'。': true, '！': true, '？': true, '；': true,
	';': true, '\n': true,
}

func isSentenceEnd(r rune) bool {
	return sentenceEndSet[r]
}
