package qa

import (
	"context"
	"fmt"
	"strings"
)

// WidthMixChecker 检测全半角混用：
// CJK 目标语中混入半角标点；拉丁目标语中混入全角字符（U+FF00–FF60）。
type WidthMixChecker struct {
	cjkTarget bool
}

// NewWidthMixChecker 创建全半角混用检测器。
func NewWidthMixChecker(targetLang string) *WidthMixChecker {
	return &WidthMixChecker{cjkTarget: glossaryIsCJK(targetLang)}
}

func (c *WidthMixChecker) Name() string { return CheckWidthMix }

func (c *WidthMixChecker) Check(_ context.Context, segments []CheckInput) []QualityIssue {
	var issues []QualityIssue
	for _, seg := range segments {
		tgt := seg.TargetText
		if strings.TrimSpace(tgt) == "" {
			continue
		}
		cleanTgt := tgt
		var regions [][2]int
		if len(seg.Protected) > 0 {
			regions = ProtectedRegions(tgt, seg.Protected)
			cleanTgt = StripRegions(tgt, regions)
		}
		if hit, msg := findWidthMix(cleanTgt, c.cjkTarget); hit != "" {
			span := LocateSpanExcludingRegions(tgt, hit, regions)
			if span == nil {
				span = &Span{MatchedText: hit}
			}
			issues = append(issues, QualityIssue{
				SegmentIndex: seg.Index,
				Severity:     SeverityWarning,
				Code:         CheckWidthMix,
				Message:      msg,
				Span:         span,
			})
		}
	}
	return issues
}

func findWidthMix(text string, cjkTarget bool) (matched, message string) {
	if cjkTarget {
		for _, r := range text {
			if isHalfwidthPunct(r) {
				return string(r), fmt.Sprintf("CJK 译文中混入半角标点：%q", string(r))
			}
		}
		return "", ""
	}
	for _, r := range text {
		if isFullwidthChar(r) {
			return string(r), fmt.Sprintf("拉丁译文中混入全角字符：%q", string(r))
		}
	}
	return "", ""
}

// isHalfwidthPunct 常见半角标点（不含字母数字与空白）。
func isHalfwidthPunct(r rune) bool {
	switch r {
	case ',', '.', '!', '?', ';', ':', '(', ')', '[', ']', '{', '}',
		'"', '\'', '`', '~', '@', '#', '$', '%', '^', '&', '*',
		'-', '_', '=', '+', '\\', '|', '/', '<', '>':
		return true
	}
	return false
}

// isFullwidthChar U+FF00–FFEF 半角/全角形式区中的全角字符（FF00–FF60 为主）。
func isFullwidthChar(r rune) bool {
	if r >= 0xFF01 && r <= 0xFF60 {
		return true
	}
	return false
}
