package subtitle

import (
	"bufio"
	"regexp"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/hash"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

// vttCueRE 从 VTT 时间戳行中提取结束时间之后的 cue settings。
var vttCueRE = regexp.MustCompile(
	`^\d{2}:\d{2}:\d{2}\.\d{3}\s*-->\s*\d{2}:\d{2}:\d{2}\.\d{3}\s*(.*)$`,
)

func parseVTT(content string) (*pipeline.Document, error) {
	// 分离头部（"WEBVTT" 及后续元数据，直到第一个空行）
	headerEnd := strings.Index(content, "\n\n")
	header := strings.TrimSpace(content)
	body := ""
	if headerEnd >= 0 {
		header = strings.TrimSpace(content[:headerEnd])
		body = content[headerEnd+2:]
	}

	if strings.TrimSpace(body) == "" {
		return &pipeline.Document{
			Format: "vtt",
			Vars:   map[string]any{"vtt_header": header},
		}, nil
	}

	blocks := splitBlocks(strings.TrimSpace(body))
	segs := make([]pipeline.Segment, 0, len(blocks))
	vars := map[string]any{"vtt_header": header}

	for _, block := range blocks {
		lines := strings.Split(block, "\n")
		tsIdx := findTimestampLine(lines)
		if tsIdx < 0 {
			segs = append(segs, pipeline.Segment{
				ID:     hash.Short(block),
				Source: block,
				Skip:   true,
			})
			continue
		}

		times := strings.TrimSpace(lines[tsIdx])

		// 可选的 cue identifier（时间戳前的行）
		id := strings.TrimSpace(strings.Join(lines[:tsIdx], "\n"))

		// 字幕文本
		text := joinNonEmpty(lines[tsIdx+1:], "\n")
		if text == "" {
			continue
		}

		segs = append(segs, pipeline.Segment{
			ID:     hash.Short(text),
			Source: text,
			Meta: map[string]any{
				"type":      "vtt",
				"id":        id,
				"start":     extractStart(times),
				"end":       extractEnd(times),
				"cue":       extractCueSettings(times),
				"timestamp": times,
			},
		})
	}

	return &pipeline.Document{Segments: segs, Format: "vtt", Vars: vars}, nil
}

func renderVTT(doc *pipeline.Document, bw *bufio.Writer) error {
	// WEBVTT 头部
	if h, ok := doc.Vars["vtt_header"].(string); ok && h != "" {
		bw.WriteString(h)
	} else {
		bw.WriteString("WEBVTT")
	}
	bw.WriteString("\n")

	for _, seg := range doc.Segments {
		bw.WriteString("\n")

		if seg.Skip {
			bw.WriteString(seg.Source)
			bw.WriteString("\n")
			continue
		}

		// 可选的 cue identifier
		if id, ok := seg.Meta["id"].(string); ok && id != "" {
			bw.WriteString(id)
			bw.WriteString("\n")
		}

		// 时间戳
		start, _ := seg.Meta["start"].(string)
		end, _ := seg.Meta["end"].(string)
		if start != "" && end != "" {
			bw.WriteString(start)
			bw.WriteString(" --> ")
			bw.WriteString(end)
			// cue settings
			if cue, ok := seg.Meta["cue"].(string); ok && cue != "" {
				bw.WriteString(" ")
				bw.WriteString(cue)
			}
			bw.WriteString("\n")
		} else if ts, ok := seg.Meta["timestamp"].(string); ok && ts != "" {
			bw.WriteString(ts)
			bw.WriteString("\n")
		}

		// 文本
		text := seg.Target
		if text == "" {
			text = seg.Source
		}
		bw.WriteString(text)
		bw.WriteString("\n")
	}
	return bw.Flush()
}

// extractCueSettings 从 VTT 时间戳行中提取 cue settings（结束时间后的内容）。
func extractCueSettings(timestampLine string) string {
	m := vttCueRE.FindStringSubmatch(timestampLine)
	if m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}
