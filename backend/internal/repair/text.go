package repair

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

// textLineRe 匹配 [N] 开头的翻译行，捕获编号和内容。
var textLineRe = regexp.MustCompile(`^\[(\d+)\]\s*(.*)`)

// TryRepairText 尝试从纯文本 LLM 响应中提取翻译结果。
// 永不返回 error——失败语义通过 Result.Fatal + Result.ParseErr 表达。
//
// 修复链：
//
//	T1 结构清理：剥离 BOM、``` 围栏、首尾非 [N] 文本
//	T3 部分成功：缺失 ID 记入 Missing，不视为 Fatal
func TryRepairText(text string, wantIDs []string, opt Options) Result {
	var repaired []string

	// T1 结构清理
	if opt.JSONStructural {
		cleaned, did := stripBOMAndZeroWidth(text)
		if did {
			repaired = append(repaired, "text.strip-bom-zw")
		}
		text = cleaned
	}

	// 剥离 ``` 围栏
	if stripped := stripCodeFence(text); stripped != text {
		text = stripped
		repaired = append(repaired, "text.strip-code-fence")
	}

	trans, glos, rubyOutput, parseErr := parseTextResponse(text, wantIDs)
	if parseErr != nil {
		// Pool model always accepts best partial: if any translations were parsed,
		// return them without Fatal so the caller can recover missing IDs.
		if len(trans) > 0 {
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
		return Result{Fatal: true, Repaired: repaired, ParseErr: parseErr}
	}

	// 计算 missing
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

// parseTextResponse 解析纯文本格式响应。
// 支持 [glossary] 和 [ruby] 段落。
func parseTextResponse(text string, wantIDs []string) (map[string]string, []prompt.BootstrapEntry, map[string][]ruby.OutputEntry, error) {
	trans := make(map[string]string)
	var glos []prompt.BootstrapEntry
	var rubyOutput map[string][]ruby.OutputEntry

	lines := strings.Split(text, "\n")
	var lastID string
	inGlossary := false
	inRuby := false
	var rubyLines []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if strings.EqualFold(line, "[glossary]") {
			inGlossary = true
			inRuby = false
			continue
		}

		if strings.EqualFold(line, "[ruby]") {
			inRuby = true
			inGlossary = false
			continue
		}

		// 检查 [N] 开头的翻译行（即使在 glossary/ruby 模式下也优先匹配，避免后续翻译丢失）
		if m := textLineRe.FindStringSubmatch(line); m != nil {
			inGlossary = false
			inRuby = false
			lastID = m[1]
			trans[lastID] = m[2]
			continue
		}

		if inGlossary {
			entry := parseGlossaryLine(line)
			if entry != nil {
				glos = append(glos, *entry)
			}
			continue
		}

		if inRuby {
			rubyLines = append(rubyLines, line)
			continue
		}

		if strings.HasPrefix(line, "[*]") {
			continue
		}

		if lastID != "" {
			trans[lastID] += "\n" + line
		}
	}

	if len(rubyLines) > 0 {
		rubyOutput = ruby.ParseSectionRubyOutput(rubyLines)
	} else {
		// 无 [ruby] 段落时，尝试从译文中提取 inline markers
		for id, text := range trans {
			if entries := ruby.ParseInlineMarkers(text); len(entries) > 0 {
				if rubyOutput == nil {
					rubyOutput = make(map[string][]ruby.OutputEntry)
				}
				rubyOutput[id] = entries
			}
		}
	}

	if len(trans) == 0 {
		return nil, nil, nil, errors.New("no translations found in text response")
	}

	var missing []string
	for _, id := range wantIDs {
		if _, ok := trans[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return trans, glos, rubyOutput, fmt.Errorf("missing translations for IDs: %v", missing)
	}

	return trans, glos, rubyOutput, nil
}

// parseGlossaryLine 解析 "source | target | notes" 格式的术语行。
func parseGlossaryLine(line string) *prompt.BootstrapEntry {
	parts := strings.SplitN(line, "|", 3)
	if len(parts) < 2 {
		return nil
	}
	source := strings.TrimSpace(parts[0])
	target := strings.TrimSpace(parts[1])
	if source == "" || target == "" {
		return nil
	}
	entry := &prompt.BootstrapEntry{Source: source, Target: target}
	if len(parts) >= 3 {
		entry.Notes = strings.TrimSpace(parts[2])
	}
	return entry
}

// stripCodeFence 剥离 ```...``` 围栏，返回内部内容。
// 支持首尾有非围栏文本的情况。委托 prompt.StripCodeFence（全仓唯一实现）：
// revise 路径上 [revisions]（prompt 侧解析）与 [ruby]（本包 collectSectionLines
// 收集）必须看到同一归一基准，围栏容错不得双副本漂移。
func stripCodeFence(text string) string {
	return prompt.StripCodeFence(text)
}

// collectSectionLines 收集 text 中 [section] 段落内的非空行（段名大小写不
// 敏感，遇下一个 [xx] 头即终止）。先经 stripCodeFence 剥围栏，保证段落收集
// 与 ParseReviseTextRevisions 的围栏预处理看到同一文本形态，避免同一响应内
// [revisions] 与 [ruby] 的扫描基准分叉。供 revise 轮的 [ruby] 收集使用。
// 注意：parseTextResponse 的扫描循环另有一份 [N] 翻译行优先于段落行的语义
// （防止漏收翻译行），不可直接改用本 helper；修改围栏/段边界容错时两处
// 需同步评估。
func collectSectionLines(text, section string) []string {
	var lines []string
	inSection := false
	for _, line := range strings.Split(stripCodeFence(text), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.EqualFold(line, "["+section+"]") {
			inSection = true
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inSection = false
			continue
		}
		if inSection {
			lines = append(lines, line)
		}
	}
	return lines
}
