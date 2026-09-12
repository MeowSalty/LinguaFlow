// Package segmatch provides text matching and replacement for segments.
package segmatch

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Match 表示一次命中的位置（字节偏移）。
type Match struct {
	Start int
	End   int
}

// Matcher 在文本上查找与替换。
type Matcher interface {
	// Find 返回 text 中所有命中位置（字节偏移，升序）。
	Find(text string) []Match
	// HasMatch 返回 text 中是否存在至少一个命中，语义与 len(Find(text)) > 0 一致。
	// 列表搜索等只需布尔判断的场景应使用它，避免为否定结果生成全部匹配。
	HasMatch(text string) bool
	// ReplaceAll 用 replaceWith 替换所有命中，返回新文本与替换次数。
	ReplaceAll(text, replaceWith string) (newText string, count int)
	// CandidateLiteral 返回可用于定位的"安全字面量"锚点。
	// 大小写敏感的 substring 匹配返回完整的查找原文；
	// 大小写敏感的 regex 匹配返回 regexp.LiteralPrefix 得到的非空字面前缀，无则返回空；
	// 大小写不敏感时一律返回空。
	CandidateLiteral() string
}

// Options 构造匹配器。
type Options struct {
	Find          string
	MatchMode     string // "substring" | "regex"，空值默认 "substring"
	CaseSensitive *bool  // nil 默认 true
	WholeWord     *bool  // nil 默认 false
}

// ErrInvalidPattern 表示正则表达式无法编译。
var ErrInvalidPattern = errors.New("invalid regex pattern")

// ErrUnsupportedMatchMode 表示传入的匹配模式不被支持。
var ErrUnsupportedMatchMode = errors.New("unsupported match mode")

type substringMatcher struct {
	find          string
	caseSensitive bool
	wholeWord     bool
}

type regexMatcher struct {
	re        *regexp.Regexp
	wholeWord bool
	candidate string
	// boundary 仅用于 HasMatch 的布尔预筛：把 pattern 包裹上整词边界后另编译。
	// 它与 Find 的「全局左最先匹配 + whole-word 过滤」并不完全等价：同一起点上
	// 交替分支的偏好选择（如 a|ab 对 "ab"，FindAll 提交较短的 a 后被过滤，包裹
	// 正则却可回溯到 ab）与被拒匹配的区间消耗都可能造成差异，因此只能作必要
	// 条件——Find 有命中时 boundary 必命中，预筛未命中即可安全早停返回 false；
	// 预筛命中仍须以 Find 为准。包裹在整串上匹配，不会像切片那样让 ^、\A、\b
	// 在切片起点重新生效。
	boundary *regexp.Regexp
}

// NewMatcher 按 opts 构造匹配器。
// 未知的匹配模式返回错误，以便调用方及时发现配置拼写错误。
func NewMatcher(opts Options) (Matcher, error) {
	caseSensitive := true
	if opts.CaseSensitive != nil {
		caseSensitive = *opts.CaseSensitive
	}
	wholeWord := false
	if opts.WholeWord != nil {
		wholeWord = *opts.WholeWord
	}

	switch opts.MatchMode {
	case "", "substring":
		return &substringMatcher{
			find:          opts.Find,
			caseSensitive: caseSensitive,
			wholeWord:     wholeWord,
		}, nil
	case "regex":
		re, err := regexp.Compile(opts.Find)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidPattern, err)
		}
		var candidate string
		wrapFlags := ""
		if caseSensitive {
			if prefix, _ := re.LiteralPrefix(); prefix != "" {
				candidate = prefix
			}
		} else {
			wrapFlags = "(?i)"
			re, err = regexp.Compile(wrapFlags + opts.Find)
			if err != nil {
				return nil, fmt.Errorf("%w: %v", ErrInvalidPattern, err)
			}
		}
		m := &regexMatcher{re: re, wholeWord: wholeWord, candidate: candidate}
		if wholeWord {
			// 大小写不敏感的折叠只影响 pattern 字面量，[^\pL\pN] 边界在 (?i)
			// 下仍恰好排除字母与数字，因此在编译前统一加前缀标志即可。
			wrapped := wrapFlags + `(?:\A|[^\pL\pN])(?:` + opts.Find + `)(?:\z|[^\pL\pN])`
			if b, berr := regexp.Compile(wrapped); berr == nil {
				m.boundary = b
			}
			// 包裹正则编译失败时置 nil，HasMatch 退化为直接精确判断，只慢不错。
		}
		return m, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedMatchMode, opts.MatchMode)
	}
}

func (m *substringMatcher) Find(text string) []Match {
	if m.find == "" {
		return nil
	}
	if m.caseSensitive {
		return m.findCaseSensitive(text)
	}
	return m.findCaseInsensitive(text)
}

func (m *substringMatcher) findCaseSensitive(text string) []Match {
	var matches []Match
	searchStart := 0
	for searchStart <= len(text) {
		relativeStart := strings.Index(text[searchStart:], m.find)
		if relativeStart < 0 {
			break
		}
		start := searchStart + relativeStart
		end := start + len(m.find)
		if !m.wholeWord || isWholeWord(text, start, end) {
			matches = append(matches, Match{Start: start, End: end})
			searchStart = end
		} else {
			// A rejected candidate can overlap a later valid candidate.
			searchStart = start + 1
		}
	}
	return matches
}

