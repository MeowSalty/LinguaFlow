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
	// ReplaceAll 用 replaceWith 替换所有命中，返回新文本与替换次数。
	ReplaceAll(text, replaceWith string) (newText string, count int)
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

type substringMatcher struct {
	find          string
	caseSensitive bool
	wholeWord     bool
}

type regexMatcher struct {
	re *regexp.Regexp
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
		pattern := opts.Find
		if !caseSensitive {
			pattern = "(?i)" + pattern
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidPattern, err)
		}
		return &regexMatcher{re: re}, nil
	default:
		return nil, fmt.Errorf("unsupported match mode %q", opts.MatchMode)
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

func (m *regexMatcher) Find(text string) []Match {
	indices := m.re.FindAllStringIndex(text, -1)
	if len(indices) == 0 {
		return nil
	}
	matches := make([]Match, len(indices))
	for i, index := range indices {
		matches[i] = Match{Start: index[0], End: index[1]}
	}
	return matches
}

func (m *regexMatcher) ReplaceAll(text, replaceWith string) (string, int) {
	return m.re.ReplaceAllString(text, replaceWith), len(m.re.FindAllStringIndex(text, -1))
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
