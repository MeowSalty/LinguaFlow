package repair

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

// TryRepairEnvelope 修复单键数组 JSON envelope：{"<key>": [<...>]}。
//
// 覆盖 semantic_qa({"issues":[...]})、adjudicate({"verdicts":[...]})、
// ruby alignment({"ruby_output":[...]}) 等 LLM 响应。这些响应与 translate envelope
// 不同：值是数组而非 ID-keyed map，因此不做 wantIDs 校验、不做多对象合并。
//
// 修复链与 TryRepair 的 L1 结构修复一致（去 BOM/零宽、补齐括号、尾随逗号、控制字符
// 转义、结巴/重复前缀鲁棒抽取），但 key 无关、且要求 key 的值为 JSON 数组--后者用于
// 在"补齐括号后得到同名外层包装"（如 {"issues":{"issues":[]}}）时跳过错误候选，
// 继而由 extractValidObject 找到内层真正的数组 envelope。
//
// 返回 (顶层 map, 修复算子链, error)。零值 Options 行为等同旧 jsonObjectSlice 路径
// （基础抽取 + unmarshal，不做结构修复）。
func TryRepairEnvelope(text, key string, opt Options) (map[string]any, []string, error) {
	var repaired []string
	accept := arrayValueAccept(key)

	if opt.JSONStructural {
		cleaned, did := stripBOMAndZeroWidth(text)
		if did {
			repaired = append(repaired, "json.strip-bom-zw")
		}
		text = cleaned
	}

	body := extractJSONObjectContaining(text, key)
	if body == "" && opt.JSONStructural && !opt.salvageDeclined {
		// 兜底：text 可能整体未闭合（截断），尝试补齐括号再抽。守卫与截断抢救
		// 同口径：显式弃用抢救的入口（semantic_qa/adjudication——部分结果会被下游
		// 静默解释为完整语义）在 close-braces 可补齐的截断形态下也维持报错重试，
		// fail-closed 对所有截断形态成立；抢救递归（inSalvage，salvageDeclined=
		// false）不受影响，补齐截断前缀的括号缺口是采纳前缀的必要步骤。
		// 抽取闭包刻意只探单 key、不带 jsonObjectSlice 回退（防误收同名异形包装，
		// 见 closeBracesFallback 说明）。
		body, repaired = closeBracesFallback(text, repaired, func(fixed string) string {
			return extractJSONObjectContaining(fixed, key)
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

	// 主链路解析成功但 key 值非数组（如补齐括号后得到 {"issues":{"issues":[]}}）：
	// 视为失败以触发鲁棒兜底，找到内层真正的数组 envelope。
	if err == nil && !accept(raw) {
		err = fmt.Errorf("%q field is not an array", key)
		raw = nil
	}

	// 鲁棒兜底：结巴/重复前缀或括号相位错乱时，matchBracePair 字符串追踪失步，
	// extractJSONObjectContaining 拿不到可解析 body。此处从每个 '{' 偏移用
	// json.Decoder 真实解析，恢复内嵌的合法数组 envelope。closeBracesPatch 与
	// 截断抢救同口径：salvageDeclined 时关闭截断残尾的修补采纳——「完整值边界
	// 截断」的残尾不会被补括号后误采纳（fail-closed 对所有截断形态成立）；
	// 结巴/重复前缀等非截断噪声靠原文解码命中，不受影响。
	if err != nil && opt.JSONStructural {
		if robust, ok := extractValidObject(text, key, accept, !opt.salvageDeclined); ok {
			repaired = repaired[:0]
			repaired = append(repaired, "json.robust-extract")
			return robust, repaired, nil
		}
	}

	// 裸数组兜底：LLM 有时丢掉外层信封，直接返回 "[{...},{...}]"（仅缺
	// {"key": ...} 包装）。主链路只扫 '{'，对这种响应必然落入 "no JSON object
	// found"。此处把首个通过"通用形状门控（非空全对象）+ opt.BareArrayAccept
	// 领域判别"的可解码数组重新包装为 {key: [...]}；元素级校验由调用方的
	// typed unmarshal + normalize 判定，兜底命中但 normalize 后零条目视为误
	// 采纳、报错重试（bareArrayNoEntriesError），维持"宁可重试也不凑错"原则。
	// salvageDeclined 时关闭 close-braces 修补采纳（与截断抢救同口径）：
	// 「完整值边界截断」的残尾会被补括号后误当作裸数组，构成对弃用承诺的旁路；
	// 真裸数组（原文可完整解码）不受影响。
	if err != nil && opt.JSONStructural {
		if wrapped, ok := extractBareArrayAsEnvelope(text, key, opt.BareArrayAccept, !opt.salvageDeclined); ok {
			repaired = append(repaired[:0], "json.bare-array")
			return wrapped, repaired, nil
		}
	}

	// 截断抢救（末环）：响应被截断在键/值中途（未闭合引号、悬空键）时，此前的
	// 算子均无法补齐字符串级缺口，最终报错会丢弃截断点之前的完整条目。回退到
	// 最后一个完整值闭合点、丢弃残尾，用截断前缀递归重跑整条修复链（enterSalvage
	// 保证深度=1）；err2==nil 即采纳。天然覆盖 revise(revisions)/ruby(ruby_output)；
	// adjudicate(verdicts)/semantic_qa(issues) 在各自入口显式弃用（无缺失重跑通道/
	// 假阴性质检风险）。安全门与残留风险见 truncateToLastCompleteValue 的权威注释（json.go）。
	if err != nil {
		if cut, ok := salvageCut(text, opt); ok {
			if raw2, repaired2, err2 := TryRepairEnvelope(cut, key, opt.enterSalvage()); err2 == nil {
				return raw2, prependSalvageOp(repaired, repaired2), nil
			}
		}
	}

	if err != nil {
		if body == "" {
			return nil, repaired, errors.New("no JSON object found")
		}
		return nil, repaired, fmt.Errorf("unmarshal: %w", err)
	}
	return raw, repaired, nil
}

// arrayValueAccept 返回要求 key 的值为 JSON 数组的谓词。用于 TryRepairEnvelope
// 在结巴/截断场景下跳过同名但形状错误的外层包装候选。
func arrayValueAccept(key string) func(map[string]any) bool {
	return func(m map[string]any) bool {
		_, ok := m[key].([]any)
		return ok
	}
}

// tryRepairKeyed 是 TryRepairSemanticQA/TryRepairAdjudication/TryRepairRubyAlignment
// 的共享骨架：TryRepairEnvelope 取出含 key 的数组 envelope，再 marshal→unmarshal
// 到具体类型切片，最后由 normalize 做 trim/过滤。
func tryRepairKeyed[T any](text, key string, opt Options, normalize func([]T) []T) ([]T, []string, error) {
	raw, repaired, err := TryRepairEnvelope(text, key, opt)
	if err != nil {
		return nil, repaired, err
	}
	v, err := decodeKeyed(raw, key, normalize)
	if err != nil {
		return nil, repaired, err
	}
	if err := bareArrayNoEntriesError(v, repaired, key); err != nil {
		return nil, repaired, err
	}
	return v, repaired, nil
}

// bareArrayNoEntriesError 裸数组兜底命中但 normalize 后零条目：说明数组元素缺
// 目标类型的必需字段，兜底误采纳了无关数组（噪声中的空数组/异构数组等），返回
// 错误以走重试路径。正常 envelope（含合法空数组）不经过裸数组兜底，不受影响。
func bareArrayNoEntriesError[T any](entries []T, repaired []string, key string) error {
	if len(entries) > 0 || !slices.Contains(repaired, "json.bare-array") {
		return nil
	}
	return fmt.Errorf("bare array adopted as %q but no entries survived normalize", key)
}

// decodeKeyed 从已解析的 envelope map 中取出 key 的数组值并反序列化到具体类型
// 切片，经 normalize 完成 trim/过滤。
func decodeKeyed[T any](raw map[string]any, key string, normalize func([]T) []T) ([]T, error) {
	v, ok := raw[key]
	if !ok {
		return nil, errors.New("response missing " + key + " field")
	}
	b, mErr := json.Marshal(v)
	if mErr != nil {
		return nil, fmt.Errorf("marshal %s: %w", key, mErr)
	}
	var typed []T
	if err := json.Unmarshal(b, &typed); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", key, err)
	}
	return normalize(typed), nil
}

// TryRepairSemanticQA 解析 {"issues":[...]} 并应用完整修复链。
// 返回 (issues, 修复算子链, error)；issues 已经过 trim 与 code 过滤（与
// prompt.ParseSemanticQATextIssues 的 code 白名单语义一致）。
//
// 截断抢救对本入口固有危险（fail-closed，任何调用方都不应默认开启）：issues
// envelope 的部分结果会被下游解释为「缺失段=已扫描无问题」——截断抢救把残尾
// 丢弃后返回的 partial，恰好像一份「只报了前半段问题」的完整质检报告，形成
// 假阴性质检。故此处无条件弃用（WithoutSalvage，连同 close-braces 截断恢复），
// 截断响应（键/值中途与完整值边界两种形态）均维持报错重试；确有需要自行决策
// 部分恢复的调用方可直接使用 TryRepairEnvelope（其 close-braces 兜底不门控）。
func TryRepairSemanticQA(text string, opt Options) ([]prompt.SemanticQAIssue, []string, error) {
	opt = opt.WithoutSalvage()
	return tryRepairKeyed(text, "issues", opt, prompt.NormalizeSemanticQAIssues)
}

// ParseSemanticQAByMode 按 response mode 解析语义质检响应（带修复）。
// text 模式优先纯文本 [issues] 协议，未识别时 fallback JSON（模型常仍吐 JSON）。
// 返回 (issues, 修复算子链, error)；text 路径修复算子链为空。
func ParseSemanticQAByMode(text string, isTextMode bool, opt Options) ([]prompt.SemanticQAIssue, []string, error) {
	if !isTextMode {
		return TryRepairSemanticQA(text, opt)
	}
	issues, recognized := prompt.ParseSemanticQATextIssues(text)
	if recognized {
		return issues, nil, nil
	}
	return TryRepairSemanticQA(text, opt)
}

// ParseReviseByMode 按 response mode 解析修订响应（带修复）。
// text 模式优先纯文本 [revisions] 协议，未识别时 fallback JSON（模型常仍吐 JSON）。
// 当响应携带注音对齐输出时一并解析返回（JSON 顶层 ruby_output；text 模式 [ruby]
// 段落），协议未携带时为 nil（无注音条目或 LLM 漏返，由调用方按空对齐处理）。
// 返回 (revisions, rubyOutput, 修复算子链, error)；text 路径修复算子链为空。
func ParseReviseByMode(text string, isTextMode bool, opt Options) ([]prompt.ReviseRevision, map[string][]ruby.OutputEntry, []string, error) {
	if !isTextMode {
		return tryRepairReviseWithRuby(text, opt)
	}
	revisions, rubyOutput, recognized := parseReviseTextWithRuby(text)
	if recognized {
		return revisions, rubyOutput, nil, nil
	}
	return tryRepairReviseWithRuby(text, opt)
}

// tryRepairReviseWithRuby 解析 {"revisions":[...]} 并提取可选的顶层 ruby_output。
func tryRepairReviseWithRuby(text string, opt Options) ([]prompt.ReviseRevision, map[string][]ruby.OutputEntry, []string, error) {
	raw, repaired, err := TryRepairEnvelope(text, "revisions", opt)
	if err != nil {
		return nil, nil, repaired, err
	}
	revisions, err := decodeKeyed(raw, "revisions", prompt.NormalizeReviseRevisions)
	if err != nil {
		return nil, nil, repaired, err
	}
	if err := bareArrayNoEntriesError(revisions, repaired, "revisions"); err != nil {
		return nil, nil, repaired, err
	}
	var rubyOutput map[string][]ruby.OutputEntry
	if rubyRaw, ok := raw["ruby_output"]; ok {
		if b, mErr := json.Marshal(rubyRaw); mErr == nil {
			_ = json.Unmarshal(b, &rubyOutput)
		}
	}
	return revisions, rubyOutput, repaired, nil
}

// parseReviseTextWithRuby 解析 text 协议修订输出：[revisions] 段落必选，其后可选
// [ruby] 段落（每行 "段id: base | text | kind[ | 条目id]"，复用 section 解析）。
// recognized=false 表示未命中 [revisions] 协议，调用方据此 fallback JSON。
func parseReviseTextWithRuby(text string) ([]prompt.ReviseRevision, map[string][]ruby.OutputEntry, bool) {
	revisions, recognized := prompt.ParseReviseTextRevisions(text)
	if !recognized {
		return nil, nil, false
	}
	var rubyOutput map[string][]ruby.OutputEntry
	if rubyLines := collectSectionLines(text, "ruby"); len(rubyLines) > 0 {
		rubyOutput = ruby.ParseSectionRubyOutput(rubyLines)
	}
	return revisions, rubyOutput, true
}

// 返回 (verdicts, 修复算子链, error)；verdicts 已经过 trim 与字段过滤（丢弃缺
// id/issue_code 的条目，语义与 prompt.ParseAdjudicationTextVerdicts 一致）。
//
// 截断抢救对本入口固有危险：adjudicate 的成功路径对所有批次段一律 SegmentDone 并
// 返回终态成功，无「缺失 verdict → 重跑」通道——抢救出的 partial 会被计为已裁决，
// 缺失 verdict 的段经 ResolvedIndices 永久跳过、不再送 LLM 裁决。故与
// TryRepairSemanticQA 同口径无条件弃用（WithoutSalvage，连同 close-braces 截断
// 恢复），截断响应（键/值中途与完整值边界两种形态）均维持报错，由 handler 走
// unresolved → 下一池整批重试。
func TryRepairAdjudication(text string, opt Options) ([]prompt.AdjudicationVerdict, []string, error) {
	opt.BareArrayAccept = prompt.ValidBareVerdictEntries
	opt = opt.WithoutSalvage()
	return tryRepairKeyed(text, "verdicts", opt, prompt.NormalizeAdjudicationVerdicts)
}

// ParseAdjudicationByMode 按 response mode 解析裁决响应（带修复）。
// text 模式优先纯文本 [verdicts] 协议，命中协议（含空列表）即返回；未命中才 fallback JSON。
// 返回 (verdicts, 修复算子链, error)；text 路径修复算子链为空。
func ParseAdjudicationByMode(text string, isTextMode bool, opt Options) ([]prompt.AdjudicationVerdict, []string, error) {
	if !isTextMode {
		return TryRepairAdjudication(text, opt)
	}
	verdicts, recognized := prompt.ParseAdjudicationTextVerdicts(text)
	if recognized {
		return verdicts, nil, nil
	}
	return TryRepairAdjudication(text, opt)
}

// TryRepairRubyAlignment 解析 {"ruby_output":[...]} 并应用完整修复链。
// 返回 (entries, 修复算子链, error)；失败时 entries 为 nil。
func TryRepairRubyAlignment(text string, opt Options) ([]ruby.OutputEntry, []string, error) {
	opt.BareArrayAccept = ruby.ValidBareOutputEntries
	return tryRepairKeyed(text, "ruby_output", opt, ruby.NormalizeOutputEntries)
}
