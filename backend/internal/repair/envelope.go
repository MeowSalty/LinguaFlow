package repair

import (
	"encoding/json"
	"errors"
	"fmt"

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
	if body == "" && opt.JSONStructural {
		// 兜底：text 可能整体未闭合，尝试补齐括号再抽。
		if fixed := closeUnbalancedBraces(text); fixed != text {
			body = extractJSONObjectContaining(fixed, key)
			if body != "" {
				repaired = append(repaired, "json.close-braces")
			}
		}
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
	// json.Decoder 真实解析，恢复内嵌的合法数组 envelope。
	if err != nil && opt.JSONStructural {
		if robust, ok := extractValidObject(text, key, accept); ok {
			repaired = repaired[:0]
			repaired = append(repaired, "json.robust-extract")
			return robust, repaired, nil
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
	v, ok := raw[key]
	if !ok {
		return nil, repaired, errors.New("response missing " + key + " field")
	}
	b, mErr := json.Marshal(v)
	if mErr != nil {
		return nil, repaired, fmt.Errorf("marshal %s: %w", key, mErr)
	}
	var typed []T
	if err := json.Unmarshal(b, &typed); err != nil {
		return nil, repaired, fmt.Errorf("unmarshal %s: %w", key, err)
	}
	return normalize(typed), repaired, nil
}

// TryRepairSemanticQA 解析 {"issues":[...]} 并应用完整修复链。
// 返回 (issues, 修复算子链, error)；issues 已经过 trim 与 code 过滤（与
// prompt.ParseSemanticQATextIssues 的 code 白名单语义一致）。
func TryRepairSemanticQA(text string, opt Options) ([]prompt.SemanticQAIssue, []string, error) {
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

// TryRepairRevise 解析 {"revisions":[...]} 并应用完整修复链。
// 返回 (revisions, 修复算子链，error)；revisions 已经过 trim、空字段过滤与重复
// id 去重（保留首次出现）。修复层不校验 id 是否属于请求批次，越界 id 由调用方丢弃。
func TryRepairRevise(text string, opt Options) ([]prompt.ReviseRevision, []string, error) {
	return tryRepairKeyed(text, "revisions", opt, prompt.NormalizeReviseRevisions)
}

// ParseReviseByMode 按 response mode 解析修订响应（带修复）。
// text 模式优先纯文本 [revisions] 协议，未识别时 fallback JSON（模型常仍吐 JSON）。
// 返回 (revisions, 修复算子链, error)；text 路径修复算子链为空。
func ParseReviseByMode(text string, isTextMode bool, opt Options) ([]prompt.ReviseRevision, []string, error) {
	if !isTextMode {
		return TryRepairRevise(text, opt)
	}
	revisions, recognized := prompt.ParseReviseTextRevisions(text)
	if recognized {
		return revisions, nil, nil
	}
	return TryRepairRevise(text, opt)
}

// 返回 (verdicts, 修复算子链, error)；verdicts 已经过 trim 与字段过滤（丢弃缺
// id/issue_code 的条目，语义与 prompt.ParseAdjudicationTextVerdicts 一致）。
func TryRepairAdjudication(text string, opt Options) ([]prompt.AdjudicationVerdict, []string, error) {
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
	return tryRepairKeyed(text, "ruby_output", opt, ruby.NormalizeOutputEntries)
}
