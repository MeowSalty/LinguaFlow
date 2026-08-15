package qa

import (
	"context"
	"fmt"
	"strings"
)

// quoteRunes 引号类包裹标点 rune 超集（含 ASCII "）。类别内替换不报，故取各类引号并集。
var quoteRunes = map[rune]struct{}{
	'「': {}, '」': {}, '『': {}, '』': {},
	'“': {}, '”': {}, '‘': {}, '’': {},
	'«': {}, '»': {}, '"': {},
}

// parenRunes 括号类包裹标点 rune 超集（中/英/方/花括号与书名号）。
var parenRunes = map[rune]struct{}{
	'（': {}, '）': {}, '(': {}, ')': {},
	'【': {}, '】': {}, '[': {}, ']': {},
	'{': {}, '}': {}, '《': {}, '》': {},
	'〈': {}, '〉': {},
}

type punctMissingCategory struct {
	name string
	set  map[rune]struct{}
}

var punctMissingCategories = []punctMissingCategory{
	{name: "引号", set: quoteRunes},
	{name: "括号", set: parenRunes},
}

// PunctuationMissingChecker 检测译文整类缺失源文存在的包裹标点（引号类/括号类）。
// 与 PunctuationPairingChecker 互补：仅译文该类标点计数 == 0 时触发，
// 译文有 ≥1 即不报（失衡交给 punctuation_pairing），二者互斥无双报；无语言依赖。
type PunctuationMissingChecker struct{}

// NewPunctuationMissingChecker 创建 punctuation_missing checker。
func NewPunctuationMissingChecker() *PunctuationMissingChecker {
	return &PunctuationMissingChecker{}
}

func (c *PunctuationMissingChecker) Name() string { return CheckPunctuationMissing }

func (c *PunctuationMissingChecker) Check(_ context.Context, segments []CheckInput) []QualityIssue {
	var issues []QualityIssue
	for _, seg := range segments {
		src := strings.TrimSpace(stripPlaceholders(seg.SourceText))
		tgt := seg.TargetText
		if src == "" || strings.TrimSpace(tgt) == "" {
			continue
		}
		regions := ProtectedRegions(tgt, seg.Protected)
		cleanTgt := stripPlaceholders(StripRegions(tgt, regions))
		for _, cat := range punctMissingCategories {
			if countCategory(src, cat.set) < 2 || countCategory(cleanTgt, cat.set) != 0 {
				continue
			}
			matched := firstCategoryRune(src, cat.set, 0) + firstCategoryRune(src, cat.set, 1)
			span := LocateSpanExcludingRegions(tgt, matched, regions)
			if span == nil {
				span = &Span{MatchedText: matched}
			}
			issues = append(issues, QualityIssue{
				SegmentIndex: seg.Index,
				Severity:     SeverityWarning,
				Code:         CheckPunctuationMissing,
				Message:      fmt.Sprintf("译文缺失源文的%s标点：%s", cat.name, matched),
				Span:         span,
			})
		}
	}
	return issues
}

// countCategory 统计 text 中属于 set 的 rune 出现次数。
func countCategory(text string, set map[rune]struct{}) int {
	n := 0
	for _, r := range text {
		if _, ok := set[r]; ok {
			n++
		}
	}
	return n
}

// firstCategoryRune 返回 src 中属于 set 的第 occurrence 个（0 基）rune 字符串；不足返回空串。
// 取首现(0)+次现(1)拼成 matched：源文推定良构，故通常为开+闭；对称 rune（如 "）亦成立。
func firstCategoryRune(src string, set map[rune]struct{}, occurrence int) string {
	seen := 0
	for _, r := range src {
		if _, ok := set[r]; ok {
			if seen == occurrence {
				return string(r)
			}
			seen++
		}
	}
	return ""
}
