package qa

import (
	"context"
	"fmt"
	"strings"
)

// PunctuationPairingChecker 检测目标语标点配对（引号/括号/书名号等）。
type PunctuationPairingChecker struct {
	pairs []pairDef
}

type pairDef struct {
	open  rune
	close rune
}

// NewPunctuationPairingChecker 按目标语选择配对表。
func NewPunctuationPairingChecker(targetLang string) *PunctuationPairingChecker {
	return &PunctuationPairingChecker{pairs: punctuationPairsFor(targetLang)}
}

func (c *PunctuationPairingChecker) Name() string { return CheckPunctuationPairing }

func punctuationPairsFor(lang string) []pairDef {
	if glossaryIsCJK(lang) {
		return []pairDef{
			{'「', '」'},
			{'『', '』'},
			{'【', '】'},
			{'（', '）'},
			{'《', '》'},
			{'〈', '〉'},
			{'“', '”'},
			{'‘', '’'},
			{'(', ')'},
			{'[', ']'},
			{'{', '}'},
			{'"', '"'},
		}
	}
	return []pairDef{
		{'(', ')'},
		{'[', ']'},
		{'{', '}'},
		{'“', '”'},
		{'‘', '’'},
		{'«', '»'},
		{'"', '"'},
	}
}

// glossaryIsCJK mirrors glossary.IsCJKTarget without importing private helpers.
func glossaryIsCJK(lang string) bool {
	l := strings.ToLower(strings.TrimSpace(lang))
	if i := strings.IndexAny(l, "-_"); i > 0 {
		l = l[:i]
	}
	switch l {
	case "zh", "ja", "ko", "th", "lo", "my", "km":
		return true
	}
	return false
}

func (c *PunctuationPairingChecker) Check(_ context.Context, segments []CheckInput) []QualityIssue {
	var issues []QualityIssue
	for _, seg := range segments {
		tgt := seg.TargetText
		if strings.TrimSpace(tgt) == "" {
			continue
		}
		if hit, detail := findPairingProblem(tgt, c.pairs); hit != "" {
			span := LocateSpan(tgt, hit)
			if span == nil {
				span = &Span{MatchedText: hit}
			}
			issues = append(issues, QualityIssue{
				SegmentIndex: seg.Index,
				Severity:     SeverityWarning,
				Code:         CheckPunctuationPairing,
				Message:      fmt.Sprintf("标点配对异常：%s", detail),
				Span:         span,
			})
		}
	}
	return issues
}

// findPairingProblem 用栈检测未匹配或错嵌套的配对标点。
// 对称引号（open==close）按交替开合处理。
func findPairingProblem(text string, pairs []pairDef) (matched, detail string) {
	openOf := make(map[rune]rune, len(pairs))
	closeOf := make(map[rune]rune, len(pairs))
	symmetric := make(map[rune]bool, len(pairs))
	for _, p := range pairs {
		if p.open == p.close {
			symmetric[p.open] = true
			continue
		}
		openOf[p.open] = p.close
		closeOf[p.close] = p.open
	}

	type frame struct {
		open  rune
		close rune
		sym   bool
	}
	var stack []frame

	for _, r := range text {
		if symmetric[r] {
			if len(stack) > 0 && stack[len(stack)-1].sym && stack[len(stack)-1].open == r {
				stack = stack[:len(stack)-1]
			} else {
				stack = append(stack, frame{open: r, close: r, sym: true})
			}
			continue
		}
		if close, ok := openOf[r]; ok {
			stack = append(stack, frame{open: r, close: close})
			continue
		}
		if open, ok := closeOf[r]; ok {
			if len(stack) == 0 {
				return string(r), fmt.Sprintf("多余的闭合标点 %q", string(r))
			}
			top := stack[len(stack)-1]
			if top.open != open {
				return string(r), fmt.Sprintf("标点错嵌套：期望闭合 %q，实际 %q", string(top.close), string(r))
			}
			stack = stack[:len(stack)-1]
		}
	}
	if len(stack) > 0 {
		top := stack[len(stack)-1]
		return string(top.open), fmt.Sprintf("未闭合的标点 %q", string(top.open))
	}
	return "", ""
}
