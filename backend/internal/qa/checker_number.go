package qa

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// NumberMismatchChecker 检测源/译阿拉伯数字 token 多重集合是否一致。
// 归一化千分位/小数分隔符后比对；v1 不做 CJK 数字换算。
type NumberMismatchChecker struct{}

// NewNumberMismatchChecker 创建数字守恒检测器。
func NewNumberMismatchChecker() *NumberMismatchChecker {
	return &NumberMismatchChecker{}
}

func (c *NumberMismatchChecker) Name() string { return CheckNumberMismatch }

// 匹配含阿拉伯数字的 token：允许内部 , . 作为千分位/小数分隔。
var numberTokenRe = regexp.MustCompile(`[0-9]+(?:[.,][0-9]+)*`)

func (c *NumberMismatchChecker) Check(_ context.Context, segments []CheckInput) []QualityIssue {
	var issues []QualityIssue
	for _, seg := range segments {
		src := seg.SourceText
		tgt := seg.TargetText
		if strings.TrimSpace(src) == "" || strings.TrimSpace(tgt) == "" {
			continue
		}
		srcNums := extractNormalizedNumbers(src)
		tgtNums := extractNormalizedNumbers(tgt)
		if multisetEqual(srcNums, tgtNums) {
			continue
		}
		issues = append(issues, QualityIssue{
			SegmentIndex: seg.Index,
			Severity:     SeverityWarning,
			Code:         CheckNumberMismatch,
			Message:      fmt.Sprintf("数字不一致：原文 %v，译文 %v", displayNumbers(src), displayNumbers(tgt)),
		})
	}
	return issues
}

func extractNormalizedNumbers(text string) map[string]int {
	counts := make(map[string]int)
	for _, raw := range numberTokenRe.FindAllString(text, -1) {
		norm := normalizeNumberToken(raw)
		if norm == "" {
			continue
		}
		counts[norm]++
	}
	return counts
}

// normalizeNumberToken 容忍千分位逗号/点与小数点/逗号互换。
// 策略：若只有一个分隔符且其后位数不为 3 的整数倍惯例，优先视为小数；
// 简化实现——去掉所有 thrusand-like 分组后保留最后一个小数分隔符。
func normalizeNumberToken(raw string) string {
	if raw == "" {
		return ""
	}
	// 仅数字直接返回
	onlyDigits := true
	for _, r := range raw {
		if !unicode.IsDigit(r) {
			onlyDigits = false
			break
		}
	}
	if onlyDigits {
		return raw
	}

	// 收集数字与分隔符位置
	var digits strings.Builder
	seps := make([]struct {
		pos int // digit count before sep
		ch  rune
	}, 0, 4)
	digitCount := 0
	for _, r := range raw {
		if unicode.IsDigit(r) {
			digits.WriteRune(r)
			digitCount++
			continue
		}
		if r == '.' || r == ',' {
			seps = append(seps, struct {
				pos int
				ch  rune
			}{pos: digitCount, ch: r})
		}
	}
	d := digits.String()
	if len(seps) == 0 {
		return d
	}

	// 判定最后一个分隔符是否为小数分隔符：其后数字位数不为 3，或只有一个分隔符且后位数 < 3
	last := seps[len(seps)-1]
	afterLast := digitCount - last.pos
	isDecimal := afterLast > 0 && afterLast != 3
	// 多个分隔符时，若全部后段为 3 位则全是千分位
	allThousands := true
	for i, s := range seps {
		var after int
		if i+1 < len(seps) {
			after = seps[i+1].pos - s.pos
		} else {
			after = digitCount - s.pos
		}
		if after != 3 {
			allThousands = false
			break
		}
	}
	if allThousands {
		return d
	}
	if isDecimal || afterLast != 3 {
		// 整数部分 = last.pos 位，小数 = 其余
		intPart := d[:last.pos]
		frac := d[last.pos:]
		// 去掉整数部分内的（已在 d 中无分隔）——d 已是纯数字
		if frac == "" {
			return intPart
		}
		return intPart + "." + frac
	}
	return d
}

func multisetEqual(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func displayNumbers(text string) []string {
	return numberTokenRe.FindAllString(text, -1)
}
