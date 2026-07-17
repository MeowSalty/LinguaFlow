package qa

import (
	"context"
	"strings"
	"unicode"
)

// UntranslatedChecker 检测未翻译的段落（source == target）。
type UntranslatedChecker struct{}

// NewUntranslatedChecker 创建一个未翻译检测器。
func NewUntranslatedChecker() *UntranslatedChecker {
	return &UntranslatedChecker{}
}

func (c *UntranslatedChecker) Name() string { return "untranslated" }

func (c *UntranslatedChecker) Check(_ context.Context, segments []CheckInput) []QualityIssue {
	var issues []QualityIssue
	for _, seg := range segments {
		src := strings.TrimSpace(seg.SourceText)
		tgt := strings.TrimSpace(seg.TargetText)
		if src == "" || tgt == "" {
			continue
		}
		if src != tgt {
			continue
		}
		if isExempt(src) {
			continue
		}
		issues = append(issues, QualityIssue{
			SegmentIndex: seg.Index,
			Severity:     SeverityError,
			Code:         "untranslated",
			Message:      "译文与原文相同",
		})
	}
	return issues
}

// isExempt 检查文本是否属于豁免类型（纯数字、纯标点、纯占位符）。
func isExempt(text string) bool {
	if text == "" {
		return true
	}
	hasLetter := false
	isPlaceholder := len(text) >= 14 && strings.Contains(text, "__LF_")
	for _, r := range text {
		if unicode.IsLetter(r) {
			hasLetter = true
			break
		}
	}
	if isPlaceholder && !hasLetter {
		return true
	}
	if !hasLetter {
		return true
	}
	return false
}
