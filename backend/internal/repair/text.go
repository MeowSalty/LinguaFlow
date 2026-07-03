package repair

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
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

	trans, glos, parseErr := parseTextResponse(text, wantIDs)
	if parseErr != nil {
		// T3 部分成功：即使有解析错误，也检查是否有部分结果
		if len(trans) > 0 && opt.Partial {
			var missing []string
			for _, id := range wantIDs {
				if _, ok := trans[id]; !ok {
					missing = append(missing, id)
				}
			}
			if float64(len(missing))/float64(len(wantIDs)) < opt.PartialThreshold {
				return Result{
					Trans:    trans,
					Glos:     glos,
					Missing:  missing,
					Repaired: repaired,
				}
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
		Trans:    trans,
		Glos:     glos,
		Missing:  missing,
		Repaired: repaired,
	}
}

// parseTextResponse 解析纯文本格式响应。
func parseTextResponse(text string, wantIDs []string) (map[string]string, []prompt.BootstrapEntry, error) {
	trans := make(map[string]string)
	var glos []prompt.BootstrapEntry

	lines := strings.Split(text, "\n")
	var lastID string
	inGlossary := false

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if strings.EqualFold(line, "[glossary]") {
			inGlossary = true
			continue
		}

		// 检查 [N] 开头的翻译行（即使在 glossary 模式下也优先匹配，避免后续翻译丢失）
		if m := textLineRe.FindStringSubmatch(line); m != nil {
			inGlossary = false
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

		if strings.HasPrefix(line, "[*]") {
			continue
		}

		if lastID != "" {
			trans[lastID] += "\n" + line
		}
	}

	if len(trans) == 0 {
		return nil, nil, errors.New("no translations found in text response")
	}

	var missing []string
	for _, id := range wantIDs {
		if _, ok := trans[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return trans, glos, fmt.Errorf("missing translations for IDs: %v", missing)
	}

	return trans, glos, nil
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
// 支持首尾有非围栏文本的情况。
func stripCodeFence(text string) string {
	text = strings.TrimSpace(text)
	// 找到第一个 ```
	start := strings.Index(text, "```")
	if start < 0 {
		return text
	}
	// 跳过 ``` 和可选的语言标识符
	afterStart := text[start+3:]
	if idx := strings.IndexByte(afterStart, '\n'); idx >= 0 {
		afterStart = afterStart[idx+1:]
	} else {
		return text
	}
	// 找到最后一个 ```
	end := strings.LastIndex(afterStart, "```")
	if end < 0 {
		return strings.TrimSpace(afterStart)
	}
	return strings.TrimSpace(afterStart[:end])
}
