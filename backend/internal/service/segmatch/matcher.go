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

// MaxPatternRunes 是 pattern 允许的最大 rune 数，按 rune 而非字节计数。
const MaxPatternRunes = 256

// ErrInvalidPattern 表示正则表达式无法编译。
var ErrInvalidPattern = errors.New("invalid regex pattern")

// ErrPatternTooLong 表示 pattern 的 rune 数超过 MaxPatternRunes。
var ErrPatternTooLong = errors.New("pattern too long")

// ErrUnsupportedMatchMode 表示传入的匹配模式不被支持。
var ErrUnsupportedMatchMode = errors.New("unsupported match mode")

type substringMatcher struct {
	find          string
	caseSensitive bool
	wholeWord     bool

	// 大小写不敏感匹配的预处理：把 find 逐 rune 归一为 SimpleFold 折叠环的
	// 最小代表元（两个 rune 折叠等价当且仅当代表元相同），并在该 rune 序列上
	// 预计算 KMP 失配函数，使匹配严格 O(n+m)。folded 为空表示无需预处理。
	folded []rune
	fail   []int
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
	if n := utf8.RuneCountInString(opts.Find); n > MaxPatternRunes {
		return nil, fmt.Errorf("%w: pattern is %d runes, max %d", ErrPatternTooLong, n, MaxPatternRunes)
	}
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
		m := &substringMatcher{
			find:          opts.Find,
			caseSensitive: caseSensitive,
			wholeWord:     wholeWord,
		}
		if !caseSensitive && opts.Find != "" {
			m.folded = foldRunes(opts.Find)
			m.fail = kmpFailure(m.folded)
		}
		return m, nil
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
			// 大小写不敏感的折叠只影响 pattern 字面量，[^\pL\p{Nd}] 边界在 (?i)
			// 下仍恰好排除字母与十进制数字，与 isWordRune（unicode.IsLetter ||
			// unicode.IsDigit，即 \pL \p{Nd}）保持一致；因此编译前统一加前缀
			// 标志即可。注意不能用 \pN：它还会排除 Nl/No（如 Ⅻ、½），导致
			// boundary 预筛在 Find 实际有命中时误判为无命中。
			wrapped := wrapFlags + `(?:\A|[^\pL\p{Nd}])(?:` + opts.Find + `)(?:\z|[^\pL\p{Nd}])`
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
	// 大小写不敏感时 folded 非空；text 的字节数小于 pattern 的 rune 数时
	// 必然无法容纳一次命中，O(1) 快速失败（每个 rune 至少占一个字节）。
	if len(text) < len(m.folded) {
		return nil
	}
	var matches []Match
	cursor := substringCursor{m: m, text: text}
	for {
		start, end, ok := cursor.next()
		if !ok {
			break
		}
		matches = append(matches, Match{Start: start, End: end})
	}
	return matches
}

// HasMatch 判定 text 中是否存在命中，语义与 len(Find(text)) > 0 一致，
// 但找到首个命中即返回，不为否定结果构建全部匹配。
func (m *substringMatcher) HasMatch(text string) bool {
	if m.find == "" {
		return false
	}
	if len(text) < len(m.folded) {
		return false
	}
	cursor := substringCursor{m: m, text: text}
	_, _, ok := cursor.next()
	return ok
}

// substringCursor 按起点升序产出通过 whole-word 过滤的 substring 命中。
// Find 收集全部命中，HasMatch 只取首个即停止，二者共用这一遍历核心。
// 每个 cursor 只服务于一次遍历，可安全跨 goroutine 复用同一个 Matcher。
type substringCursor struct {
	m    *substringMatcher
	text string

	// 大小写敏感：下一次 strings.Index 的起始字节偏移。
	searchStart int

	// 大小写不敏感：在折叠归一的 rune 序列上跑 KMP。
	q       int   // 已匹配的折叠 pattern 前缀长度，恒 < patternLen
	pos     int   // 下一个待消费 rune 的字节偏移
	seq     int   // 已消费 rune 的全局序号（0 起）
	rdStart []int // 环形缓冲：rdStart[runeIndex%patternLen] = 该 rune 起始字节
}

func (c *substringCursor) next() (start, end int, ok bool) {
	if c.m.caseSensitive {
		return c.nextCaseSensitive()
	}
	return c.nextCaseInsensitive()
}

