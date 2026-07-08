package qa

import (
	"context"
	"fmt"
	"math"
	"unicode"
)

// LengthRatioChecker 检测译文长度比异常。
type LengthRatioChecker struct {
	minRatio float64
	maxRatio float64
}

// NewLengthRatioChecker 创建一个长度比检测器。
// minRatio=0 表示禁用过短检测，maxRatio=0 表示禁用过长检测。
// 负值视为配置错误，回退为默认值（minRatio=0.2, maxRatio=3.0）。
func NewLengthRatioChecker(minRatio, maxRatio float64) *LengthRatioChecker {
	if minRatio < 0 {
		minRatio = 0.2
	}
	if maxRatio < 0 {
		maxRatio = 3.0
	}
	if maxRatio == 0 {
		maxRatio = math.Inf(1)
	}
	return &LengthRatioChecker{minRatio: minRatio, maxRatio: maxRatio}
}

func (c *LengthRatioChecker) Name() string { return "length_ratio" }

func (c *LengthRatioChecker) Check(_ context.Context, segments []CheckInput) []QualityIssue {
	var issues []QualityIssue
	for _, seg := range segments {
		src := seg.SourceText
		tgt := seg.TargetText
		if src == "" || tgt == "" {
			continue
		}
		srcLen := weightedLen(src)
		if srcLen < 5 {
			continue
		}
		tgtLen := weightedLen(tgt)
		ratio := float64(tgtLen) / float64(srcLen)
		if ratio < c.minRatio {
			issues = append(issues, QualityIssue{
				SegmentIndex: seg.Index,
				Severity:     SeverityWarning,
				Code:         "length_ratio",
				Message:      fmt.Sprintf("译文过短 (%.1f%%)，加权长度比 %.2f", ratio*100, ratio),
			})
		} else if ratio > c.maxRatio {
			issues = append(issues, QualityIssue{
				SegmentIndex: seg.Index,
				Severity:     SeverityWarning,
				Code:         "length_ratio",
				Message:      fmt.Sprintf("译文过长 (%.0f%%)，加权长度比 %.2f", ratio*100, ratio),
			})
		}
	}
	return issues
}

// weightedLen 计算加权字符长度：CJK 字符计 2，其他计 1。
func weightedLen(text string) int {
	n := 0
	for _, r := range text {
		if isCJK(r) {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// isCJK 检测 CJK 字符（中文、日文假名、韩文）。
func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r)
}
