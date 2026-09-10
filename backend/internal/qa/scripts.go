package qa

import (
	"strings"
	"unicode"
)

// primaryScripts 返回语言主标签对应的 Unicode 脚本表集合。
// 未知语言、"auto" 或空串返回 nil。
func primaryScripts(lang string) []*unicode.RangeTable {
	switch normalizeLang(lang) {
	case "ja":
		return []*unicode.RangeTable{unicode.Hiragana, unicode.Katakana, unicode.Han}
	case "ko":
		// Hangul + Han：韩文混用汉字；使 zh→ko 走弱档而非准强档
		return []*unicode.RangeTable{unicode.Hangul, unicode.Han}
	case "zh":
		return []*unicode.RangeTable{unicode.Han}
	case "ru", "uk", "be", "bg", "mk", "sr", "mn":
		return []*unicode.RangeTable{unicode.Cyrillic}
	case "ar", "ur", "fa", "ps", "az":
		return []*unicode.RangeTable{unicode.Arabic}
	case "he", "yi":
		return []*unicode.RangeTable{unicode.Hebrew}
	case "th":
		return []*unicode.RangeTable{unicode.Thai}
	case "lo":
		return []*unicode.RangeTable{unicode.Lao}
	case "hi", "mr", "sa", "ne":
		return []*unicode.RangeTable{unicode.Devanagari}
	case "bn", "as":
		return []*unicode.RangeTable{unicode.Bengali}
	case "ta":
		return []*unicode.RangeTable{unicode.Tamil}
	case "te":
		return []*unicode.RangeTable{unicode.Telugu}
	case "kn":
		return []*unicode.RangeTable{unicode.Kannada}
	case "ml":
		return []*unicode.RangeTable{unicode.Malayalam}
	case "gu":
		return []*unicode.RangeTable{unicode.Gujarati}
	case "pa":
		return []*unicode.RangeTable{unicode.Gurmukhi}
	case "si":
		return []*unicode.RangeTable{unicode.Sinhala}
	case "bo", "dz":
		return []*unicode.RangeTable{unicode.Tibetan}
	case "my":
		return []*unicode.RangeTable{unicode.Myanmar}
	case "ka":
		return []*unicode.RangeTable{unicode.Georgian}
	case "hy":
		return []*unicode.RangeTable{unicode.Armenian}
	case "am", "ti":
		return []*unicode.RangeTable{unicode.Ethiopic}
	case "km":
		return []*unicode.RangeTable{unicode.Khmer}
	case "el":
		return []*unicode.RangeTable{unicode.Greek}
	case "en", "fr", "de", "es", "it", "pt", "nl", "vi", "tr", "pl",
		"sv", "da", "no", "fi", "cs", "sk", "hu", "ro", "hr", "sl",
		"lt", "lv", "et", "sq", "ca", "gl", "eu", "id", "ms", "tl",
		"sw", "af", "is", "ga", "cy", "mt", "lb", "bs":
		return []*unicode.RangeTable{unicode.Latin}
	default:
		return nil
	}
}

// normalizeLang 取 BCP-47 首个子标签并小写（支持 - / _ 分隔）。
func normalizeLang(lang string) string {
	lang = strings.TrimSpace(lang)
	if lang == "" {
		return ""
	}
	lang = strings.ToLower(lang)
	if i := strings.IndexAny(lang, "-_"); i >= 0 {
		lang = lang[:i]
	}
	return lang
}

func tableIn(tables []*unicode.RangeTable, t *unicode.RangeTable) bool {
	for _, x := range tables {
		if x == t {
			return true
		}
	}
	return false
}

// sourceOnlyScripts 返回源语主脚本中目标语不使用的部分，即"出现即证明是源语文本"的脚本集。
// 与 source_residual 的 resolveRules 不同：不排除 Han，也不分档——判定的是整段回传，
// Han 本身就是有效证据（zh→en 的 identity 文本含汉字即铁证），无需 minRun 降噪。
// 任一语言为空/auto/无法解析时返回 ok=false，调用方据此退回保守判定。
func sourceOnlyScripts(srcLang, tgtLang string) ([]*unicode.RangeTable, bool) {
	src := normalizeLang(srcLang)
	tgt := normalizeLang(tgtLang)
	if src == "" || src == "auto" || tgt == "" || tgt == "auto" {
		return nil, false
	}
	srcS := primaryScripts(src)
	tgtS := targetWritingScripts(tgt)
	if srcS == nil || tgtS == nil {
		return nil, false
	}
	var only []*unicode.RangeTable
	for _, t := range srcS {
		if !tableIn(tgtS, t) {
			only = append(only, t)
		}
	}
	return only, true
}

// targetWritingScripts 返回目标语现代规范正文实际使用的脚本集，即"出现在 identity
// 文本里也不足以证明是源语回传"的那些脚本。
//
// 仅 ko 与 primaryScripts 不同。primaryScripts 把 Han 纳入 ko 是为 source_residual
// 的档位解析服务（让 zh→ko 落弱档而非准强档，见该函数内注释），但现代韩文正文以
// 谚文书写，汉字只见于古文与少量括注。沿用它会让 zh→ko 的差集为空，整段中文原文
// 回传降为 warning——而 ko 既不在 script.languageScripts 注册表内（script_mismatch
// 静默不活跃），zh→ko 的 source_residual 弱档也默认关闭，等于完全没有兜底。
func targetWritingScripts(lang string) []*unicode.RangeTable {
	scripts := primaryScripts(lang)
	if normalizeLang(lang) != "ko" {
		return scripts
	}
	out := make([]*unicode.RangeTable, 0, len(scripts))
	for _, t := range scripts {
		if isHanTable(t) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// containsAnyScript 报告 text 中是否出现属于 tables 任一脚本的字符。
func containsAnyScript(text string, tables []*unicode.RangeTable) bool {
	for _, r := range text {
		if ruleBelongs(r, tables) {
			return true
		}
	}
	return false
}

func isHanTable(t *unicode.RangeTable) bool {
	return t == unicode.Han
}

func isKanaTable(t *unicode.RangeTable) bool {
	return t == unicode.Hiragana || t == unicode.Katakana
}

func isHangulTable(t *unicode.RangeTable) bool {
	return t == unicode.Hangul
}
