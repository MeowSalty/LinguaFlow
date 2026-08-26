package qa

import (
	"context"
	"fmt"
	"strings"
)

// WidthMixChecker 检测全半角混用：
// CJK 目标语中混入半角标点；拉丁目标语中混入全角字符（U+FF01–FF5E）。
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
		regions := InlineMarkupRegions(tgt, seg.Protected)
		cleanTgt := StripRegions(tgt, regions)
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
		runes := []rune(text)
		for i := 0; i < len(runes); i++ {
			r := runes[i]
			if !isHalfwidthPunct(r) {
				continue
			}
			switch r {
			case ',', ':':
				// 数字双侧守卫：左右紧邻均为 ASCII 数字时豁免（1,000 / 12:30 / 2:1）。
				if i > 0 && i < len(runes)-1 && isASCIIDigit(runes[i-1]) && isASCIIDigit(runes[i+1]) {
					continue
				}
			case '!', '?':
				// 数字前缀 run 守卫：!? 连续 run 前紧邻 ASCII 数字时整个 run 豁免（5! / 3! / 100!?）。
				j := i + 1
				for j < len(runes) && (runes[j] == '!' || runes[j] == '?') {
					j++
				}
				if i > 0 && isASCIIDigit(runes[i-1]) {
					i = j - 1 // for 循环 i++ 后跳到 run 结束处
					continue
				}
				return string(r), fmt.Sprintf("CJK 译文中混入半角标点：%q", string(r))
			}
			return string(r), fmt.Sprintf("CJK 译文中混入半角标点：%q", string(r))
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

// isHalfwidthPunct CJK 译文中零歧义的半角标点安全集：
// `! ? , ; : ( ) [ ]`，半→全转换 +0xFEE0，与 correct 归一化规则共享同一口径。
// 其余半角符号（. " ' { } @#$%^&* 等）歧义过大，既不检出也不转换。
func isHalfwidthPunct(r rune) bool {
	switch r {
	case '!', '?', ',', ';', ':', '(', ')', '[', ']':
		return true
	}
	return false
}

// isASCIIDigit 判断是否 ASCII 数字 0-9（守卫用）。
func isASCIIDigit(r rune) bool { return r >= '0' && r <= '9' }

// isFullwidthChar 半角/全角形式区中与 ASCII 一一对应的全角字符（U+FF01–FF5E），
// 含全角字母数字与全角标点；排除 FF5F/FF60（｟｠，无 ASCII 对应）。
func isFullwidthChar(r rune) bool {
	return r >= 0xFF01 && r <= 0xFF5E
}
