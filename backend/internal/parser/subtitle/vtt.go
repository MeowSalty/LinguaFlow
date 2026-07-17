package subtitle

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
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
	rawBytes := []byte(content)

	// 分离头部（"WEBVTT" 及后续元数据，直到第一个空行）
	headerEnd := strings.Index(content, "\n\n")
	header := strings.TrimSpace(content)
	body := ""
	bodyOffset := 0 // body 在原始字节中的起始偏移
	if headerEnd >= 0 {
		header = strings.TrimSpace(content[:headerEnd])
		body = content[headerEnd+2:]
		bodyOffset = headerEnd + 2
	}

	if strings.TrimSpace(body) == "" {
		return &pipeline.Document{
			Format: "vtt",
			Vars:   map[string]any{"vtt_header": header},
		}, nil
	}

	// 按空行分割 body，记录每个 block 的字节偏移
	blocks := splitVTTBlocksWithOffsets(rawBytes, bodyOffset)
	segs := make([]pipeline.Segment, 0, len(blocks))
	vars := map[string]any{"vtt_header": header}

	for _, blk := range blocks {
		blockText := string(rawBytes[blk.start:blk.end])
		lines := strings.Split(blockText, "\n")
		tsIdx := findTimestampLine(lines)
		if tsIdx < 0 {
			segs = append(segs, pipeline.Segment{
				ID:     hash.Short(blockText),
				Source: blockText,
				Skip:   true,
				Meta: map[string]any{
					"pos_block": []int{blk.start, blk.end},
				},
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

		// 计算文本在 block 内的字节偏移
		textStartInBlock := 0
		for i := 0; i <= tsIdx; i++ {
			textStartInBlock += len(lines[i]) + 1
		}
		// 跳过文本前的空行
		for textStartInBlock < len(blockText) {
			remaining := blockText[textStartInBlock:]
			if len(remaining) == 0 {
				break
			}
			if remaining[0] == '\n' {
				textStartInBlock++
				continue
			}
			break
		}

		absTextStart := blk.start + textStartInBlock
		textEnd := absTextStart
		textLines := lines[tsIdx+1:]
		offset := textStartInBlock
		for _, tl := range textLines {
			t := strings.TrimSpace(tl)
			if t == "" {
				offset += len(tl) + 1
				continue
			}
			offset += len(tl) + 1
			textEnd = blk.start + offset
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
				"pos_block": []int{blk.start, blk.end},
				"pos_text":  []int{absTextStart, textEnd},
			},
		})
	}

	return &pipeline.Document{Segments: segs, Format: "vtt", Vars: vars}, nil
}

func renderVTT(doc *pipeline.Document, original io.Reader, bw *bufio.Writer) error {
	// 读取原始文件全部字节
	raw, err := io.ReadAll(original)
	if err != nil {
		return fmt.Errorf("vtt: read original: %w", err)
	}

	// 收集所有可翻译 segment 的替换区间
	type replacement struct {
		textStart int
		textEnd   int
		text      string
	}
	var replacements []replacement
	for _, seg := range doc.Segments {
		if seg.Skip {
			continue
		}
		posText, ok := seg.Meta["pos_text"].([]int)
		if !ok || len(posText) < 2 {
			continue
		}
		target := seg.Target
		if target == "" {
			target = seg.Source
		}
		replacements = append(replacements, replacement{
			textStart: posText[0],
			textEnd:   posText[1],
			text:      target,
		})
	}

	// 按字节偏移替换
	cursor := 0
	for _, rep := range replacements {
		bw.Write(raw[cursor:rep.textStart])
		bw.WriteString(rep.text)
		cursor = rep.textEnd
	}
	bw.Write(raw[cursor:])
	return bw.Flush()
}

// splitVTTBlocksWithOffsets 在 body 范围内按空行分割，返回每个 block 的绝对字节偏移。
func splitVTTBlocksWithOffsets(raw []byte, bodyOffset int) []blockWithOffset {
	var blocks []blockWithOffset
	body := raw[bodyOffset:]
	lines := bytes.Split(body, []byte("\n"))
	offset := bodyOffset
	blockStart := -1

	for _, line := range lines {
		lineLen := len(line)
		if len(bytes.TrimSpace(line)) == 0 {
			if blockStart >= 0 {
				blocks = append(blocks, blockWithOffset{start: blockStart, end: offset})
				blockStart = -1
			}
			offset += lineLen + 1
			continue
		}
		if blockStart < 0 {
			blockStart = offset
		}
		offset += lineLen + 1
	}
	if blockStart >= 0 {
		blocks = append(blocks, blockWithOffset{start: blockStart, end: offset})
	}
	return blocks
}

// extractCueSettings 从 VTT 时间戳行中提取 cue settings（结束时间后的内容）。
func extractCueSettings(timestampLine string) string {
	m := vttCueRE.FindStringSubmatch(timestampLine)
	if m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}
