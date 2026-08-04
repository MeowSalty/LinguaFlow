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

func isHanTable(t *unicode.RangeTable) bool {
	return t == unicode.Han
}

func isKanaTable(t *unicode.RangeTable) bool {
	return t == unicode.Hiragana || t == unicode.Katakana
}

func isHangulTable(t *unicode.RangeTable) bool {
	return t == unicode.Hangul
}
