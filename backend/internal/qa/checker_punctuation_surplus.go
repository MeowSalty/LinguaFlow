package qa

import (
	"context"
	"fmt"
	"strings"
)

// PunctuationSurplusChecker 检测译文整类多出源文不存在的包裹标点（引号类/括号类）。
// 与 PunctuationMissingChecker 互补：仅源文该类标点计数 == 0 且译文该类计数 >= 2
// 时触发，译文有 <2 个即不报（失衡交给 punctuation_pairing），二者互斥无双报；无语言依赖。
type PunctuationSurplusChecker struct{}

// NewPunctuationSurplusChecker 创建 punctuation_surplus checker。
func NewPunctuationSurplusChecker() *PunctuationSurplusChecker {
	return &PunctuationSurplusChecker{}
}

func (c *PunctuationSurplusChecker) Name() string { return CheckPunctuationSurplus }

func (c *PunctuationSurplusChecker) Check(_ context.Context, segments []CheckInput) []QualityIssue {
	var issues []QualityIssue
	for _, seg := range segments {
		tgt := seg.TargetText
		src := strings.TrimSpace(seg.SourceText)
		if src == "" || strings.TrimSpace(tgt) == "" {
			continue
		}
		cleanSrc := StripProtectedRegions(src, seg.Protected)
		regions := InlineMarkupRegions(tgt, seg.Protected)
		cleanTgt := StripProtectedRegionsWithRegions(tgt, regions)
		for _, cat := range punctMissingCategories {
			if countCategory(cleanSrc, cat.set) != 0 || countCategory(cleanTgt, cat.set) < 2 {
				continue
			}
			matched := firstCategoryRune(cleanTgt, cat.set, 0) + firstCategoryRune(cleanTgt, cat.set, 1)
			span := LocateSpanExcludingRegions(tgt, matched, regions)
			if span == nil {
				span = &Span{MatchedText: matched}
			}
			issues = append(issues, QualityIssue{
				SegmentIndex: seg.Index,
				Severity:     SeverityWarning,
				Code:         CheckPunctuationSurplus,
				Message:      fmt.Sprintf("译文多出源文没有的%s标点：%s", cat.name, matched),
				Span:         span,
			})
		}
	}
	return issues
}
