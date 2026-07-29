package qa

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// WhitespaceIrregularChecker 检测零宽字符、NBSP、tab、行内异常空白。
// 普通首尾 trim 差异不报（由 postprocess 负责）。
type WhitespaceIrregularChecker struct{}

// NewWhitespaceIrregularChecker 创建空白异常检测器。
func NewWhitespaceIrregularChecker() *WhitespaceIrregularChecker {
	return &WhitespaceIrregularChecker{}
}

func (c *WhitespaceIrregularChecker) Name() string { return CheckWhitespaceIrregular }

func (c *WhitespaceIrregularChecker) Check(_ context.Context, segments []CheckInput) []QualityIssue {
	var issues []QualityIssue
	for _, seg := range segments {
		tgt := seg.TargetText
		if tgt == "" {
			continue
		}
		if hit := findIrregularWhitespace(tgt); hit != "" {
			span := LocateSpan(tgt, hit)
			if span == nil {
				span = &Span{MatchedText: hit}
			}
			issues = append(issues, QualityIssue{
				SegmentIndex: seg.Index,
				Severity:     SeverityWarning,
				Code:         CheckWhitespaceIrregular,
				Message:      fmt.Sprintf("存在异常空白字符：%s", describeWhitespace(hit)),
				Span:         span,
			})
		}
	}
	return issues
}

func findIrregularWhitespace(s string) string {
	for _, r := range s {
		switch r {
		case '\u200b', '\u200c', '\u200d', '\ufeff', // ZWSP / ZWNJ / ZWJ / BOM
			'\u00a0', // NBSP
			'\t',
			'\u2028', '\u2029', // line/paragraph separator
			'\u3000': // ideographic space treated as irregular when mixed mid-line? plan: 行内异常空白
			return string(r)
		}
		if unicode.Is(unicode.Cf, r) && unicode.IsSpace(r) {
			return string(r)
		}
	}
	// 行内连续非普通空格的空白 run（排除纯首尾 trim）
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return ""
	}
	start := strings.Index(s, trimmed)
	if start < 0 {
		return ""
	}
	inner := s[start : start+len(trimmed)]
	for i, r := range inner {
		if r == '\n' || r == '\r' {
			continue
		}
		if unicode.IsSpace(r) && r != ' ' {
			_, size := utf8.DecodeRuneInString(inner[i:])
			return inner[i : i+size]
		}
	}
	return ""
}

func describeWhitespace(s string) string {
	if s == "" {
		return ""
	}
	r, _ := utf8.DecodeRuneInString(s)
	switch r {
	case '\u200b':
		return "零宽空格 (U+200B)"
	case '\u200c':
		return "零宽非连接符 (U+200C)"
	case '\u200d':
		return "零宽连接符 (U+200D)"
	case '\ufeff':
		return "BOM (U+FEFF)"
	case '\u00a0':
		return "不间断空格 (NBSP)"
	case '\t':
		return "制表符"
	case '\u3000':
		return "全角空格"
	default:
		return fmt.Sprintf("U+%04X", r)
	}
}

// RepeatedSpaceChecker 检测连续空格以及 CJK 字符间多余空格。
type RepeatedSpaceChecker struct {
	cjkTarget bool
}

// NewRepeatedSpaceChecker 创建连续空格检测器。
func NewRepeatedSpaceChecker(targetLang string) *RepeatedSpaceChecker {
	return &RepeatedSpaceChecker{cjkTarget: glossaryIsCJK(targetLang)}
}

func (c *RepeatedSpaceChecker) Name() string { return CheckRepeatedSpace }

func (c *RepeatedSpaceChecker) Check(_ context.Context, segments []CheckInput) []QualityIssue {
	var issues []QualityIssue
	for _, seg := range segments {
		tgt := seg.TargetText
		if tgt == "" {
			continue
		}
		if hit := findRepeatedSpace(tgt, c.cjkTarget); hit != "" {
			span := LocateSpan(tgt, hit)
			if span == nil {
				span = &Span{MatchedText: hit}
			}
			issues = append(issues, QualityIssue{
				SegmentIndex: seg.Index,
				Severity:     SeverityWarning,
				Code:         CheckRepeatedSpace,
				Message:      "存在连续空格或 CJK 字符间多余空格",
				Span:         span,
			})
		}
	}
	return issues
}

func findRepeatedSpace(s string, cjkTarget bool) string {
	if strings.Contains(s, "  ") {
		idx := strings.Index(s, "  ")
		end := idx
		for end < len(s) && s[end] == ' ' {
			end++
		}
		return s[idx:end]
	}
	if !cjkTarget {
		return ""
	}
	runes := []rune(s)
	for i := 0; i+2 < len(runes); i++ {
		if isCJK(runes[i]) && runes[i+1] == ' ' && isCJK(runes[i+2]) {
			return string(runes[i : i+3])
		}
	}
	return ""
}
