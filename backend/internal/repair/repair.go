package repair

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

// Options 控制各层修复算子的启用。零值（所有 bool=false）等于"不修复"——
// 调用方未传 Options 时 TryRepair 仅做基础 JSON 抽取，行为与原 jsonObjectSlice 路径一致。
type Options struct {
	JSONStructural       bool    // L1：BOM 剥离、尾随逗号、控制字符、括号补齐、多对象合并
	SchemaAliases        bool    // L2：字段名同义化（translation→translations 等）
	Partial              bool    // L2：部分 ID 缺失时返回 partial 而非 Fatal（调用方据此决定 single 重跑还是 shrink）
	PartialThreshold     float64 // (0,1]；缺失率 ≥ 阈值时调用方应放弃 partial 走 shrink
	PlaceholderNormalize bool    // L3：占位符大小写/下划线变体归一（与 NormalizePlaceholders 配合）
	PromptUpgrade        bool    // L4：解析失败或占位符仍缺失时附加反例 reminder 重试一次
}

// Result 是 TryRepair 的统一返回。
//
// 状态判定：
//   - Fatal=true：解析完全无救；调用方应走 shrinkOrFallback。ParseErr 非 nil。
//   - Fatal=false 且 Missing 空：全成功。
//   - Fatal=false 且 Missing 非空：partial。调用方据 Options.PartialThreshold 决定
//     仅对缺失 ID 单独重试，还是因缺失率过高放弃。
type Result struct {
	Trans      map[string]string
	Glos       []prompt.BootstrapEntry
	RubyOutput map[string][]ruby.OutputEntry // segment ID → ruby 输出条目
	Missing    []string                             // wantIDs 中未出现在 Trans 里的子集
	Repaired   []string                             // 修复算子链，便于日志诊断
	Fatal      bool
	ParseErr   error
}

// TryRepair 尝试解析 LLM 响应 text 为 envelope {"translations":{...}, "glossary":[...]}。
// 永不返回 error——失败语义通过 Result.Fatal + Result.ParseErr 表达。
//
// 修复链（每步独立可关）：
//  1. 去 BOM/零宽 (JSONStructural)
//  2. 抽取含 "translations" 的对象（或 SchemaAliases 启用时退而求其次找 alias 字段）
//  3. 解析失败 → 尝试 trailing-comma / escape-control / close-braces / merge-objects
//  4. SchemaAliases 启用时把 translation/result/output/data.translations 归一为 translations
//  5. 校验 wantIDs：缺失记入 Missing；多余 ID **不**视为错误（与旧 strict 路径不同）
func TryRepair(text string, wantIDs []string, opt Options) Result {
	var repaired []string

	if opt.JSONStructural {
		cleaned, did := stripBOMAndZeroWidth(text)
		if did {
			repaired = append(repaired, "json.strip-bom-zw")
		}
		text = cleaned
	}

	// 若文本中存在 ≥2 个 translations 对象，先尝试 merge——否则 extractJSONObjectContaining
	// 只会拿到第一个，丢掉其他对象里的 ID。
	if opt.JSONStructural {
		if merged := mergeTranslationObjects(text); merged != "" {
			if r2, err := unmarshalGeneric(merged); err == nil {
				repaired = append(repaired, "json.merge-objects")
				return finalizeResult(r2, wantIDs, repaired, opt)
			}
		}
	}

	body := pickEnvelopeBody(text, opt)
	if body == "" && opt.JSONStructural {
		// 兜底：text 可能整体未闭合，尝试补齐括号再抽。
		if fixed := closeUnbalancedBraces(text); fixed != text {
			body = pickEnvelopeBody(fixed, opt)
			if body != "" {
				repaired = append(repaired, "json.close-braces")
			}
		}
	}
	if body == "" {
		return Result{Fatal: true, Repaired: repaired, ParseErr: errors.New("no JSON object found")}
	}

	raw, err := unmarshalGeneric(body)

	if err != nil && opt.JSONStructural {
		fixed := body
		if v := fixTrailingCommas(fixed); v != fixed {
			fixed = v
			repaired = append(repaired, "json.trailing-comma")
		}
		if v := escapeControlChars(fixed); v != fixed {
			fixed = v
			repaired = append(repaired, "json.escape-control")
		}
		if v := closeUnbalancedBraces(fixed); v != fixed {
			fixed = v
			repaired = append(repaired, "json.close-braces")
		}
		if fixed != body {
			raw, err = unmarshalGeneric(fixed)
		}
	}

	if err != nil {
		return Result{Fatal: true, Repaired: repaired, ParseErr: fmt.Errorf("unmarshal: %w", err)}
	}

	return finalizeResult(raw, wantIDs, repaired, opt)
}

