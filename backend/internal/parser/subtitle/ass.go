package subtitle

import (
	"bufio"
	"fmt"
	"io"
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
	flushBuf := func(startLine, endLine int) {
		if buf.Len() == 0 {
			return
		}
		s := strings.TrimRight(buf.String(), "\n")
		if s != "" {
			segs = append(segs, pipeline.Segment{
				ID:     hash.Short(s),
				Source: s,
				Skip:   true,
				Meta: map[string]any{
					"pos_lines": []int{startLine, endLine},
				},
			})
		}
		buf.Reset()
	}

	// 字段索引：在 Events 节中通过 Format 行确定各字段位置
	textFieldIdx := -1
	bufStartLine := 0 // 当前缓冲区起始行号（0-based）

	for lineIdx, raw := range lines {
		_ = raw

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
					flushBuf(bufStartLine, lineIdx)
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

		// Dialogue 行 → 先刷新之前的缓冲区
		flushBuf(bufStartLine, lineIdx)
		bufStartLine = lineIdx

		prefix, text := splitASSFields(body, textFieldIdx)
		if text == "" {
			// 无文本的 Dialogue 行，保留原样
			segs = append(segs, pipeline.Segment{
				ID:     hash.Short(raw),
				Source: raw,
				Skip:   true,
				Meta: map[string]any{
					"pos_line": lineIdx, // 0-based
				},
			})
			buf.Reset()
			bufStartLine = lineIdx + 1
			continue
		}

		segs = append(segs, pipeline.Segment{
			ID:     hash.Short(text),
			Source: text,
			Meta: map[string]any{
				"type":           "ass",
				"dialogue_start": "Dialogue: " + prefix,
				"pos_line":       lineIdx, // 0-based 行索引
			},
		})
		buf.Reset()
		bufStartLine = lineIdx + 1
	}

	// 尾部非 Dialogue 内容
	flushBuf(bufStartLine, len(lines))

	if len(segs) == 0 {
		return &pipeline.Document{Format: "ass"}, nil
	}

	return &pipeline.Document{Segments: segs, Format: "ass"}, nil
}

// findTextFieldIndex 从 ASS Format 行内容中定位 Text 字段的索引（0-based）。
func findTextFieldIndex(formatLine string) int {
	fields := strings.Split(formatLine, ",")
	for i, f := range fields {
		if strings.TrimSpace(f) == "Text" {
			return i
		}
	}
	if len(fields) > 0 {
		return len(fields) - 1
	}
	return 9
}

// splitASSFields 将 Dialogue 行 body 按逗号分割，返回前缀和文本。
func splitASSFields(body string, textIdx int) (prefix, text string) {
	if textIdx <= 0 {
		return "", body
	}

	count := 0
	for i := 0; i < len(body); i++ {
		if body[i] == ',' {
			count++
			if count == textIdx {
				return body[:i+1], body[i+1:]
			}
		}
	}
	return body + ",", ""
}

func renderASS(doc *pipeline.Document, original io.Reader, bw *bufio.Writer) error {
	// 读取原始文件所有行
	raw, err := io.ReadAll(original)
	if err != nil {
		return fmt.Errorf("ass: read original: %w", err)
	}
	origLines := strings.Split(string(raw), "\n")

	// 构建行索引 → Segment 的映射（仅可翻译行）
	lineToSeg := make(map[int]pipeline.Segment)
	for _, seg := range doc.Segments {
		if seg.Skip {
			continue
		}
		lineIdx, ok := seg.Meta["pos_line"].(int)
		if !ok {
			continue
		}
		lineToSeg[lineIdx] = seg
	}

	// 按行输出，可翻译行替换文本部分
	for i, line := range origLines {
		seg, ok := lineToSeg[i]
		if !ok {
			bw.WriteString(line)
			bw.WriteByte('\n')
			continue
		}

		prefix, ok := seg.Meta["dialogue_start"].(string)
		if !ok || prefix == "" {
			bw.WriteString(line)
			bw.WriteByte('\n')
			continue
		}

		text := seg.Target
		if text == "" {
			text = seg.Source
		}

		bw.WriteString(prefix)
		bw.WriteString(text)
		bw.WriteByte('\n')
	}
	return bw.Flush()
}
