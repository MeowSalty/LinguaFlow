// Package script 提供「同一语言、多种文字系统」检测的单一事实来源：
// 从目标语言 tag 解析期望文字系统与同语言的兄弟文字系统，
// 并扫描文本统计兄弟文字系统的专属字符证据，供上层 qa checker 使用。
package script

import (
	"strings"

	"golang.org/x/text/language"
)

// Script 是 ISO 15924 文字系统码。
type Script string

const (
	Hans Script = "Hans" // 简体汉字
	Hant Script = "Hant" // 繁体汉字
	Latn Script = "Latn" // 拉丁字母
	Cyrl Script = "Cyrl" // 西里尔字母
	Arab Script = "Arab" // 阿拉伯字母
	Mong Script = "Mong" // 传统蒙文
	Guru Script = "Guru" // 古木基文（旁遮普语）
)

// DisplayName 返回用于 issue message 的中文名；未知码原样返回。
func (s Script) DisplayName() string {
	switch s {
	case Hans:
		return "简体汉字"
	case Hant:
		return "繁体汉字"
	case Latn:
		return "拉丁字母"
	case Cyrl:
		return "西里尔字母"
	case Arab:
		return "阿拉伯字母"
	case Mong:
		return "传统蒙文"
	case Guru:
		return "古木基文（旁遮普语）"
	default:
		return string(s)
	}
}

// languageScripts 声明式注册表：语言主子标签（小写）→ 该语言全部可用文字系统。
// 切片顺序即 Scan 并列证据时的决胜顺序，不要随意调换。
// cmn 为汉语的 ISO 639-3 码，与 zh 同义。
var languageScripts = map[string][]Script{
	"zh":  {Hans, Hant},
	"cmn": {Hans, Hant},
	"sr":  {Cyrl, Latn},
	"uz":  {Latn, Cyrl, Arab},
	"kk":  {Cyrl, Latn, Arab},
	"az":  {Latn, Cyrl, Arab},
	"mn":  {Cyrl, Mong},
	"pa":  {Guru, Arab},
	"ms":  {Latn, Arab},
	"ug":  {Arab, Cyrl, Latn},
	"ku":  {Latn, Arab, Cyrl},
	"tg":  {Cyrl, Latn},
}

// Profile 描述目标语言的文字系统期望。
type Profile struct {
	Language string   // 语言主子标签（小写），如 "zh"
	Expected Script   // 期望文字系统
	Siblings []Script // 同语言的其他文字系统（排除 Expected）
}

// Resolve 从目标语言 tag 解析 Profile。
// region 信息仅用于推断 script，不参与注册表匹配（如 zh-TW → Hant）。
// 空串、"auto"、解析失败、语言主子标签不在注册表（单文字系统语言、垃圾串等），
// 或推断出的 script 不在该语言的注册表集合内，一律返回 (zero, false)。
func Resolve(targetLang string) (Profile, bool) {
	tag := strings.TrimSpace(targetLang)
	if tag == "" || strings.EqualFold(tag, "auto") {
		return Profile{}, false
	}
	lang, err := language.Parse(tag)
	if err != nil {
		// 容错：小写化并把下划线换成连字符后再试一次（如 "zh_Hans" / "ZH-HANS"）
		retry := strings.ReplaceAll(strings.ToLower(tag), "_", "-")
		if lang, err = language.Parse(retry); err != nil {
			return Profile{}, false
		}
	}
	b, _ := lang.Base()
	langKey := strings.ToLower(b.String())
	if langKey == "" || langKey == "und" {
		return Profile{}, false
	}
	scripts, ok := languageScripts[langKey]
	if !ok {
		return Profile{}, false
	}
	// Script() 尊重显式 script 子标签，缺失时按 CLDR likely-subtags 兜底
	// （zh→Hans、zh-TW→Hant、sr→Cyrl、uz→Latn）；其 String() 即 ISO 15924 四字母码。
	sc, _ := lang.Script()
	expected := Script(sc.String())
	if !scriptIn(scripts, expected) {
		return Profile{}, false
	}
	siblings := make([]Script, 0, len(scripts)-1)
	for _, s := range scripts {
		if s != expected {
			siblings = append(siblings, s)
		}
	}
	return Profile{Language: langKey, Expected: expected, Siblings: siblings}, true
}

// scriptIn 判断 s 是否在 scripts 集合内。
func scriptIn(scripts []Script, s Script) bool {
	for _, x := range scripts {
		if x == s {
			return true
		}
	}
	return false
}
