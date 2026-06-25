package stages

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/protect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
)

// parseBatchResponse 解析 {"translations":{"<id>":"<text>", ...}} 并校验 wantIDs 完整。
// 当响应携带 inline 抽取的 {"glossary":[...]} 时，一并解析返回；缺失视作空切片。
// 当响应携带 {"ruby_output":{...}} 时，一并解析返回；缺失视作 nil。
// 容错：模型有时把 JSON 包在 ```json … ``` 围栏里或夹带前后说明文字，
// 这里用 jsonObjectSlice 抽出第一段完整的 JSON 对象。
//
// 这是严格语义：缺一 ID 即 err、多一 ID 即 err；调用方包括 translateSingle 的 S5
// 占位符补救路径仍依赖该行为。批量主路径走 parseBatchResponseLenient（允许 partial）。
func parseBatchResponse(text string, wantIDs []string) (map[string]string, []prompt.BootstrapEntry, map[string][]protect.RubyOutputEntry, error) {
	body := jsonObjectSlice(text)
	if body == "" {
		return nil, nil, nil, fmt.Errorf("no JSON object found in response")
	}
	var env struct {
		Translations map[string]string                    `json:"translations"`
		Glossary     []prompt.BootstrapEntry              `json:"glossary"`
		RubyOutput   map[string][]protect.RubyOutputEntry `json:"ruby_output"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		return nil, nil, nil, fmt.Errorf("unmarshal translations: %w", err)
	}
	if env.Translations == nil {
		return nil, nil, nil, errors.New("response missing \"translations\" field")
	}
	for _, id := range wantIDs {
		if _, ok := env.Translations[id]; !ok {
			return nil, nil, nil, fmt.Errorf("missing translation for id %q", id)
		}
	}
	if len(env.Translations) != len(wantIDs) {
		return nil, nil, nil, fmt.Errorf("expected %d translations, got %d", len(wantIDs), len(env.Translations))
	}
	return env.Translations, env.Glossary, env.RubyOutput, nil
}

// parseBatchResponseLenient 是 parseBatchResponse 的"宽容"版本：委托 repair.TryRepair
// 做多层结构修复 + schema 容错，允许 wantIDs 部分缺失（写入 Result.Missing），
// 不把"多余 ID"视为错误。Result.Fatal=true 时调用方应走 shrinkOrFallback；否则
// 根据 Result.Missing 决定是否仅对缺失段单独重跑。
func parseBatchResponseLenient(text string, wantIDs []string, opt repair.Options) repair.Result {
	return repair.TryRepair(text, wantIDs, opt)
}

// jsonObjectSlice 从 text 中截取首个 { 到与之配对的 } 之间的子串。
// 支持字符串里的转义和大括号，跳过 ``` 围栏；找不到返回空串。
func jsonObjectSlice(text string) string {
	start := strings.IndexByte(text, '{')
	if start < 0 {
		return ""
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
				return text[start : i+1]
			}
		}
	}
	return ""
}

// translationsSchema 按 wantIDs 生成 OpenAI 严格 JSON Schema：
// 要求 translations 下的属性集合与 wantIDs 完全一致。
// 当 includeGlossary=true 时，在外层属性里再加一个 "glossary" 数组，要求 items 严格匹配
// {source,target,notes}；外层 required 同步加入 "glossary"。
// 当 includeRuby=true 时，在外层属性里加一个 "ruby_output" 对象，按 wantIDs 键控，
// 每个值为 {base,text} 数组；ruby_output 不加入 required（LLM 可以不输出）。
func translationsSchema(wantIDs []string, includeGlossary bool, includeRuby bool) map[string]any {
	props := make(map[string]any, len(wantIDs))
	for _, id := range wantIDs {
		props[id] = map[string]any{"type": "string"}
	}
	required := make([]string, len(wantIDs))
	copy(required, wantIDs)
	outerProps := map[string]any{
		"translations": map[string]any{
			"type":                 "object",
			"properties":           props,
			"required":             required,
			"additionalProperties": false,
		},
	}
	outerRequired := []string{"translations"}
	if includeGlossary {
		outerProps["glossary"] = map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"source": map[string]any{"type": "string"},
					"target": map[string]any{"type": "string"},
					"notes":  map[string]any{"type": "string"},
				},
				"required":             []string{"source", "target", "notes"},
				"additionalProperties": false,
			},
		}
		outerRequired = append(outerRequired, "glossary")
	}

	if includeRuby {
		rubyOutputProps := make(map[string]any)
		for _, id := range wantIDs {
			rubyOutputProps[id] = map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"base": map[string]any{"type": "string"},
						"text": map[string]any{"type": "string"},
					},
					"required":             []string{"base", "text"},
					"additionalProperties": false,
				},
			}
		}
		outerProps["ruby_output"] = map[string]any{
			"type":       "object",
			"properties": rubyOutputProps,
		}
		outerRequired = append(outerRequired, "ruby_output")
	}

	return map[string]any{
		"type":                 "object",
		"properties":           outerProps,
		"required":             outerRequired,
		"additionalProperties": false,
	}
}
