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
		regions := InlineMarkupRegions(tgt, seg.Protected)
		cleanTgt := StripRegions(tgt, regions)
		if hit := findIrregularWhitespace(cleanTgt); hit != "" {
			span := LocateSpanExcludingRegions(tgt, hit, regions)
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
		regions := InlineMarkupRegions(tgt, seg.Protected)
		// RepeatedSpaceChecker 在原串上检测，再过滤掉落在保护区内的命中。
		// 不使用 StripRegions：拼接非保护区片段会在边界处制造原文不存在的连续空格 / CJK 间空格。
		for _, m := range findRepeatedSpaceAll(tgt, c.cjkTarget) {
			if regionCovers(regions, m.start, m.end) {
				continue
			}
			span := &Span{MatchedText: m.text, TargetStart: &m.start, TargetEnd: &m.end}
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

type repeatedSpaceMatch struct {
	text       string
	start, end int // rune 偏移
}

// findRepeatedSpaceAll 返回原串中所有连续空格 / CJK 字符间空格的命中（rune 偏移）。
// 供 RepeatedSpaceChecker 在原串上过滤保护区使用，避免 StripRegions 拼接产生虚假命中。
func findRepeatedSpaceAll(s string, cjkTarget bool) []repeatedSpaceMatch {
	var out []repeatedSpaceMatch
	runes := []rune(s)
	i := 0
	for i < len(runes) {
		if runes[i] != ' ' {
			i++
			continue
		}
		start := i
		for i < len(runes) && runes[i] == ' ' {
			i++
		}
		if i-start >= 2 {
			out = append(out, repeatedSpaceMatch{text: string(runes[start:i]), start: start, end: i})
		}
	}
	if !cjkTarget {
		return out
	}
	for j := 0; j+2 < len(runes); j++ {
		if isCJK(runes[j]) && runes[j+1] == ' ' && isCJK(runes[j+2]) {
			out = append(out, repeatedSpaceMatch{text: string(runes[j : j+3]), start: j, end: j + 3})
		}
	}
	return out
}
