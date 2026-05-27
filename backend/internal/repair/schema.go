package repair

// normalizeEnvelopeKeys 把 raw 顶层若干候选字段（translation / result / output /
// results / data.translations）归一为 translations。若 raw 已含 translations 则不动。
// 返回 (修改后的 map, 是否做了归一化)。
//
// 这是 L2 schema 兼容修复的核心：LLM 偶尔写成 "translation" 缺 s、用 "result"/"output"
// 包裹、或多包一层 "data"。本函数只在顶层做平移，不递归修改字符串值。
func normalizeEnvelopeKeys(raw map[string]any) (map[string]any, bool) {
	if _, ok := raw["translations"]; ok {
		return raw, false
	}
	for _, alias := range []string{"translation", "result", "output", "results"} {
		if v, ok := raw[alias]; ok {
			if m, ok2 := v.(map[string]any); ok2 {
				raw["translations"] = m
				delete(raw, alias)
				return raw, true
			}
		}
	}
	if v, ok := raw["data"]; ok {
		if m, ok2 := v.(map[string]any); ok2 {
			if inner, ok3 := m["translations"]; ok3 {
				raw["translations"] = inner
				delete(raw, "data")
				return raw, true
			}
		}
	}
	return raw, false
}
