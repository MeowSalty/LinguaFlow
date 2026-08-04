package glossary

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// IsCJKTarget 基于 BCP-47 主 tag 判定 lang 是否属于「无空格分词」语种。
// 命中时 SafeReplace 走直接替换路径；否则走带词边界的拉丁路径。
// 仅识别主 tag（"zh-CN" → "zh"），子 tag 不影响。
func IsCJKTarget(lang string) bool {
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

// SafeReplace 在 s 中把 from 字面值替换为 to，按 lang 选择边界策略。
//
//   - CJK 目标语（IsCJKTarget == true）：直接 strings.ReplaceAll。
//   - 其他目标语（拉丁/西里尔等）：扫描所有 from 出现位置，仅替换两侧 rune 不构成「词内字符」
//     的独立匹配；子串匹配（如 "AI" 出现在 "wait" 里）保留。
//
// 返回 (newText, replaced, warn)。replaced 表示是否产生过实际替换；warn != ""
// 时调用方应记 Warn 日志，常见情况包括只有歧义子串、或混合时跳过子串。
//
// 调用方应在 from == "" 或 from == to 时 noop（函数内部也做了短路）。
func SafeReplace(s, from, to, lang string) (string, bool, string) {
	if from == "" || from == to {
		return s, false, ""
	}
	if IsCJKTarget(lang) {
		if !strings.Contains(s, from) {
			return s, false, ""
		}
		return strings.ReplaceAll(s, from, to), true, ""
	}

	// 拉丁路径：逐 byte 找 from，配对左右 rune 边界。
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	replaced := false
	skippedSub := 0
	for i < len(s) {
		j := strings.Index(s[i:], from)
		if j < 0 {
			b.WriteString(s[i:])
			break
		}
		absStart := i + j
		absEnd := absStart + len(from)
		if isIndependentMatch(s, absStart, absEnd) {
			b.WriteString(s[i:absStart])
			b.WriteString(to)
			replaced = true
		} else {
			b.WriteString(s[i:absEnd])
			skippedSub++
		}
		i = absEnd
	}
	warn := ""
	switch {
	case skippedSub > 0 && !replaced:
		warn = "ambiguous-substring-only"
	case skippedSub > 0 && replaced:
		warn = fmt.Sprintf("kept %d substring matches", skippedSub)
	}
	return b.String(), replaced, warn
}

// isIndependentMatch 判断 s[start:end] 两侧是否构成词边界——左右相邻 rune 都不是
// 「词内字符」（letter / digit / 下划线），或抵达字符串端点。
func isIndependentMatch(s string, start, end int) bool {
	if start > 0 {
		r, _ := utf8.DecodeLastRuneInString(s[:start])
		if isWordChar(r) {
			return false
		}
	}
	if end < len(s) {
		r, _ := utf8.DecodeRuneInString(s[end:])
		if isWordChar(r) {
			return false
		}
	}
	return true
}

func isWordChar(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// CaseInsensitiveReplace 在 s 中查找 old 的大小写不敏感匹配，替换为 new。
// 返回替换后的字符串、是否发生了替换。
// 该函数仅在 case_sensitive == false 且 SafeReplace 未命中时作为降级路径调用。
func CaseInsensitiveReplace(s, old, new string) (string, bool) {
	if old == "" {
		return s, false
	}

	lowerS := strings.ToLower(s)
	lowerOld := strings.ToLower(old)

	// 1. 检查是否存在大小写不敏感匹配
	if !strings.Contains(lowerS, lowerOld) {
		return s, false
	}

	// 2. 使用 strings.ToLower 的索引定位匹配位置，然后按原始位置替换
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	replaced := false
	for i < len(s) {
		j := strings.Index(lowerS[i:], lowerOld)
		if j < 0 {
			b.WriteString(s[i:])
			break
		}
		absStart := i + j
		absEnd := absStart + len(old)
		b.WriteString(s[i:absStart])
		b.WriteString(new)
		replaced = true
		i = absEnd
	}
	return b.String(), replaced
}
