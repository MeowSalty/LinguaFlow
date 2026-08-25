package correct

import (
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/model"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

// PunctuationMissingWrapRule mechanically wraps a translation that lost the
// source's outer paired quotes. It only fires when the segment carries a
// punctuation_missing issue, the source is a single wrapped quote span, and the
// target has none of those quote runes — a safe, high-frequency subset. Multi-
// span sources (e.g. 「你好」「再见」) are left untouched with a warning.
type PunctuationMissingWrapRule struct{}

func (*PunctuationMissingWrapRule) Name() string { return RulePunctuationMissingWrap }

func (*PunctuationMissingWrapRule) ResolvedCodes() []string {
	return []string{qa.CheckPunctuationMissing}
}

func (r *PunctuationMissingWrapRule) Apply(seg *model.Segment) CorrectionResult {
	// 1) Trigger: must have a pending (non-dismissed) punctuation_missing issue.
	if !hasPendingIssueCode(seg.Issues, qa.CheckPunctuationMissing) {
		return CorrectionResult{Reason: "no punctuation_missing issue"}
	}
	// 2) Source: clean (strip protected regions + placeholders), trimmed, must be
	//    a single wrapped quote span: first rune is an opening quote, last rune is
	//    its matching closing quote, length strictly greater than the two quotes.
	cleanSrc := strings.TrimSpace(qa.StripProtectedRegions(seg.Source, seg.Protected))
	open, ok := firstRune(cleanSrc)
	if !ok {
		return CorrectionResult{Reason: "empty source"}
	}
	close, isPair := closingRuneFor(open)
	if !isPair {
		return CorrectionResult{Reason: "source does not start with a paired opening quote"}
	}
	closeActual, ok := lastRune(cleanSrc)
	if !ok || closeActual != close {
		return CorrectionResult{Reason: "source does not end with the matching closing quote"}
	}
	if runeLen(cleanSrc) <= 2 {
		return CorrectionResult{Reason: "source has no content between the quotes"}
	}
	// 3) Single-span guard: exactly one open and one close in the cleaned source.
	//    Multiple spans (count==2) → no-op to avoid wrong whole-wrap.
	if countRune(cleanSrc, open) != 1 || countRune(cleanSrc, close) != 1 {
		return CorrectionResult{Reason: "multi-span source, cannot safely whole-wrap"}
	}
	// 4) Target defense: the cleaned target must have zero of these quote runes.
	cleanTgt := strings.TrimSpace(qa.StripProtectedRegions(seg.Target, seg.Protected))
	if countRune(cleanTgt, open) != 0 || countRune(cleanTgt, close) != 0 {
		return CorrectionResult{Reason: "target already contains the quote runes"}
	}
	// 5) 源文边界守卫：源文引号不在原始最外缘时拒绝执行，保留 issue 可见
	//    （详见 sourceQuotesAtRawEdges）。
	if !sourceQuotesAtRawEdges(seg.Source, open, close) {
		return CorrectionResult{Reason: reasonSourceQuotesInsideMarkup}
	}
	newTarget := string(open) + seg.Target + string(close)
	return CorrectionResult{
		Changed:       true,
		NewTarget:     newTarget,
		Op:            "punctuation_missing.wrap",
		ResolvedCodes: []string{qa.CheckPunctuationMissing},
	}
}

// hasPendingIssueCode 报告 issues 中是否存在指定 code 且未被裁决为 dismissed 的 issue。
// correct 规则只能由 pending issue 触发；dismissed 对规则不可见，避免机械修复推翻裁决。
func hasPendingIssueCode(issues []qa.QualityIssue, code string) bool {
	for _, iss := range issues {
		if iss.Code == code && !iss.Dismissed() {
			return true
		}
	}
	return false
}

// runeLen returns the rune count of s.
func runeLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}