// finalizeResult 把已解析的 raw envelope 转成最终 Result——含 SchemaAliases、translations
// 抽取、glossary 抽取与 wantIDs 完整性比对。提取出来便于 TryRepair 主路径和 merge 早返路径复用。
func finalizeResult(raw map[string]any, wantIDs []string, repaired []string, opt Options) Result {
	if opt.SchemaAliases {
		if normalized, did := normalizeEnvelopeKeys(raw); did {
			raw = normalized
			repaired = append(repaired, "schema.alias")
		}
	}

	transRaw, ok := raw["translations"]
	if !ok {
		return Result{Fatal: true, Repaired: repaired, ParseErr: errors.New("response missing translations field")}
	}
	trans, terr := toStringMap(transRaw)
	if terr != nil {
		return Result{Fatal: true, Repaired: repaired, ParseErr: fmt.Errorf("translations field shape: %w", terr)}
	}

	var glos []prompt.BootstrapEntry
	if glosRaw, ok := raw["glossary"]; ok {
		if b, mErr := json.Marshal(glosRaw); mErr == nil {
			_ = json.Unmarshal(b, &glos)
		}
	}

	var rubyOutput map[string][]ruby.OutputEntry
	if rubyRaw, ok := raw["ruby_output"]; ok {
		if b, mErr := json.Marshal(rubyRaw); mErr == nil {
			_ = json.Unmarshal(b, &rubyOutput)
		}
	}
	if rubyOutput == nil {
		if extracted, did := extractNestedRubyOutput(transRaw); did {
			rubyOutput = extracted
			repaired = append(repaired, "schema.ruby-nested-extract")
		}
	}

	var missing []string
	for _, id := range wantIDs {
		if _, ok := trans[id]; !ok {
			missing = append(missing, id)
		}
	}

	return Result{
		Trans:      trans,
		Glos:       glos,
		RubyOutput: rubyOutput,
		Missing:    missing,
		Repaired:   repaired,
	}
}

// TryRepairBootstrap 解析 bootstrap 响应 {"glossary":[{...},...]}。复用 L1 修复链与
// L2 字段同义化（terms/entries → glossary）。语义与 prompt.ParseBootstrapResponse 一致：
// 过滤空 source/target、按 source 去重保留首次。
//
// 返回 (entries, 修复算子链，error)。
func TryRepairBootstrap(text string, opt Options) ([]prompt.BootstrapEntry, []string, error) {
	var repaired []string
	if opt.JSONStructural {
		cleaned, did := stripBOMAndZeroWidth(text)
		if did {
			repaired = append(repaired, "json.strip-bom-zw")
		}
		text = cleaned
	}

	body := ""
	keys := []string{"glossary"}
	if opt.SchemaAliases {
		keys = append(keys, "terms", "entries")
	}
	for _, k := range keys {
		body = extractJSONObjectContaining(text, k)
		if body != "" {
			break
		}
	}
	if body == "" {
		body = jsonObjectSlice(text)
	}
	if body == "" {
		return nil, repaired, errors.New("no JSON object found in bootstrap response")
	}

	raw, err := unmarshalGeneric(body)
	if err != nil && opt.JSONStructural {
		fixed := body
		if v := fixTrailingCommas(fixed); v != fixed {
			fixed = v
			repaired = append(repaired, "json.trailing-comma")
		}
		if v := escapeControlChars(fixed); v != fixed {
			fixed = v
			repaired = append(repaired, "json.escape-control")
		}
		if v := closeUnbalancedBraces(fixed); v != fixed {
			fixed = v
			repaired = append(repaired, "json.close-braces")
		}
		if fixed != body {
			raw, err = unmarshalGeneric(fixed)
		}
	}
	if err != nil {
		return nil, repaired, fmt.Errorf("unmarshal: %w", err)
	}

	if opt.SchemaAliases {
		if _, hasGlos := raw["glossary"]; !hasGlos {
			for _, alias := range []string{"terms", "entries"} {
				if v, ok := raw[alias]; ok {
					raw["glossary"] = v
					delete(raw, alias)
					repaired = append(repaired, "schema.alias")
					break
				}
			}
		}
	}

	glosRaw, ok := raw["glossary"]
	if !ok {
		return nil, repaired, errors.New("response missing glossary field")
	}
	b, _ := json.Marshal(glosRaw)
	var entries []prompt.BootstrapEntry
	if err := json.Unmarshal(b, &entries); err != nil {
		return nil, repaired, fmt.Errorf("unmarshal glossary: %w", err)
	}
	out := entries[:0]
	seen := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		e.Source = strings.TrimSpace(e.Source)
		e.Target = strings.TrimSpace(e.Target)
		e.Notes = strings.TrimSpace(e.Notes)
		if e.Source == "" || e.Target == "" {
			continue
		}
		if _, dup := seen[e.Source]; dup {
			continue
		}
		seen[e.Source] = struct{}{}
		out = append(out, e)
	}
	return out, repaired, nil
}

