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
	JSONStructural       bool // L1：BOM 剥离、尾随逗号、控制字符、括号补齐、多对象合并
	SchemaAliases        bool // L2：字段名同义化（translation→translations 等）
	PlaceholderNormalize bool // L3：占位符大小写/下划线变体归一（与 NormalizePlaceholders 配合）
	PromptUpgrade        bool // L4：解析失败或占位符仍缺失时附加反例 reminder 重试一次

	// BareArrayAccept 是裸数组兜底（json.bare-array）的领域判别器：在通用形状
	// 门控（非空全对象数组）通过后、采纳候选前调用，返回 false 则继续扫描下一
	// 候选。nil 表示无领域判别（仅靠通用门控 + normalize 空结果拒绝）。由
	// TryRepairRubyAlignment/TryRepairAdjudication 等领域入口注入，避免 repair
	// 包硬编码领域 schema。
	BareArrayAccept func(entries []any) bool

	// salvageDeclined 置位表示调用方显式弃用截断抢救（json.truncation-salvage）及
	// 配套的 close-braces 截断恢复（TryRepairBootstrap / TryRepairEnvelope 的
	// body=="" 兜底）。供「部分结果会被下游静默解释为完整语义」的调用方使用
	// （见 WithoutSalvage）。只置位不清位；截断抢救的递归防重入不用本标志而用
	// inSalvage（见 enterSalvage）——递归层仍需 close-braces 兜底补齐截断前缀的
	// 括号缺口。
	salvageDeclined bool

	// inSalvage 置位表示当前调用处于截断抢救的递归层内：抢救分支不再进入，保证
	// 递归深度恒为 1。与 salvageDeclined 刻意分离——递归层不得弃用 close-braces 兜底。
	inSalvage bool
}

// WithoutSalvage 返回禁用截断抢救的 Options 副本。供「部分结果会被下游静默解释为
// 完整语义」的调用方显式弃用（见 TryRepairSemanticQA / TryRepairAdjudication /
// glossary prune）：截断抢救只保证前缀完整、缺失部分走 Missing/重试通道；若下游
// 把「缺」解释为「确认无」（假阴性），或入口没有缺失检测通道（adjudication 的
// partial 会被当作终态成功），部分恢复反而制造错误结论，此时应弃用、维持 Fatal/
// 重试。TryRepairBootstrap 与 TryRepairEnvelope 的 close-braces 截断恢复随本标志
// 一并弃用——fail-closed 对所有截断形态（键/值中途与完整值边界）成立。
func (o Options) WithoutSalvage() Options {
	o.salvageDeclined = true
	return o
}

// enterSalvage 返回截断抢救递归重跑修复链所携带的 Options 副本：置位 inSalvage，
// 递归层不再进入抢救分支（深度恒为 1）。刻意不动 salvageDeclined——递归层的
// close-braces 兜底必须可用，补齐截断前缀的括号缺口是采纳前缀的必要步骤。
func (o Options) enterSalvage() Options {
	o.inSalvage = true
	return o
}

