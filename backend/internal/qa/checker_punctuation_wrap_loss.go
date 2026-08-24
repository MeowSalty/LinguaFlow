package qa

import (
	"context"
	"fmt"
	"strings"
)

// directionalQuotePairs 有向开→闭引号映射，不含对称的 ASCII "（无方向，
// 不能构成首尾包裹判定）。本地维护以保持 checker 语义自洽；correct 包
// 另有独立映射（helpers.go quotePairs），二者刻意不共享。
var directionalQuotePairs = map[rune]rune{
	'「': '」',
	'『': '』',
	'“': '”',
	'‘': '’',
	'«': '»',
}

// PunctuationWrapLossChecker 检测源文整段被引号包裹而译文丢失外层包裹。
//
// 与 punctuation_missing（整类计数归零）互补：译文只要新增任意内层引号
// （如 「…」→…“x”…）missing 即不触发，本 checker 按首尾包裹结构对比捕捉
// 该盲区。不做互斥——彻底丢引号时 missing 与本 checker 可同时各报一条
// （code 不同、指纹不同），单边残留（「a / a」）仍委派 punctuation_pairing。
type PunctuationWrapLossChecker struct{}

// NewPunctuationWrapLossChecker 创建 punctuation_wrap_loss checker。
func NewPunctuationWrapLossChecker() *PunctuationWrapLossChecker {
	return &PunctuationWrapLossChecker{}
}

func (c *PunctuationWrapLossChecker) Name() string { return CheckPunctuationWrapLoss }

func (c *PunctuationWrapLossChecker) Check(_ context.Context, segments []CheckInput) []QualityIssue {
	var issues []QualityIssue
	for _, seg := range segments {
		tgt := seg.TargetText
		src := strings.TrimSpace(seg.SourceText)
		if src == "" || strings.TrimSpace(tgt) == "" {
			continue
		}
		cleanSrc := StripProtectedRegions(src, seg.Protected)
		regions := InlineMarkupRegions(tgt, seg.Protected)
		cleanTgt := strings.TrimSpace(StripProtectedRegionsWithRegions(tgt, regions))
		if cleanTgt == "" {
			continue
		}
		open, close, ok := outerQuoteWrap(cleanSrc)
		if !ok {
			continue
		}
		if targetEdgesBare(cleanTgt) {
			matched := string(open) + string(close)
			span := LocateSpanExcludingRegions(tgt, matched, regions)
			if span == nil {
				span = &Span{MatchedText: matched}
			}
			issues = append(issues, QualityIssue{
				SegmentIndex: seg.Index,
				Severity:     SeverityWarning,
				Code:         CheckPunctuationWrapLoss,
				Message:      fmt.Sprintf("源文整体被引号包裹，译文丢失外层包裹：%s", matched),
				Span:         span,
			})
		}
	}
	return issues
}

// outerQuoteWrap 判定 text（clean、已 TrimSpace）是否为"单 span 整段引号包裹"：
// 首 rune 为有向开引号、末 rune 为其配对闭引号、中间有内容、且开闭各恰好
// 出现一次。多 span（「你好」「再见」）与句中引号均返回 false，前者归
// punctuation_missing，后者不是包裹丢失。
func outerQuoteWrap(text string) (open, close rune, ok bool) {
	first, hasFirst := firstRuneOf(text)
	if !hasFirst {
		return 0, 0, false
	}
	closing, isPair := directionalQuotePairs[first]
	if !isPair {
		return 0, 0, false
	}
	last, hasLast := lastRuneOf(text)
	if !hasLast || last != closing {
		return 0, 0, false
	}
	if runeLenOf(text) <= 2 {
		return 0, 0, false
	}
	if countCategory(text, map[rune]struct{}{first: {}, last: {}}) != 2 {
		return 0, 0, false
	}
	return first, closing, true
}

// targetEdgesBare 报告 text（clean、已 TrimSpace）首尾两端均无任何引号类
// rune（quoteRunes 超集，含对称 "）。首或尾存在任意引号即视为包裹结构
// 保留（含类别替换 「→“ 等），交由其他 checker 处理失衡。
func targetEdgesBare(text string) bool {
	first, ok := firstRuneOf(text)
	if ok && isQuoteRune(first) {
		return false
	}
	last, ok := lastRuneOf(text)
	if ok && isQuoteRune(last) {
		return false
	}
	return true
}

func isQuoteRune(r rune) bool {
	_, ok := quoteRunes[r]
	return ok
}

// firstRuneOf 返回 s 的首 rune；空串返回 false。
func firstRuneOf(s string) (rune, bool) {
	for _, r := range s {
		return r, true
	}
	return 0, false
}

// lastRuneOf 返回 s 的末 rune；空串返回 false。
func lastRuneOf(s string) (rune, bool) {
	var last rune
	seen := false
	for _, r := range s {
		last = r
		seen = true
	}
	return last, seen
}

// runeLenOf 返回 s 的 rune 数。
func runeLenOf(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}
