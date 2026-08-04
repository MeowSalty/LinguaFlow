// Package repair 实现 LLM 响应的"主动修复"算子：在 translate / bootstrap stage
// 解析失败之前，尽力把 LLM 返回的"看似畸形但可救活"的内容修成可解析的 JSON envelope，
// 并对部分 ID 缺失等情况返回 partial 结果，让上层选择重试而非整批降级。
//
// 设计原则：
//   - 修复必须安全：不动 string value 内容，只动 JSON 结构与外层 schema；
//     宁可让上层重试也不要凑出可能错的译文。
//   - 修复算子彼此独立、可单测；TryRepair 串成完整链路。
//   - 占位符 normalize 仅作用于"已知 key 的变体"，从不对未知占位符做任何操作。
package repair

import (
	"encoding/json"
	"fmt"
	"strings"
)

// stripBOMAndZeroWidth 去掉文本中常见的 BOM 与零宽字符（U+FEFF / U+200B-U+200D）。
// 这些字符极少出现在合法 LLM 输出里；即使出现在字符串值中，对用户可见文本也无意义。
// 返回 (新文本，是否做过修改)。
func stripBOMAndZeroWidth(text string) (string, bool) {
	const (
		bom  = "\uFEFF"
		zws  = "\u200B"
		zwnj = "\u200C"
		zwj  = "\u200D"
	)
	orig := text
	for _, ch := range []string{bom, zws, zwnj, zwj} {
		text = strings.ReplaceAll(text, ch, "")
	}
	return text, text != orig
}

// matchBracePair 从 text[start] 处假定是 '{'，扫描到与之配对的 '}'，返回其索引。
// 期间正确跳过字符串内的转义和大括号；未匹配返回 -1。
func matchBracePair(text string, start int) int {
	if start >= len(text) || text[start] != '{' {
		return -1
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(text); i++ {
		c := text[i]
		if inStr {
			if esc {
				esc = false
				continue
			}
			switch c {
			case '\\':
				esc = true
			case '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// jsonObjectSlice 从 text 中截取首个 { 到与之配对的 } 之间的子串。
// 与 internal/pipeline/stages 中同名函数行为一致，独立维护以避免跨包依赖。
func jsonObjectSlice(text string) string {
	start := strings.IndexByte(text, '{')
	if start < 0 {
		return ""
	}
	end := matchBracePair(text, start)
	if end < 0 {
		return ""
	}
	return text[start : end+1]
}

// extractJSONObjectContaining 扫描 text 中所有 balanced {...} 区间，返回首个含
// `"requiredKey"` 字面的对象。找不到返回空串。
//
// 用途：当响应里同时存在 <thinking>{"reasoning":"..."}</thinking> 与
// {"translations":{...}} 两个对象时，jsonObjectSlice 会抓到第一个错误对象。
// 本函数则跳过不含目标字段的对象，落到正确的那一个。
func extractJSONObjectContaining(text, requiredKey string) string {
	needle := `"` + requiredKey + `"`
	pos := 0
	for pos < len(text) {
		off := strings.IndexByte(text[pos:], '{')
		if off < 0 {
			return ""
		}
		start := pos + off
		end := matchBracePair(text, start)
		if end < 0 {
			return ""
		}
		body := text[start : end+1]
		if strings.Contains(body, needle) {
			return body
		}
		pos = end + 1
	}
	return ""
}

// fixTrailingCommas 移除字符串外的尾随逗号（,} 或 ,]），允许逗号与括号间夹空白。
// 字符串内的逗号不动。
func fixTrailingCommas(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inStr := false
	esc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			b.WriteByte(c)
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
			b.WriteByte(c)
			continue
		}
		if c == ',' {
			j := i + 1
			for j < len(s) && (s[j] == ' ' || s[j] == '\t' || s[j] == '\n' || s[j] == '\r') {
				j++
			}
			if j < len(s) && (s[j] == '}' || s[j] == ']') {
				continue
			}
		}
		b.WriteByte(c)
	}
	return b.String()
}

// escapeControlChars 在字符串值内把未转义的控制字符（0x00-0x1F，含 \n \t \r）
// 转为 \uXXXX 形式。字符串外的不动。\n \t \r 本身在 JSON 字符串里也是非法的，
// 但出现频率最高（LLM 直接换行写多行），同样转义掉。
func escapeControlChars(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inStr := false
	esc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			if esc {
				b.WriteByte(c)
				esc = false
				continue
			}
			if c == '\\' {
				b.WriteByte(c)
				esc = true
				continue
			}
			if c == '"' {
				b.WriteByte(c)
				inStr = false
				continue
			}
			if c < 0x20 {
				fmt.Fprintf(&b, `\u%04X`, c)
				continue
			}
			b.WriteByte(c)
			continue
		}
		if c == '"' {
			inStr = true
		}
		b.WriteByte(c)
	}
	return b.String()
}

// closeUnbalancedBraces 当 s 末尾大括号未平衡（depth > 0），追加缺失数量的 '}'。
// 若字符串未闭合则不补——补 '"' 容易把后续噪声纳入字符串值，反而引入错误内容。
func closeUnbalancedBraces(s string) string {
	depth := 0
	inStr := false
	esc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			if esc {
				esc = false
				continue
			}
			switch c {
			case '\\':
				esc = true
			case '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
		}
	}
	if inStr || depth <= 0 {
		return s
	}
	return s + strings.Repeat("}", depth)
}

// mergeTranslationObjects 找出 text 中所有含 "translations" 字段的 JSON 对象，
// 合并它们的 translations map（首个出现优先），其他字段保留首个对象的值。
// 仅 1 个或 0 个对象时返回空串（无需合并）。
func mergeTranslationObjects(text string) string {
	merged := map[string]string{}
	var firstObj map[string]any
	pos := 0
	count := 0
	for pos < len(text) {
		off := strings.IndexByte(text[pos:], '{')
		if off < 0 {
			break
		}
		start := pos + off
		end := matchBracePair(text, start)
		if end < 0 {
			break
		}
		body := text[start : end+1]
		pos = end + 1
		if !strings.Contains(body, `"translations"`) {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(body), &raw); err != nil {
			continue
		}
		t, ok := raw["translations"].(map[string]any)
		if !ok {
			continue
		}
		count++
		for k, v := range t {
			if str, ok := v.(string); ok {
				if _, exists := merged[k]; !exists {
					merged[k] = str
				}
			}
		}
		if firstObj == nil {
			firstObj = raw
		}
	}
	if count < 2 || firstObj == nil {
		return ""
	}
	out := make(map[string]any, len(firstObj))
	for k, v := range firstObj {
		out[k] = v
	}
	transMap := make(map[string]any, len(merged))
	for k, v := range merged {
		transMap[k] = v
	}
	out["translations"] = transMap
	b, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(b)
}
