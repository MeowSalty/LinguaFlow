package qa

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
)

// ForbiddenTermChecker 检测源文命中禁译条目且译文含 banned target。
type ForbiddenTermChecker struct {
	gls     glossary.Glossary
	srcLang string
	tgtLang string
}

// NewForbiddenTermChecker 创建禁译检测器；glossary 为 nil 时跳过。
func NewForbiddenTermChecker(gls glossary.Glossary, srcLang, tgtLang string) *ForbiddenTermChecker {
	return &ForbiddenTermChecker{gls: gls, srcLang: srcLang, tgtLang: tgtLang}
}

func (c *ForbiddenTermChecker) Name() string { return CheckForbiddenTerm }

func (c *ForbiddenTermChecker) Check(ctx context.Context, segments []CheckInput) []QualityIssue {
	if c.gls == nil {
		return nil
	}
	var issues []QualityIssue
	for _, seg := range segments {
		src := seg.SourceText
		tgt := seg.TargetText
		if strings.TrimSpace(src) == "" || strings.TrimSpace(tgt) == "" {
			continue
		}
		hits, err := c.gls.Lookup(ctx, src, c.srcLang, c.tgtLang)
		if err != nil || len(hits) == 0 {
			continue
		}
		seen := make(map[string]struct{})
		for _, e := range hits {
			if !e.Forbidden {
				continue
			}
			if e.Target == "" {
				continue
			}
			if !termPresent(src, e.Source, e.CaseSensitive, c.srcLang) {
				continue
			}
			if !termPresent(tgt, e.Target, e.CaseSensitive, c.tgtLang) {
				continue
			}
			key := e.Source + "\x00" + e.Target
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			span := LocateSpan(tgt, e.Target)
			if span == nil {
				span = &Span{MatchedText: e.Target}
			}
			issues = append(issues, QualityIssue{
				SegmentIndex: seg.Index,
				Severity:     SeverityError,
				Code:         CheckForbiddenTerm,
				Message:      fmt.Sprintf("译文含禁译词「%s」（源术语「%s」）", e.Target, e.Source),
				Span:         span,
			})
		}
	}
	return issues
}

// TermInconsistencyChecker 检测源文命中推荐词条但译文未使用推荐译法。
// mandatory=true → error；mandatory=false → warning。
type TermInconsistencyChecker struct {
	gls     glossary.Glossary
	srcLang string
	tgtLang string
}

// NewTermInconsistencyChecker 创建术语不一致检测器；glossary 为 nil 时跳过。
func NewTermInconsistencyChecker(gls glossary.Glossary, srcLang, tgtLang string) *TermInconsistencyChecker {
	return &TermInconsistencyChecker{gls: gls, srcLang: srcLang, tgtLang: tgtLang}
}

func (c *TermInconsistencyChecker) Name() string { return CheckTermInconsistency }

func (c *TermInconsistencyChecker) Check(ctx context.Context, segments []CheckInput) []QualityIssue {
	if c.gls == nil {
		return nil
	}
	var issues []QualityIssue
	for _, seg := range segments {
		src := seg.SourceText
		tgt := seg.TargetText
		if strings.TrimSpace(src) == "" || strings.TrimSpace(tgt) == "" {
			continue
		}
		hits, err := c.gls.Lookup(ctx, src, c.srcLang, c.tgtLang)
		if err != nil || len(hits) == 0 {
			continue
		}
		seen := make(map[string]struct{})
		for _, e := range hits {
			if e.Forbidden {
				continue
			}
			if e.Source == "" || e.Target == "" {
				continue
			}
			// 源侧须真正命中（尊重边界与大小写）
			if !termPresent(src, e.Source, e.CaseSensitive, c.srcLang) {
				continue
			}
			if termPresent(tgt, e.Target, e.CaseSensitive, c.tgtLang) {
				continue
			}
			key := e.Source + "\x00" + e.Target
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			sev := SeverityWarning
			if e.Mandatory {
				sev = SeverityError
			}
			issues = append(issues, QualityIssue{
				SegmentIndex: seg.Index,
				Severity:     sev,
				Code:         CheckTermInconsistency,
				Message:      fmt.Sprintf("术语「%s」未使用推荐译法「%s」", e.Source, e.Target),
				Span:         &Span{MatchedText: e.Source},
			})
		}
	}
	return issues
}

// termPresent 判断 needle 是否按 SafeReplace 边界语义出现在 haystack 中。
// CJK 语种：直接包含；拉丁：词边界独立匹配；CaseSensitive=false 时大小写不敏感。
func termPresent(haystack, needle string, caseSensitive bool, lang string) bool {
	if needle == "" || haystack == "" {
		return false
	}
	if glossaryIsCJK(lang) {
		if caseSensitive {
			return strings.Contains(haystack, needle)
		}
		return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
	}
	return latinTermPresent(haystack, needle, caseSensitive)
}

func latinTermPresent(haystack, needle string, caseSensitive bool) bool {
	if caseSensitive {
		return latinIndependentContains(haystack, needle)
	}
	// 大小写不敏感：在 lower 文本上定位，再映射回原文做边界判定
	lowerH := strings.ToLower(haystack)
	lowerN := strings.ToLower(needle)
	if lowerN == "" {
		return false
	}
	// needle 与 haystack 可能因大小写折叠改变字节长度，按 rune 对齐扫描
	hRunes := []rune(haystack)
	nRunes := []rune(needle)
	if len(nRunes) == 0 || len(hRunes) < len(nRunes) {
		return false
	}
	// 用 lower 字符串的 Index 循环，但用原始字节偏移需注意 EqualFold 长度
	// 简化：按 rune 窗口 EqualFold + 边界
	for i := 0; i+len(nRunes) <= len(hRunes); i++ {
		window := string(hRunes[i : i+len(nRunes)])
		if !strings.EqualFold(window, needle) {
			continue
		}
		// 计算字节起止
		start := runeSliceByteOffset(hRunes, i)
		end := runeSliceByteOffset(hRunes, i+len(nRunes))
		if isIndependentMatchLocal(haystack, start, end) {
			return true
		}
		_ = lowerH
		_ = lowerN
	}
	return false
}

func latinIndependentContains(s, from string) bool {
	if from == "" {
		return false
	}
	i := 0
	for i < len(s) {
		j := strings.Index(s[i:], from)
		if j < 0 {
			return false
		}
		absStart := i + j
		absEnd := absStart + len(from)
		if isIndependentMatchLocal(s, absStart, absEnd) {
			return true
		}
		i = absEnd
	}
	return false
}

// isIndependentMatchLocal 镜像 glossary.isIndependentMatch：两侧非词内字符。
func isIndependentMatchLocal(s string, start, end int) bool {
	if start > 0 {
		r, _ := utf8.DecodeLastRuneInString(s[:start])
		if isWordCharLocal(r) {
			return false
		}
	}
	if end < len(s) {
		r, _ := utf8.DecodeRuneInString(s[end:])
		if isWordCharLocal(r) {
			return false
		}
	}
	return true
}

func isWordCharLocal(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func runeSliceByteOffset(runes []rune, n int) int {
	if n <= 0 {
		return 0
	}
	if n >= len(runes) {
		return len(string(runes))
	}
	return len(string(runes[:n]))
}
