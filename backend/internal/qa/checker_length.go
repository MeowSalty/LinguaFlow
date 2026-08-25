package qa

import (
	"context"
	"fmt"
	"math"
	"unicode"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

// LengthRatioChecker 检测译文长度比异常。
type LengthRatioChecker struct {
	minRatio float64
	maxRatio float64
	method   LengthMethod
}

// NewLengthRatioChecker 创建一个长度比检测器。
// minRatio=0 表示禁用过短检测，maxRatio=0 表示禁用过长检测。
// 负值视为配置错误，回退为默认值（minRatio=0.2, maxRatio=3.0）。
func NewLengthRatioChecker(minRatio, maxRatio float64, method LengthMethod) *LengthRatioChecker {
	if minRatio < 0 {
		minRatio = 0.2
	}
	if maxRatio < 0 {
		maxRatio = 3.0
	}
	if maxRatio == 0 {
		maxRatio = math.Inf(1)
	}
	if method == "" {
		method = LengthMethodCharWeight
	}
	return &LengthRatioChecker{minRatio: minRatio, maxRatio: maxRatio, method: method}
}

func (c *LengthRatioChecker) Name() string { return CheckLengthRatio }

func (c *LengthRatioChecker) Check(_ context.Context, segments []CheckInput) []QualityIssue {
	var issues []QualityIssue
	for _, seg := range segments {
		// 长度检测在源文（提取时已剥离）与译文（还原后含标签）之间必须基于
		// 相同形态比较：统一委托 ruby.StripRubyTags 剥为"基底文本"，与
		// preserve_kinds 过滤后译文实际保留的字符对齐，避免注音文本导致
		// 长度比误报。
		src := ruby.StripRubyTags(seg.SourceText)
		tgt := ruby.StripRubyTags(seg.TargetText)
		if src == "" || tgt == "" {
			continue
		}
		srcLen := c.measure(src)
		if srcLen < 5 {
			continue
		}
		tgtLen := c.measure(tgt)
		ratio := float64(tgtLen) / float64(srcLen)
		if ratio < c.minRatio {
			issues = append(issues, QualityIssue{
				SegmentIndex: seg.Index,
				Severity:     SeverityWarning,
				Code:         CheckLengthRatio,
				Message:      fmt.Sprintf("译文过短 (%.1f%%)，长度比 %.2f", ratio*100, ratio),
			})
		} else if ratio > c.maxRatio {
			issues = append(issues, QualityIssue{
				SegmentIndex: seg.Index,
				Severity:     SeverityWarning,
				Code:         CheckLengthRatio,
				Message:      fmt.Sprintf("译文过长 (%.0f%%)，长度比 %.2f", ratio*100, ratio),
			})
		}
	}
	return issues
}

// measure 根据配置的计数方式计算文本长度。
func (c *LengthRatioChecker) measure(text string) int {
	if c.method == LengthMethodWordCount {
		return countWords(text)
	}
	return weightedLen(text)
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

// countWords 计算语义单元数：CJK 每字 1 词，拉丁每词 1 词。
func countWords(text string) int {
	count := 0
	inWord := false
	for _, r := range text {
		if isCJK(r) {
			count++
			inWord = false
		} else if unicode.IsSpace(r) {
			inWord = false
		} else {
			if !inWord {
				count++
				inWord = true
			}
		}
	}
	return count
}

// isCJK 检测 CJK 字符（中文、日文假名、韩文）。
func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r)
}
