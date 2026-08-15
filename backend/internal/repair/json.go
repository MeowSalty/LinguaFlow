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
	"reflect"
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

// escapeUnescapedQuotes 修复字符串值内未转义的双引号。
//
// LLM 常在 string 值里写出未转义的引号（如 "reason":""英国"是中文用词"），
// 使 JSON 解析在第二个 " 处提前闭合字符串，随后报 "invalid character after
// object key:value pair" 之类错误。本函数扫描整段：当处于字符串内、遇到一个
// 非转义的 " 时，若其后首个非空白字符不是合法的字符串终止上下文（',' '}' ']' ':'
// 之一：':' 对应 key 结束，其余对应 value 结束），则判定该 " 是值内引号，转义为 \"。
//
// 安全性：仅在主解析失败后作为兜底介入（调用方在 unmarshal 失败分支调用）；
// \" 解码后仍是 "，不改语义内容，与 escapeControlChars 同属安全转义。多字节 UTF-8
// 序列中不会出现 0x22 / 0x5C，故逐字节扫描 ASCII 终止符是安全的。
func escapeUnescapedQuotes(s string) string {
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
				if isLegalStringTerminator(s, i+1) {
					b.WriteByte(c)
					inStr = false
					continue
				}
				b.WriteString(`\"`)
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

// isLegalStringTerminator 判断 s[at:] 起首个非空白字符是否为合法的字符串终止
// 上下文：',' '}' ']' ':' 之一（':' 对应 key 结束，',}]' 对应 value 结束）。
// s[at:] 全为空白或为空时视为合法终止（末尾由 closeUnbalancedBraces 补齐）。
func isLegalStringTerminator(s string, at int) bool {
	for j := at; j < len(s); j++ {
		switch s[j] {
		case ' ', '\t', '\n', '\r':
			continue
		case ',', '}', ']', ':':
			return true
		default:
			return false
		}
	}
	return true
}

// closeUnbalancedBraces 当 s 末尾括号未平衡，追加缺失的闭合符号（'}' 或 ']'）。
// 同时追踪 '{'/'}'（对象）与 '['/']'（数组）：数组型 envelope（{"issues":[...]）被
// 截断时只补 '}' 会导致 ']' 缺失，json.Unmarshal 失败；用栈记录开启类型可正确补全。
// 若字符串未闭合则不补——补 '"' 容易把后续噪声纳入字符串值，反而引入错误内容。
func closeUnbalancedBraces(s string) string {
	var stack []byte
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
		case '{', '[':
			stack = append(stack, c)
		case '}', ']':
			if len(stack) > 0 {
				// 栈顶与 c 配对才弹：'}' 配 '{'、']' 配 '['。
				top := stack[len(stack)-1]
				if (c == '}' && top == '{') || (c == ']' && top == '[') {
					stack = stack[:len(stack)-1]
				}
			}
		}
	}
	if inStr || len(stack) == 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + len(stack))
	b.WriteString(s)
	// 栈底→顶记录最早的开启在前；闭合需按开启逆序（后开的先闭），故从栈顶倒序追加。
	for i := len(stack) - 1; i >= 0; i-- {
		switch stack[i] {
		case '{':
			b.WriteByte('}')
		case '[':
			b.WriteByte(']')
		}
	}
	return b.String()
}

// extractValidEnvelope 是 brace-matching 抽取失败后的鲁棒兜底。
//
// 目标破损形态：LLM 产生"结巴/重复前缀"（如 {"{"ruby_output":...}}）或任意
// 引号/括号相位错乱噪声，使 matchBracePair 字符串追踪失步，导致
// extractJSONObjectContaining / jsonObjectSlice 找不到 balanced 对象。
//
// 本函数从每个 '{' 偏移用 json.Decoder 真实解析：Decoder 读取首个完整 JSON 值
// 并容忍其后多余数据，因此即便对象被嵌在垃圾里也能定位。
//
// 安全性（关键）：
//   - 只接受含 canonical "translations" 字段且其值为 map 的对象作为 envelope；
//     alias（output/result 等）不在兜底里接受——alias 抽取由主链路
//     pickEnvelopeBody / normalizeEnvelopeKeys 负责，兜底只在主链路失败后介入，
//     此时按"命中任一 alias key 即返"会把诱饵对象（如 {"output":{"1":"x"}}）
//     误判为 envelope 并静默产出错误译文。
//   - 多个 translations 候选时 MERGE 各自的 translations map（首键优先，与
//     主链路 mergeTranslationObjects 语义一致），以恢复散落在多个对象里的 ID。
//   - 若同一 ID 在不同候选中出现且值不同（冲突），无法判定正确译文，按
//     "宁可重试也不要凑出可能错的译文"原则直接放弃（返回 false → 上层 Fatal/重试）。
//
// 未闭合字符串因 Decoder 失败而被正确判为不可救（保留"未闭合引号=Fatal"语义）。
func extractValidEnvelope(text string) (map[string]any, bool) {
	var first map[string]any
	merged := map[string]any{}
	aborted := false
	scanKeyedObjects(text, "translations", func(raw map[string]any) bool {
		t, ok := raw["translations"].(map[string]any)
		if !ok {
			// translations 值非 map（如 alias 诱饵或结巴产生的错误外层）：跳过。
			return true
		}
		if first == nil {
			first = raw
		}
		for k, v := range t {
			if existing, exists := merged[k]; exists {
				if !reflect.DeepEqual(existing, v) {
					// 同一 ID 出现冲突的不同值：无法确定正确译文，放弃（上层 Fatal/重试）。
					aborted = true
					return false
				}
				continue
			}
			merged[k] = v
		}
		return true
	})
	if aborted || first == nil {
		return nil, false
	}
	// 以首个候选为基底（保留 ruby_output/glossary 等字段），translations 用合并结果。
	out := make(map[string]any, len(first))
	for k, v := range first {
		out[k] = v
	}
	out["translations"] = merged
	return out, true
}

