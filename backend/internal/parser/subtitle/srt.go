package subtitle

import (
	"bufio"
	"fmt"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/hash"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

func parseSRT(content string) (*pipeline.Document, error) {
	blocks := splitBlocks(strings.TrimSpace(content))
	segs := make([]pipeline.Segment, 0, len(blocks))

	for _, block := range blocks {
		lines := strings.Split(block, "\n")
		tsIdx := findTimestampLine(lines)
		if tsIdx < 0 {
			// 没有时间戳的 block 作为不可翻译段保留
			segs = append(segs, pipeline.Segment{
				ID:     hash.Short(block),
				Source: block,
				Skip:   true,
			})
			continue
		}

		times := strings.TrimSpace(lines[tsIdx])

		// 序号：时间戳前的所有非空行
		idParts := make([]string, 0, tsIdx)
		for _, l := range lines[:tsIdx] {
			if t := strings.TrimSpace(l); t != "" {
				idParts = append(idParts, t)
			}
		}

		// 文本：时间戳后的所有非空行
		text := joinNonEmpty(lines[tsIdx+1:], "\n")
		if text == "" {
			continue
		}

		segs = append(segs, pipeline.Segment{
			ID:     hash.Short(text),
			Source: text,
			Meta: map[string]any{
				"type":  "srt",
				"seq":   strings.Join(idParts, " "),
				"start": extractStart(times),
				"end":   extractEnd(times),
			},
		})
	}

	return &pipeline.Document{Segments: segs, Format: "srt"}, nil
}

func renderSRT(doc *pipeline.Document, bw *bufio.Writer) error {
	for i, seg := range doc.Segments {
		if seg.Skip {
			if i > 0 {
				bw.WriteString("\n")
			}
			bw.WriteString(seg.Source)
			bw.WriteString("\n")
			continue
		}

		if i > 0 {
			bw.WriteString("\n")
		}

		// 序号
		seq, _ := seg.Meta["seq"].(string)
		if seq == "" {
			seq = fmt.Sprintf("%d", i+1)
		}
		bw.WriteString(seq)
		bw.WriteString("\n")

		// 时间戳
		start, _ := seg.Meta["start"].(string)
		end, _ := seg.Meta["end"].(string)
		if start != "" && end != "" {
			bw.WriteString(start)
			bw.WriteString(" --> ")
			bw.WriteString(end)
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
