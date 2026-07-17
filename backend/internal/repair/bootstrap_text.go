package repair

import (
	"errors"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
)

// ErrBootstrapTextEmpty 表示 text 模式响应无可解析术语，且缺少显式 [glossary] 空协议。
// 精简路径用此错误避免把乱输出当成「全删」。
var ErrBootstrapTextEmpty = errors.New("empty bootstrap text without [glossary] header")

// TryRepairBootstrapText 从纯文本 LLM 响应中解析术语抽取/精简结果。
// 输出协议：
//
//	[glossary]
//	source | target | notes
//
// 若无 [glossary] header，回退扫描全文中符合 parseGlossaryLine 的行。
// 零条目也成功返回（空列表），与 JSON 路径 {"glossary":[]} 语义一致。
// 返回 (entries, 修复算子链, error)。error 仅在内部逻辑异常时返回；正常空结果不报错。
func TryRepairBootstrapText(text string, opt Options) ([]prompt.BootstrapEntry, []string, error) {
	entries, repaired, _, err := tryRepairBootstrapText(text, opt)
	return entries, repaired, err
}

// ParseBootstrapByMode 按 response mode 解析抽取/精简响应。
// text 模式优先纯文本协议，失败或空列表时 fallback JSON（模型常仍吐 JSON）。
// requireHeaderForEmpty：text 路径得到空列表且无 [glossary] 头、JSON 也失败时返回 ErrBootstrapTextEmpty。
// 精简应传 true（空=全删）；抽取可传 false（空=无术语）。
func ParseBootstrapByMode(text string, isTextMode bool, opt Options, requireHeaderForEmpty bool) ([]prompt.BootstrapEntry, []string, error) {
	if !isTextMode {
		return TryRepairBootstrap(text, opt)
	}

	entries, repaired, hasHeader, err := tryRepairBootstrapText(text, opt)
	if err == nil && len(entries) > 0 {
		return entries, repaired, nil
	}

	jsonEntries, jsonRepaired, jsonErr := TryRepairBootstrap(text, opt)
	if jsonErr == nil {
		ops := append(repaired, jsonRepaired...)
		if len(entries) == 0 && len(jsonEntries) > 0 {
			ops = append(ops, "text.fallback-json")
		}
		return jsonEntries, ops, nil
	}

	if err == nil {
		if len(entries) == 0 && requireHeaderForEmpty && !hasHeader {
			return nil, repaired, ErrBootstrapTextEmpty
		}
		return entries, repaired, nil
	}
	return nil, repaired, err
}

func tryRepairBootstrapText(text string, opt Options) ([]prompt.BootstrapEntry, []string, bool, error) {
	var repaired []string

	if opt.JSONStructural {
		cleaned, did := stripBOMAndZeroWidth(text)
		if did {
			repaired = append(repaired, "text.strip-bom-zw")
		}
		text = cleaned
	}

	if stripped := stripCodeFence(text); stripped != text {
		text = stripped
		repaired = append(repaired, "text.strip-code-fence")
	}

	entries, hasHeader := parseBootstrapTextGlossary(text)
	return entries, repaired, hasHeader, nil
}

// parseBootstrapTextGlossary 解析 [glossary] 段或全文中的术语行，去重保留首次。
// 返回 hasHeader 表示是否出现过 [glossary] 标记。
func parseBootstrapTextGlossary(text string) ([]prompt.BootstrapEntry, bool) {
	lines := strings.Split(text, "\n")
	inGlossary := false
	hasHeader := false
	var raw []prompt.BootstrapEntry

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.EqualFold(line, "[glossary]") {
			inGlossary = true
			hasHeader = true
			continue
		}
		// 遇到其他 section header 则退出 glossary 段
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			if hasHeader {
				inGlossary = false
			}
			continue
		}
		if hasHeader && !inGlossary {
			continue
		}
		entry := parseGlossaryLine(line)
		if entry != nil {
			raw = append(raw, *entry)
		}
	}

	return dedupeBootstrapEntries(raw), hasHeader
}

// dedupeBootstrapEntries 过滤空 source/target，按 source 去重保留首次。
func dedupeBootstrapEntries(entries []prompt.BootstrapEntry) []prompt.BootstrapEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]prompt.BootstrapEntry, 0, len(entries))
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
	return out
}
