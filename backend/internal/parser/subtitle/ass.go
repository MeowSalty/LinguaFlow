package subtitle

import (
	"bufio"
	"regexp"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/hash"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

// ---------------------------------------------------------------------------
// ASS
// ---------------------------------------------------------------------------

// assSectionRE 匹配 ASS 节标题，如 [Events]。
var assSectionRE = regexp.MustCompile(`^\[([^\]]+)\]$`)

// assFormatRE 匹配 ASS 事件节中的 Format 行。
var assFormatRE = regexp.MustCompile(`^(?:Format|format):\s*(.*)$`)

// assDialogueRE 匹配 ASS 对话行（Dialogue 或 Comment）。
var assDialogueRE = regexp.MustCompile(`^(Dialogue|Comment):\s*(.*)$`)

func parseASS(content string) (*pipeline.Document, error) {
	lines := strings.Split(content, "\n")

	var segs []pipeline.Segment

	// 非 Dialogue 行的累积缓冲区（头部、样式、注释等）
	var buf strings.Builder
	flushBuf := func() {
		if buf.Len() == 0 {
			return
		}
		s := strings.TrimRight(buf.String(), "\n")
		if s != "" {
			segs = append(segs, pipeline.Segment{
				ID:     hash.Short(s),
				Source: s,
				Skip:   true,
			})
		}
		buf.Reset()
	}

	// 字段索引：在 Events 节中通过 Format 行确定各字段位置
	// 标准 Events Format: Layer, Start, End, Style, Actor, MarginL, MarginR, MarginZ, Effect, Text
	// Text 总是末位字段
	textFieldIdx := -1

	for _, raw := range lines {
		line := raw
		_ = line

		// 检查是否进入 Events 节
		m := assSectionRE.FindStringSubmatch(strings.TrimSpace(raw))
		if m != nil {
			sectionName := m[1]
			if sectionName == "Events" {
				// 在新的 Events 节开头，重置字段索引
				textFieldIdx = -1
			} else {
				// 切换出 Events 节时，刷新缓冲区
				if textFieldIdx >= 0 {
					flushBuf()
				}
				textFieldIdx = -2 // 非 Events 节标记
			}
			buf.WriteString(raw)
			buf.WriteByte('\n')
			continue
		}

		// 非 Events 节 → 全部进缓冲区
		if textFieldIdx == -2 {
			buf.WriteString(raw)
			buf.WriteByte('\n')
			continue
		}

		// Events 节内
		// 检查 Format 行
		fm := assFormatRE.FindStringSubmatch(strings.TrimSpace(raw))
		if fm != nil {
			textFieldIdx = findTextFieldIndex(fm[1])
			buf.WriteString(raw)
			buf.WriteByte('\n')
			continue
		}

		// 仍然在 Events 节，但尚未看到 Format 行
		if textFieldIdx < 0 {
			buf.WriteString(raw)
			buf.WriteByte('\n')
			continue
		}

		// Events 节中非 Dialogue/非空行
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			buf.WriteByte('\n')
			continue
		}

		dm := assDialogueRE.FindStringSubmatch(trimmed)
		if dm == nil {
			// 非 Dialogue 行（如 Picture、Map 等）
			buf.WriteString(raw)
			buf.WriteByte('\n')
			continue
		}

		lineType := dm[1] // "Dialogue" 或 "Comment"
		body := dm[2]

		if lineType == "Comment" {
			buf.WriteString(raw)
			buf.WriteByte('\n')
			continue
		}

		// Dialogue 行 → 创建翻译段
		flushBuf()

		prefix, text := splitASSFields(body, textFieldIdx)
		if text == "" {
			// 无文本的 Dialogue 行，保留原样
			segs = append(segs, pipeline.Segment{
				ID:     hash.Short(raw),
				Source: raw,
				Skip:   true,
			})
			continue
		}

		segs = append(segs, pipeline.Segment{
			ID:     hash.Short(text),
			Source: text,
			Meta: map[string]any{
				"type":           "ass",
				"dialogue_start": "Dialogue: " + prefix,
			},
		})
	}

	// 尾部非 Dialogue 内容
	flushBuf()

	if len(segs) == 0 {
		return &pipeline.Document{Format: "ass"}, nil
	}

	return &pipeline.Document{Segments: segs, Format: "ass"}, nil
}

// findTextFieldIndex 从 ASS Format 行内容中定位 Text 字段的索引（0-based）。
// 标准格式："Layer, Start, End, Style, Actor, MarginL, MarginR, MarginZ, Effect, Text"
// Text 是末位字段。
func findTextFieldIndex(formatLine string) int {
	fields := strings.Split(formatLine, ",")
	for i, f := range fields {
		if strings.TrimSpace(f) == "Text" {
			return i
		}
	}
	// 未找到 Text 字段则默认末位
	if len(fields) > 0 {
		return len(fields) - 1
	}
	return 9 // 标准 ASS 中 Text 是第 10 个字段（0-based=9）
}

// splitASSFields 将 Dialogue 行 body 按逗号分割，返回前缀（前 textFieldIdx 个字段 + 逗号）
// 和文本（剩余部分）。文本可能包含逗号。
func splitASSFields(body string, textIdx int) (prefix, text string) {
	if textIdx <= 0 {
		// Text 是第一个字段：整个 body 都是文本
		return "", body
	}

	// 找到第 textIdx 个逗号
	count := 0
	for i := 0; i < len(body); i++ {
		if body[i] == ',' {
			count++
			if count == textIdx {
				return body[:i+1], body[i+1:]
			}
		}
	}
	// 字段数不足：整个 body 作为前缀，文本为空
	return body + ",", ""
}

func renderASS(doc *pipeline.Document, bw *bufio.Writer) error {
	// 处理可能存在的头部/样式等非 Dialogue 内容
	// 这些内容以 Skip=true 的 Segment 存储在开头
	for _, seg := range doc.Segments {
		if seg.Skip {
			bw.WriteString(seg.Source)
			bw.WriteString("\n")
			continue
		}

		prefix, ok := seg.Meta["dialogue_start"].(string)
		if !ok || prefix == "" {
			// 没有前缀，尝试用默认格式重建
			prefix = "Dialogue: 0,0:00:00.00,0:00:00.00,Default,,0,0,0,,"
		}

		text := seg.Target
		if text == "" {
			text = seg.Source
		}

		bw.WriteString(prefix)
		bw.WriteString(text)
		bw.WriteString("\n")
	}
	return bw.Flush()
}
