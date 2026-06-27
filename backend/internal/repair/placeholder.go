package repair

import (
	"fmt"
	"regexp"
)

// placeholderVariantRE 匹配 LF 占位符的常见变体形态：
//   - 标准：__LF_000001__（2 个尾部下划线）
//   - 大小写错：__lf_000001__ / __Lf_000001__
//   - 缺中间下划线：__LF000001__
//   - 末尾仅 1 个下划线：__LF_000001_
//   - 末尾零个下划线：__LF_000001（LLM 剥离全部尾部下划线，最常见损坏形态）
//   - 数字位数不规范（少于 6 位时由代码补零；多于 6 位会被拒绝）
//
// 尾部用 `_{0,2}` 匹配 0–2 个下划线，覆盖 LLM 剥离尾部下划线的场景。
// `_{0,2}` 比 `_+`（≥1）多了「零尾部下划线」的匹配能力，比 `_*` 更保守，
// 避免吞掉占位符后面紧跟的下划线字符。已知 key 兜底（knownKeys guard）
// 防止误匹配正文中的类占位符字面。
// 同理，LF 之后不允许夹空白——保持紧凑，避免吃正文。
var placeholderVariantRE = regexp.MustCompile(`__[lL][fF]_?(\d{1,7})_{0,2}`)

// NormalizePlaceholders 把 text 中"形态错误的"LF 占位符归一回标准 __LF_NNNNNN__。
//
// **Guard**：仅当归一后的 key 在 knownKeys 中存在时才替换；未知 key 的"变体"一律不动，
// 以免把正文里偶然出现的"看着像占位符"的字面值替换成真占位符，污染译文。
//
// 标准形态会被正则匹配但归一后 key 与 match 相同——不计入 normalized 列表。
// 返回 (新文本，被实际归一的标准 key 列表；按出现顺序、去重)。
func NormalizePlaceholders(text string, knownKeys map[string]string) (string, []string) {
	if len(knownKeys) == 0 {
		return text, nil
	}
	var normalized []string
	seen := map[string]bool{}

	out := placeholderVariantRE.ReplaceAllStringFunc(text, func(match string) string {
		sub := placeholderVariantRE.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		nStr := sub[1]
		if len(nStr) > 6 {
			// 超出标准位数，可能是 LLM 编造的；不修复
			return match
		}
		for len(nStr) < 6 {
			nStr = "0" + nStr
		}
		std := fmt.Sprintf("__LF_%s__", nStr)
		if _, ok := knownKeys[std]; !ok {
			return match
		}
		if match != std && !seen[std] {
			seen[std] = true
			normalized = append(normalized, std)
		}
		return std
	})
	return out, normalized
}