func (m *substringMatcher) findCaseInsensitive(text string) []Match {
	findRuneCount := utf8.RuneCountInString(m.find)
	var matches []Match
	for start := 0; start < len(text); {
		end, ok := advanceRunes(text, start, findRuneCount)
		if ok && strings.EqualFold(text[start:end], m.find) &&
			(!m.wholeWord || isWholeWord(text, start, end)) {
			matches = append(matches, Match{Start: start, End: end})
			start = end
			continue
		}
		_, size := utf8.DecodeRuneInString(text[start:])
		start += size
	}
	return matches
}

// HasMatch 判定 text 中是否存在命中，语义与 len(Find(text)) > 0 一致，
// 但找到首个命中即返回，不为否定结果构建全部匹配。
func (m *substringMatcher) HasMatch(text string) bool {
	if m.find == "" {
		return false
	}
	if m.caseSensitive {
		searchStart := 0
		for searchStart <= len(text) {
			relativeStart := strings.Index(text[searchStart:], m.find)
			if relativeStart < 0 {
				return false
			}
			start := searchStart + relativeStart
			end := start + len(m.find)
			if !m.wholeWord || isWholeWord(text, start, end) {
				return true
			}
			// A rejected candidate can overlap a later valid candidate.
			searchStart = start + 1
		}
		return false
	}
	findRuneCount := utf8.RuneCountInString(m.find)
	for start := 0; start < len(text); {
		end, ok := advanceRunes(text, start, findRuneCount)
		if ok && strings.EqualFold(text[start:end], m.find) &&
			(!m.wholeWord || isWholeWord(text, start, end)) {
			return true
		}
		_, size := utf8.DecodeRuneInString(text[start:])
		start += size
	}
	return false
}

func (m *substringMatcher) ReplaceAll(text, replaceWith string) (string, int) {
	matches := m.Find(text)
	if len(matches) == 0 {
		return text, 0
	}

	var builder strings.Builder
	newLength := len(text)
	for _, match := range matches {
		newLength += len(replaceWith) - (match.End - match.Start)
	}
	builder.Grow(newLength)
	last := 0
	for _, match := range matches {
		builder.WriteString(text[last:match.Start])
		builder.WriteString(replaceWith)
		last = match.End
	}
	builder.WriteString(text[last:])
	return builder.String(), len(matches)
}

func (m *regexMatcher) CandidateLiteral() string {
	return m.candidate
}

func (m *substringMatcher) CandidateLiteral() string {
	if m.caseSensitive {
		return m.find
	}
	return ""
}

func (m *regexMatcher) Find(text string) []Match {
	subs := m.submatchIndices(text)
	if len(subs) == 0 {
		return nil
	}
	matches := make([]Match, len(subs))
	for i, sm := range subs {
		matches[i] = Match{Start: sm[0], End: sm[1]}
	}
	return matches
}

// HasMatch 判定 text 中是否存在命中。非整词时 MatchString 与 FindAll 非空等价，
// 且不生成匹配切片；整词时先用 boundary 包裹正则做必要条件预筛（Find 有命中则
// boundary 必命中），预筛未命中即安全早停，命中再以 Find 的全局左最先语义为准。
func (m *regexMatcher) HasMatch(text string) bool {
	if !m.wholeWord {
		return m.re.MatchString(text)
	}
	if m.boundary != nil && !m.boundary.MatchString(text) {
		return false
	}
	return len(m.Find(text)) > 0
}

func (m *regexMatcher) ReplaceAll(text, replaceWith string) (string, int) {
	subs := m.submatchIndices(text)
	if len(subs) == 0 {
		return text, 0
	}
	dst := make([]byte, 0, len(text)+len(subs)*len(replaceWith))
	last := 0
	for _, sm := range subs {
		dst = append(dst, text[last:sm[0]]...)
		dst = m.re.ExpandString(dst, replaceWith, text, sm)
		last = sm[1]
	}
	dst = append(dst, text[last:]...)
	return string(dst), len(subs)
}

// submatchIndices 返回 text 上标准非重叠全局匹配中通过 whole-word 过滤的匹配，
// 每项为 FindAllStringSubmatchIndex 格式的完整子匹配索引（升序）。
// 未参与的捕获组仍为 -1，其余索引为 text 的绝对字节偏移。
// 匹配始终基于完整字符串：对切片迭代匹配会让 ^、\A、\b 等锚点在切片起点重新生效，
// 产生完整字符串上不存在的伪匹配。
func (m *regexMatcher) submatchIndices(text string) [][]int {
	subs := m.re.FindAllStringSubmatchIndex(text, -1)
	if !m.wholeWord || len(subs) == 0 {
		return subs
	}
	filtered := subs[:0]
	for _, sm := range subs {
		if isWholeWord(text, sm[0], sm[1]) {
			filtered = append(filtered, sm)
		}
	}
	return filtered
}

func isWholeWord(text string, start, end int) bool {
	if start > 0 {
		previous, _ := utf8.DecodeLastRuneInString(text[:start])
		if isWordRune(previous) {
			return false
		}
	}
	if end < len(text) {
		next, _ := utf8.DecodeRuneInString(text[end:])
		if isWordRune(next) {
			return false
		}
	}
	return true
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

func advanceRunes(text string, start, count int) (int, bool) {
	end := start
	for i := 0; i < count; i++ {
		if end >= len(text) {
			return 0, false
		}
		_, size := utf8.DecodeRuneInString(text[end:])
		end += size
	}
	return end, true
}