// Result 是 TryRepair 的统一返回。
//
// 状态判定：
//   - Fatal=true：解析完全无救；调用方应走 shrinkOrFallback。ParseErr 非 nil。
//     截断场景引入 json.truncation-salvage 后 Fatal 语义更窄：仅当响应在键/值
//     中途截断且不存在任何可回退的完整值闭合点（或抢救出的前缀仍解析失败）时
//     才 Fatal；有完整前缀时返回 partial（Missing 非空）。
//   - Fatal=false 且 Missing 空：全成功。
//   - Fatal=false 且 Missing 非空：partial（调用方自行处理缺失 ID），
//     包括截断抢救保住的前缀。
type Result struct {
	Trans      map[string]string
	Glos       []prompt.BootstrapEntry
	RubyOutput map[string][]ruby.OutputEntry // segment ID → ruby 输出条目
	Missing    []string                      // wantIDs 中未出现在 Trans 里的子集
	Repaired   []string                      // 修复算子链，便于日志诊断
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
		body, repaired = closeBracesFallback(text, repaired, func(fixed string) string {
			return pickEnvelopeBody(fixed, opt)
		})
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
		if v := escapeUnescapedQuotes(fixed); v != fixed {
			fixed = v
			repaired = append(repaired, "json.escape-quotes")
		}
		if v := closeUnbalancedBraces(fixed); v != fixed {
			fixed = v
			repaired = append(repaired, "json.close-braces")
		}
		if fixed != body {
			raw, err = unmarshalGeneric(fixed)
		}
	}

	// 鲁棒兜底：当 LLM 产生"结巴/重复前缀"（如 {"{"ruby_output":...}}）或任意
	// 引号/括号相位错乱噪声时，matchBracePair 字符串追踪会失步，pickEnvelopeBody
	// 拿不到可解析 body。此处从每个 '{' 偏移用 json.Decoder 真实解析（容忍尾部数据），
	// 恢复内嵌的合法对象。只接受含 "translations" 的对象，避免把诱饵当 envelope。
	if err != nil && opt.JSONStructural {
		if robust, ok := extractValidEnvelope(text); ok {
			repaired = append(repaired, "json.robust-extract")
			return finalizeResult(robust, wantIDs, repaired, opt)
		}
	}

	// 截断抢救（末环）：响应被 max_tokens 截断在键/值中途（未闭合引号、悬空键）
	// 时，此前的算子均无法补齐字符串级缺口，整批 Fatal 会丢弃截断点之前的完整
	// 译文。回退到最后一个完整值闭合点、丢弃残尾，用截断前缀递归重跑整条修复链；
	// 仅当递归产出 Fatal=false 的结果才采纳（Fatal=false 且 Missing 非空的 partial
	// 同样采纳——缺失 ID 由调用方经 Missing/重试通道处理，这正是部分恢复）。
	// 安全门与残留风险见 truncateToLastCompleteValue 的权威注释（json.go）。
	if err != nil {
		if cut, ok := salvageCut(text, opt); ok {
			if inner := TryRepair(cut, wantIDs, opt.enterSalvage()); !inner.Fatal {
				inner.Repaired = prependSalvageOp(repaired, inner.Repaired)
				return inner
			}
		}
	}

	if err != nil {
		if body == "" {
			return Result{Fatal: true, Repaired: repaired, ParseErr: errors.New("no JSON object found")}
		}
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
	if body == "" && opt.JSONStructural && !opt.salvageDeclined {
		// 兜底：text 可能整体未闭合（截断），尝试补齐括号再抽。守卫与截断抢救
		// 同口径：显式弃用抢救的调用方（glossary prune）在 close-braces 可补齐的
		// 截断形态下也维持报错重试，不做部分恢复——截断缺口会被 computePruneDiff
		// 解释为「建议删除」；抢救递归（inSalvage）不受影响，补齐截断前缀的括号
		// 缺口是采纳前缀的必要步骤。字符串未闭合时本兜底无效（close-braces 不补
		// 引号），由下方截断抢救按完整值边界回退再重试。
		body, repaired = closeBracesFallback(text, repaired, func(fixed string) string {
			for _, k := range keys {
				if b := extractJSONObjectContaining(fixed, k); b != "" {
					return b
				}
			}
			return jsonObjectSlice(fixed)
		})
	}
	if body == "" {
		// 截断抢救（末环）：响应被截断在键/值中途时，回退到最后一个完整值闭合点、
		// 丢弃残尾，用截断前缀递归重跑整条修复链；递归内 close-braces 兜底补齐
		// 截断前缀的括号缺口。采纳逻辑收敛于 salvageBootstrapPrefix 单点。
		if entries2, repaired2, ok := salvageBootstrapPrefix(text, repaired, opt); ok {
			return entries2, repaired2, nil
		}
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
		if v := escapeUnescapedQuotes(fixed); v != fixed {
			fixed = v
			repaired = append(repaired, "json.escape-quotes")
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
		// 截断抢救（末环）：body 已抽出但连同修复链仍无法解析的形态（如值内非法
		// 转义+截断双故障，见 TestTryRepairBootstrap_TruncationSalvage_BrokenBody），
		// 回退到最后一个完整值闭合点、丢弃残尾，递归重跑整条修复链。采纳逻辑
		// 收敛于 salvageBootstrapPrefix 单点。
		if entries2, repaired2, ok := salvageBootstrapPrefix(text, repaired, opt); ok {
			return entries2, repaired2, nil
		}
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

// salvageBootstrapPrefix 是 TryRepairBootstrap 两处截断抢救末环（body=="" 与
// body 已抽出但修复链仍失败）的共享实现：经 salvageCut 判定并截取前缀后，递归
// 重跑整条修复链（enterSalvage 保证深度=1，递归内 close-braces 兜底补齐前缀的
// 括号缺口），err==nil 即采纳并前插抢救算子；不可抢救时返回 ok=false。
// 安全门与残留风险见 truncateToLastCompleteValue 的权威注释（json.go）。
func salvageBootstrapPrefix(text string, repaired []string, opt Options) ([]prompt.BootstrapEntry, []string, bool) {
	if cut, ok := salvageCut(text, opt); ok {
		if entries, inner, err := TryRepairBootstrap(cut, opt.enterSalvage()); err == nil {
			return entries, prependSalvageOp(repaired, inner), true
		}
	}
	return nil, nil, false
}

// envelopeKeys 返回判定 envelope 所用的候选字段名：始终含 "translations"，
// SchemaAliases 启用时追加常见同义字段。
func envelopeKeys(opt Options) []string {
	keys := []string{"translations"}
	if opt.SchemaAliases {
		keys = append(keys, "translation", "result", "output", "results")
	}
	return keys
}

// pickEnvelopeBody 从 text 中挑出最可能含 translations 的 JSON 对象。
// SchemaAliases 启用时按候选字段顺序探测；否则只看 "translations"；都找不到回退到首对象。
func pickEnvelopeBody(text string, opt Options) string {
	for _, k := range envelopeKeys(opt) {
		if body := extractJSONObjectContaining(text, k); body != "" {
			return body
		}
	}
	return jsonObjectSlice(text)
}

// closeBracesFallback body 抽取失败（text 可能整体未闭合/截断）时的共享兜底：
// 尝试补齐括号后按 extract 重新抽取 envelope 主体，成功时记录 json.close-braces
// 算子并返回 (body, 更新后的算子链)；失败返回 ("", 原算子链)。
// TryRepair / TryRepairEnvelope / TryRepairBootstrap 的 body=="" 兜底共用此骨架，
// 抽取策略由各入口以闭包传入——TryRepairEnvelope 刻意只探单 key、不带
// jsonObjectSlice 回退，以防误收同名异形包装（见其 doc），勿向其抽取闭包加回退。
func closeBracesFallback(text string, repaired []string, extract func(string) string) (string, []string) {
	fixed := closeUnbalancedBraces(text)
	if fixed == text {
		return "", repaired
	}
	if body := extract(fixed); body != "" {
		return body, append(repaired, "json.close-braces")
	}
	return "", repaired
}

// salvageCut 是各修复入口截断抢救分支的共享守卫：集中判定 JSONStructural 开启、
// 抢救未被显式弃用（salvageDeclined）、不在抢救递归层内（inSalvage——防无限递归
// 的唯一闸门）三个不变量，再回退到最后一个完整值闭合点。通过后返回 (截断前缀, true)。
// 安全门与残留风险见 truncateToLastCompleteValue 的权威注释（json.go）。
func salvageCut(text string, opt Options) (string, bool) {
	if !opt.JSONStructural || opt.salvageDeclined || opt.inSalvage {
		return "", false
	}
	return truncateToLastCompleteValue(text)
}

// prependSalvageOp 把 json.truncation-salvage 算子前插进外层算子链，再接上递归层
// 产出的内层算子链——算子链按应用顺序记录，外层算子在前、抢救递归在内。
func prependSalvageOp(outer, inner []string) []string {
	return append(append(outer, "json.truncation-salvage"), inner...)
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
