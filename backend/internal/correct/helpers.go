package correct

import "strings"

// quotePairs is the authoritative open→close mapping used by the wrap rule.
// It mirrors a subset of qa.quoteRunes restricted to directionally paired quotes
// (symmetric " is excluded — it has no open/close direction). Kept local & explicit
// so correct never drifts if the checker's superset changes.
var quotePairs = map[rune]rune{
	'「': '」',
	'『': '』',
	'“': '”',
	'‘': '’',
	'«': '»',
}

func countRune(s string, r rune) int {
	n := 0
	for _, c := range s {
		if c == r {
			n++
		}
	}
	return n
}

// firstRune returns the first rune of s and whether s is non-empty.
func firstRune(s string) (rune, bool) {
	for _, r := range s {
		return r, true
	}
	return 0, false
}

// lastRune returns the last rune of s and whether s is non-empty.
func lastRune(s string) (rune, bool) {
	var last rune
	seen := false
	for _, r := range s {
		last = r
		seen = true
	}
	return last, seen
}

// closingRuneFor reports whether r is a known opening quote and returns its closing pair.
func closingRuneFor(r rune) (rune, bool) {
	c, ok := quotePairs[r]
	return c, ok
}

// sourceQuotesAtRawEdges 报告源文经 TrimSpace 后是否以 open 开头、close 结尾。
//
// StripProtectedRegions 对内联标记透明，形如 <b>「你好」</b>（标记在引号外侧）的源文
// 也会被判定为单一包裹 span。此时包裹原始译文边缘会把引号放到标记之外，与源文结构
// 相反（如 「<b>你好</b>」）。引号不在原始最外缘时调用方应拒绝执行，保留 issue 可见。
// 由两条机械包裹规则（punctuation_missing.wrap / punctuation_wrap_loss.wrap）共用。
func sourceQuotesAtRawEdges(source string, open, close rune) bool {
	rawSrc := strings.TrimSpace(source)
	return strings.HasPrefix(rawSrc, string(open)) && strings.HasSuffix(rawSrc, string(close))
}

// reasonSourceQuotesInsideMarkup 是 sourceQuotesAtRawEdges 失败时的共用拒绝原因。
const reasonSourceQuotesInsideMarkup = "source quotes sit inside protected markup; skip to avoid inverting structure"