// pickEnvelopeBody 从 text 中挑出最可能含 translations 的 JSON 对象。
// SchemaAliases 启用时按候选字段顺序探测；否则只看 "translations"；都找不到回退到首对象。
func pickEnvelopeBody(text string, opt Options) string {
	keys := []string{"translations"}
	if opt.SchemaAliases {
		keys = append(keys, "translation", "result", "output", "results")
	}
	for _, k := range keys {
		if body := extractJSONObjectContaining(text, k); body != "" {
			return body
		}
	}
	return jsonObjectSlice(text)
}

func unmarshalGeneric(body string) (map[string]any, error) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// firstStringKey 按优先级尝试从 m 中取 keys 的 string 值，返回第一个命中的。
// 全部未命中返回空串。
func firstStringKey(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func toStringMap(v any) (map[string]string, error) {
	switch tv := v.(type) {
	case map[string]any:
		out := make(map[string]string, len(tv))
		for k, val := range tv {
			switch sv := val.(type) {
			case string:
				out[k] = sv
			case map[string]any:
				if s := firstStringKey(sv, "target", "translation", "text", "source"); s != "" {
					out[k] = s
				} else {
					return nil, fmt.Errorf("value for key %q is object but no translatable string field found", k)
				}
			default:
				return nil, fmt.Errorf("value for key %q is not string (got %T)", k, val)
			}
		}
		return out, nil
	case []any:
		out := make(map[string]string, len(tv))
		for i, item := range tv {
			obj, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("translations array item %d is not object (got %T)", i, item)
			}
			id, _ := obj["id"].(string)
			if id == "" {
				return nil, fmt.Errorf("translations array item %d missing string \"id\"", i)
			}
			if s := firstStringKey(obj, "target", "translation", "text", "source"); s != "" {
				out[id] = s
			} else {
				return nil, fmt.Errorf("translations array item %d (id=%q) has no translatable string field", i, id)
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("expected object or array, got %T", v)
	}
}

// extractNestedRubyOutput 处理 LLM 将 ruby_output 嵌套在 translations 条目中的情况：
//
//	{"translations":{"1":{"translation":"...","ruby_output":[...]}, ...}}
//
// 提取为顶层 ruby_output map，返回 (提取结果, true)；无需修复时返回 (nil, false)。
func extractNestedRubyOutput(transRaw any) (map[string][]ruby.OutputEntry, bool) {
	transObj, ok := transRaw.(map[string]any)
	if !ok {
		return nil, false
	}
	extracted := make(map[string][]ruby.OutputEntry)
	for id, val := range transObj {
		entry, ok := val.(map[string]any)
		if !ok {
			continue
		}
		rubyRaw, hasRuby := entry["ruby_output"]
		if !hasRuby {
			continue
		}
		rubyArr, ok := rubyRaw.([]any)
		if !ok {
			continue
		}
		b, err := json.Marshal(rubyArr)
		if err != nil {
			continue
		}
		var entries []ruby.OutputEntry
		if err := json.Unmarshal(b, &entries); err != nil {
			continue
		}
		if len(entries) > 0 {
			extracted[id] = entries
		}
	}
	if len(extracted) == 0 {
		return nil, false
	}
	return extracted, true
}