// decodeKeyedEnvelope 从 s 起用 Decoder 解析首个 JSON 对象，仅当其含
// requiredKey 字段时才视为候选。Decoder 读取首个完整 JSON 值并容忍其后多余数据，
// 因此即便对象被嵌在垃圾里也能定位。不校验字段值类型--由调用方通过 accept 判定。
func decodeKeyedEnvelope(s, requiredKey string) (map[string]any, bool) {
	dec := json.NewDecoder(strings.NewReader(s))
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return nil, false
	}
	if _, ok := raw[requiredKey]; !ok {
		return nil, false
	}
	return raw, true
}

// extractValidObject 是 brace-matching 抽取失败后的通用鲁棒兜底，面向非 translations
// 的单键数组 envelope（{"issues":[...]}/{"verdicts":[...]}/{"ruby_output":[...]}）。
//
// 与 extractValidEnvelope 同源：从每个 '{' 偏移用 json.Decoder 真实解析（容忍尾部
// 噪声与结巴/重复前缀），accept 判定字段值形状（调用方传 arrayValueAccept 等）。
// 不同之处：这些 envelope 的值是数组而非 ID-keyed map，故不做合并--首个满足 accept
// 的候选即返回。
//
// 安全性：accept 必须校验字段值类型，避免把同名但形状错误的外层包装（如结巴产生的
// {"issues":{"issues":[]}}）误判为目标 envelope。
func extractValidObject(text, requiredKey string, accept func(map[string]any) bool) (map[string]any, bool) {
	var found map[string]any
	scanKeyedObjects(text, requiredKey, func(raw map[string]any) bool {
		if accept != nil && !accept(raw) {
			return true // 不满足形状谓词，继续扫下一个候选
		}
		found = raw
		return false // 命中首个满足条件的候选，停止扫描
	})
	return found, found != nil
}

// scanKeyedObjects 是 extractValidEnvelope / extractValidObject 共享的鲁棒扫描骨架。
//
// 从 text 中每个 '{' 偏移用 json.Decoder 真实解析首个 JSON 对象（容忍尾部多余数据，
// 因此即便对象被嵌在垃圾里或结巴/重复前缀也能定位），仅当对象含 requiredKey 字段时
// 调用 handle(raw)。handle 返回 false 表示停止扫描，true 表示继续找下一个候选。
//
// 修复策略：每个 '{' 偏移只取一个候选——先试原文，原文失败且属截断场景（tail 较短）
// 再试 close-braces。close-braces 候选仅用于"整段被截断"场景：只当 '{' 靠近文本末尾
// 时才尝试，避免对每个早期偏移都对整段后缀做 O(n) 扫描（否则失败路径呈近似二次开销）。
// 整段截断的兜底已由主链路 close-braces（repair.go）处理过，此处仅作补充。
//
// 安全约束：本函数不做字段值类型校验（那是 handle/accept 的职责），也不对未闭合
// 字符串做补闭合（Decoder 失败即跳过，保留"未闭合引号=Fatal"语义）。
func scanKeyedObjects(text, requiredKey string, handle func(map[string]any) bool) {
	const (
		maxAttempts      = 256
		truncationWindow = 4096
	)
	attempts := 0
	for i := 0; i < len(text); i++ {
		if text[i] != '{' {
			continue
		}
		attempts++
		if attempts > maxAttempts {
			break
		}
		tail := text[i:]
		raw, ok := decodeKeyedEnvelope(tail, requiredKey)
		if !ok && len(tail) <= truncationWindow {
			if fixed := closeUnbalancedBraces(tail); fixed != tail {
				raw, ok = decodeKeyedEnvelope(fixed, requiredKey)
			}
		}
		if !ok {
			continue
		}
		if !handle(raw) {
			return
		}
	}
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
