// Package repair 实现 LLM 响应的"主动修复"算子：在 translate / bootstrap stage
// 解析失败之前，尽力把 LLM 返回的"看似畸形但可救活"的内容修成可解析的 JSON envelope，
// 并对部分 ID 缺失等情况返回 partial 结果，让上层选择重试而非整批降级。
//
// 设计原则：
//   - 修复必须安全：不动 string value 内容，只动 JSON 结构与外层 schema；
//     宁可让上层重试也不要凑出可能错的译文。
//   - 截断抢救：响应在键/值中途被截断（未闭合引号、悬空键）时，回退到最后一个
//     完整值闭合点抢救前缀；只截断不补全内容，残尾丢弃、缺失 ID 走重试；
//     无完整值时仍 Fatal。部分结果会被下游静默解释为完整语义的入口
//     （TryRepairSemanticQA / TryRepairAdjudication / glossary prune）经
//     Options.WithoutSalvage 显式弃用——弃用同时覆盖 close-braces 对「完整值
//     边界截断」的恢复，fail-closed 对所有截断形态成立。
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

// isValueTerminator 判断 s[at:] 起首个非空白字符是否为值位置引号闭合的合法后继：
// ',' '}' ']' 或 EOF。':' 意味着刚闭合的是键引号，不是值。
// 与 isLegalStringTerminator 的差别仅在不含 ':'——truncateToLastCompleteValue
// 只认「值闭合点」，悬空键与键中途截断的假边界由此规避。
func isValueTerminator(s string, at int) bool {
	for j := at; j < len(s); j++ {
		switch s[j] {
		case ' ', '\t', '\n', '\r':
			continue
		case ',', '}', ']':
			return true
		default:
			return false
		}
	}
	return true
}

// truncateToLastCompleteValue 扫描 JSON 文本，返回截至最后一个「值闭合点」的前缀。
// 值闭合点三类（均为"该位置之前的结构是自洽的完整 JSON 值序列"的安全切点）：
//  1. 字符串引号闭合，且其后首个非空白字符为 ',' '}' ']' 或 EOF——键引号后跟
//     ':'，天然排除（悬空键与键中途截断的假边界由此规避）；
//  2. '}' 容器闭合（对象值的结束：数组元素、内层 map 等）；
//  3. ']' 容器闭合（数组值的结束）。
//
// 无值闭合点或闭合点恰在文本末尾（cut == len(text)）时返回 (原文, false)。
// 只做截断：不补闭合、不编辑字符串内容——补齐由调用方重跑修复链完成
// （close-braces 负责补括号，缺失 ID 走 Missing/重试通道）。
//
// 安全门：
//  1. 只做"按完整值边界截断"+"补括号"，保留条目均为模型完整生成的字节；
//  2. 截断前缀必须经完整修复链解析成功才采纳（调用方职责）——引号相位错乱噪声
//     几乎必然解析失败，维持 Fatal；
//  3. 已知残留风险：值内未转义引号+截断双故障且尾部恰似 `,"NN":"` 时可能收下
//     被提前闭合的截断值，与既有 escapeUnescapedQuotes 算子同级，翻译域内可忽略。
func truncateToLastCompleteValue(s string) (string, bool) {
	cut := -1
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
				if isValueTerminator(s, i+1) {
					cut = i + 1
				}
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '}', ']':
			// 容器值闭合；根对象闭合后跟垃圾的情况 cut<len 也成立，无害
			// （截断前缀仍须经修复链解析成功才采纳）。
			cut = i + 1
		}
	}
	if cut <= 0 || cut >= len(s) {
		return s, false
	}
	return s[:cut], true
}

// closeUnbalancedBraces 当 s 末尾括号未平衡，追加缺失的闭合符号（'}' 或 ']'）。
// 同时追踪 '{'/'}'（对象）与 '['/']'（数组）：数组型 envelope（{"issues":[...]）被
// 截断时只补 '}' 会导致 ']' 缺失，json.Unmarshal 失败；用栈记录开启类型可正确补全。
// 若字符串未闭合则不补——补 '"' 容易把后续噪声纳入字符串值，反而引入错误内容。
// 字符串级缺口不属于本算子职责（本算子只补括号、不补引号）；截断场景由
// truncateToLastCompleteValue 在最后一个完整值闭合点回退、丢弃残尾后交回修复链
// 重跑处理，未闭合引号本身仍维持 Fatal。
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
// 该语义限于字符串级算子——它们不补闭合引号；截断场景由 truncateToLastCompleteValue
// 在最后一个完整值闭合点回退、丢弃残尾后重跑修复链处理，本函数不受影响。
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
//
// closeBracesPatch 控制截断残尾的修补采纳（与 extractBareArrayAsEnvelope 同口径）：
// 置 false 时（截断抢救弃用方——semantic_qa/adjudication），Decoder 直接解析失败的
// 偏移不再尝试 close-braces 补齐，「完整值边界截断」的残尾不会被补括号后采纳；
// 结巴/重复前缀等非截断噪声靠原文解码命中，不受影响。
func extractValidObject(text, requiredKey string, accept func(map[string]any) bool, closeBracesPatch bool) (map[string]any, bool) {
	var found map[string]any
	scanKeyedObjectsPatch(text, requiredKey, closeBracesPatch, func(raw map[string]any) bool {
		if accept != nil && !accept(raw) {
			return true // 不满足形状谓词，继续扫下一个候选
		}
		found = raw
		return false // 命中首个满足条件的候选，停止扫描
	})
	return found, found != nil
}