// nextCaseSensitive 沿用 strings.Index 的字节语义：命中按左最先升序且互不
// 重叠；整词被拒时起点只前进一个字节，使跨过被拒起点的重叠候选仍可命中。
func (c *substringCursor) nextCaseSensitive() (int, int, bool) {
	text := c.text
	find := c.m.find
	for c.searchStart <= len(text) {
		relativeStart := strings.Index(text[c.searchStart:], find)
		if relativeStart < 0 {
			c.searchStart = len(text) + 1
			return 0, 0, false
		}
		start := c.searchStart + relativeStart
		end := start + len(find)
		if !c.m.wholeWord || isWholeWord(text, start, end) {
			c.searchStart = end
			return start, end, true
		}
		// A rejected candidate can overlap a later valid candidate.
		c.searchStart = start + 1
	}
	return 0, 0, false
}

// nextCaseInsensitive 在 text 上以 KMP 单趟匹配折叠归一的 pattern，命中按
// 起点升序且互不重叠；整词被拒时保留 KMP 失配回退，等价于起点前进一个
// rune，使重叠候选仍可命中。命中起点的字节偏移取自每个已消费 rune 的全局
// 序号对 patternLen 取模的环形缓冲，而不是当前 KMP 状态（状态会因失配回退
// 与命中重置而不再对应起点）。
func (c *substringCursor) nextCaseInsensitive() (int, int, bool) {
	text := c.text
	pat := c.m.folded
	fail := c.m.fail
	patternLen := len(pat)
	for c.pos < len(text) {
		globalIndex := c.seq
		r, size := utf8.DecodeRuneInString(text[c.pos:])
		start := c.pos
		folded := foldRune(r)
		c.seq = globalIndex + 1
		c.pos = start + size

		for c.q > 0 && pat[c.q] != folded {
			c.q = fail[c.q-1]
		}
		if pat[c.q] != folded {
			continue
		}
		if c.q == 0 && c.rdStart == nil {
			c.rdStart = make([]int, patternLen)
		}
		c.rdStart[globalIndex%patternLen] = start
		c.q++
		if c.q < patternLen {
			continue
		}
		matchStart := c.rdStart[(globalIndex-patternLen+1)%patternLen]
		if !c.m.wholeWord || isWholeWord(text, matchStart, c.pos) {
			c.q = 0
			return matchStart, c.pos, true
		}
		// 整词拒绝：按标准 KMP 回退继续寻找重叠候选。
		c.q = fail[patternLen-1]
	}
	return 0, 0, false
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
// 整词命中只检查 submatchIndices 是否非空，不构造 []Match。
func (m *regexMatcher) HasMatch(text string) bool {
	if !m.wholeWord {
		return m.re.MatchString(text)
	}
	if m.boundary != nil && !m.boundary.MatchString(text) {
		return false
	}
	return len(m.submatchIndices(text)) > 0
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

// foldRune 返回 r 在 unicode.SimpleFold 折叠环上的最小代表元。两个 rune 在
// simple case folding 下等价当且仅当它们落在同一折叠环，因此代表元相等
// 精确对应 strings.EqualFold 的单 rune 语义。非法 UTF-8 解码出的 RuneError
// 自成环，也按 strings.EqualFold 的行为与 U+FFFD 视为等价。
func foldRune(r rune) rune {
	minRune := r
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		if f < minRune {
			minRune = f
		}
	}
	return minRune
}

// foldRunes 把 s 逐 rune 折叠归一；range 对非法字节按 RuneError 单位处理，
// 与匹配时逐 rune 解码的切分方式一致。
func foldRunes(s string) []rune {
	folded := make([]rune, 0, utf8.RuneCountInString(s))
	for _, r := range s {
		folded = append(folded, foldRune(r))
	}
	return folded
}

// kmpFailure 计算 pat 的标准 KMP 失配函数：fail[i] 是 pat[:i+1] 的最长
// 真前缀且同时为后缀的长度。
func kmpFailure(pat []rune) []int {
	fail := make([]int, len(pat))
	for i, k := 1, 0; i < len(pat); i++ {
		for k > 0 && pat[k] != pat[i] {
			k = fail[k-1]
		}
		if pat[k] == pat[i] {
			k++
		}
		fail[i] = k
	}
	return fail
}
