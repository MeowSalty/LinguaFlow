package script

import (
	"sync"
	"unicode"
)

// sharedOverride 手工维护的「两栖字」清单：推导表会把它们判为某一侧专属，
// 但它们在简繁文本中均属规范用法，单字证据的误报代价远高于漏报，构建专属表时剔除。
//
// 繁体侧（OpenCC t2s 会转写，但简体亦常用）：
//   - 乾（乾隆/乾坤）
//
// 简体侧（OpenCC s2t 会转写，但繁体亦常用）：
//   - 后（皇后）、里（公里/里長）、干（干涉/若干）
//   - 范（姓氏）、于（姓氏）、余（文言第一人称）、云（子曰诗云）
//   - 斗（北斗/熨斗）、岩（岩石）、咸（老少咸宜）
//   - 准（批准/准許）、划（划船）、征（遠征/長征）、游（游泳）
//   - 台（台灣）、峰（峰为繁体标准写法，峯反是异体）、迹（跡/迹并通）
//   - 采（文采/采邑）、党（党項/姓氏）、郁（馥郁/姓氏）、雇（僱的异体）
//
// 著经实测推导已归为共用（OpenCC 字级不转著→着），无须剔除。
// 每个字符须真实落在推导表内（见 TestSharedOverrideConsistency，推导变更时测试会提醒清理）。
const sharedOverride = "乾后里干范于余云斗岩咸准划征游台峰迹采党郁雇"

var (
	hanOnce      sync.Once
	hansSpecific map[rune]struct{} // 简体专属汉字（已剔除 sharedOverride）
	hantSpecific map[rune]struct{} // 繁体专属汉字（已剔除 sharedOverride）
)

// hanTables 惰性构建并返回两张 Han 专属字符表。
func hanTables() (hans, hant map[rune]struct{}) {
	hanOnce.Do(func() {
		override := make(map[rune]struct{}, len(sharedOverride))
		for _, r := range sharedOverride {
			override[r] = struct{}{}
		}
		hansSpecific = buildSpecific(hansSpecificChars, override)
		hantSpecific = buildSpecific(hantSpecificChars, override)
	})
	return hansSpecific, hantSpecific
}

// buildSpecific 把生成表字符串展开为集合，剔除 override 中的两栖字。
func buildSpecific(chars string, override map[rune]struct{}) map[rune]struct{} {
	m := make(map[rune]struct{}, len(chars))
	for _, r := range chars {
		if _, skip := override[r]; skip {
			continue
		}
		m[r] = struct{}{}
	}
	return m
}

// runeScript 判别单个 rune 的文字系统归属。
// 非 Han 字符按 Unicode RangeTable 直接归类；Han 字符查简繁专属表：
// 落在某一侧专属表则返回该侧，简繁共用或表未收录的返回 false（中性，不算证据）。
// 标点、数字、组合符号（Inherited）等一律 false。
func runeScript(r rune) (Script, bool) {
	switch {
	case unicode.Is(unicode.Han, r):
		hans, hant := hanTables()
		if _, ok := hans[r]; ok {
			return Hans, true
		}
		if _, ok := hant[r]; ok {
			return Hant, true
		}
		return "", false // 简繁共用，不算证据
	case unicode.Is(unicode.Cyrillic, r):
		return Cyrl, true
	case unicode.Is(unicode.Latin, r):
		// unicode.Latin 表只含拉丁字母本身，ASCII 数字/标点不在其中，直接用即可
		return Latn, true
	case unicode.Is(unicode.Arabic, r):
		return Arab, true
	case unicode.Is(unicode.Mongolian, r):
		return Mong, true
	case unicode.Is(unicode.Gurmukhi, r):
		return Guru, true
	default:
		return "", false // Common/Inherited/其他文字，中性
	}
}