// robustScanMaxAttempts / robustScanTruncationWindow 是鲁棒扫描算子
// （scanKeyedObjects / extractBareArrayAsEnvelope）共享的调参常量：前者限制
// 尝试的偏移数，后者限定 close-braces 截断修补只对靠近文本末尾的偏移尝试，
// 避免对每个早期偏移都做 O(n) 后缀扫描。
const (
	robustScanMaxAttempts      = 256
	robustScanTruncationWindow = 4096
)

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
// 字符串做补闭合（Decoder 失败即跳过，保留"未闭合引号=Fatal"语义——字符串级算子
// 不补闭合；截断场景由 truncateToLastCompleteValue 在完整值边界回退后重跑修复链处理）。
func scanKeyedObjects(text, requiredKey string, handle func(map[string]any) bool) {
	scanKeyedObjectsPatch(text, requiredKey, true, handle)
}

// scanKeyedObjectsPatch 是 scanKeyedObjects 的带截断修补开关版本：
// closeBracesPatch=false 时（截断抢救弃用方）跳过 decodeKeyedEnvelope 失败后的
// close-braces 修补尝试，「完整值边界截断」的残尾不被补括号后采纳（fail-closed
// 对所有截断形态成立）；结巴/重复前缀等非截断噪声靠原文 Decoder 解码命中，
// 不受影响。
func scanKeyedObjectsPatch(text, requiredKey string, closeBracesPatch bool, handle func(map[string]any) bool) {
	attempts := 0
	for i := 0; i < len(text); i++ {
		if text[i] != '{' {
			continue
		}
		attempts++
		if attempts > robustScanMaxAttempts {
			break
		}
		tail := text[i:]
		raw, ok := decodeKeyedEnvelope(tail, requiredKey)
		if !ok && closeBracesPatch && len(tail) <= robustScanTruncationWindow {
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

// extractBareArrayAsEnvelope 是单键数组 envelope 的裸数组兜底。
//
// 目标破损形态：LLM 丢掉外层信封，直接输出条目数组本身，如
// [{"id":"1","base":"白银之风",...}]（应为 {"ruby_output":[...]}）。主链路的
// brace-matching 与 robust-extract 都只扫 '{' 偏移，永远找不到含 key 的对象。
//
// 本函数从每个 '[' 偏移用 json.Decoder 真实解析（容忍尾部多余数据），仅当解码
// 出"非空且元素全为对象"的 JSON 数组、且通过 accept 领域判别（nil 表示跳过）
// 时把它重新包装为 {key: [...]} 返回。只做结构搬运、不动元素内容；通用形状门控
// 挡掉空数组与异构数组，accept 挡掉键名形状不符的回显诱饵（如 prompt 中条目
// 清单的回显），元素级校验由调用方的 normalize 判定（兜底命中但 normalize 后
// 零条目视为误采纳、报错重试，见 bareArrayNoEntriesError）。
//
// closeBracesPatch 控制截断残尾的修补采纳：置 false 时（截断抢救弃用方——
// semantic_qa/adjudication），Decoder 直接解析失败的偏移不再尝试 close-braces
// 补齐——「完整值边界截断 + 括号不闭合」的残尾（如 {"verdicts":[{...},{...}）
// 会被补括号后误当作裸数组采纳，构成对弃用承诺的旁路；真裸数组（原文可完整
// 解码）不受影响，仍正常包装。
func extractBareArrayAsEnvelope(text, key string, accept func([]any) bool, closeBracesPatch bool) (map[string]any, bool) {
	attempts := 0
	for i := 0; i < len(text); i++ {
		if text[i] != '[' {
			continue
		}
		attempts++
		if attempts > robustScanMaxAttempts {
			break
		}
		tail := text[i:]
		dec := json.NewDecoder(strings.NewReader(tail))
		var arr []any
		err := dec.Decode(&arr)
		if err != nil && closeBracesPatch && len(tail) <= robustScanTruncationWindow {
			if fixed := closeUnbalancedBraces(tail); fixed != tail {
				dec = json.NewDecoder(strings.NewReader(fixed))
				err = dec.Decode(&arr)
			}
		}
		if err != nil || !allObjectEntries(arr) {
			continue
		}
		if accept != nil && !accept(arr) {
			continue
		}
		return map[string]any{key: arr}, true
	}
	return nil, false
}

// allObjectEntries 判定裸数组兜底的采纳门控：非空且全部元素为 JSON 对象。
// 目标条目（issues/verdicts/revisions/ruby_output）都是对象数组，元素为标量
// 或空数组均属噪声，继续扫描下一候选。
func allObjectEntries(arr []any) bool {
	if len(arr) == 0 {
		return false
	}
	for _, e := range arr {
		if _, ok := e.(map[string]any); !ok {
			return false
		}
	}
	return true
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
