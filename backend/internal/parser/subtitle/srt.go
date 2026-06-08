package subtitle

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/hash"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

func parseSRT(content string) (*pipeline.Document, error) {
	// 记录每个 block 在原始 content 中的字节偏移
	rawBytes := []byte(content)
	blocks := splitBlocksWithOffsets(rawBytes)
	segs := make([]pipeline.Segment, 0, len(blocks))

	for _, blk := range blocks {
		blockText := string(rawBytes[blk.start:blk.end])
		lines := strings.Split(blockText, "\n")
		tsIdx := findTimestampLine(lines)
		if tsIdx < 0 {
			// 没有时间戳的 block 作为不可翻译段保留
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

		// 计算文本在 block 内的字节偏移（相对 block 起始）
		// 文本起始 = block 起始 + 前面行的字节数 + 时间戳行的字节数 + 换行符
		textStartInBlock := 0
		for i := 0; i <= tsIdx; i++ {
			textStartInBlock += len(lines[i]) + 1 // +1 for \n
		}
		// 跳过文本前的空行
		for textStartInBlock < len(blockText) {
			remaining := blockText[textStartInBlock:]
			if strings.TrimSpace(remaining) == "" {
				break
			}
			if remaining[0] == '\n' {
				textStartInBlock++
				continue
			}
			break
		}

		// 文本在原始文件中的绝对偏移
		absTextStart := blk.start + textStartInBlock
		// 文本结束位置：找到 block 中最后一个非空文本行
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
				"type":      "srt",
				"seq":       strings.Join(idParts, " "),
				"start":     extractStart(times),
				"end":       extractEnd(times),
				"pos_block": []int{blk.start, blk.end},
				"pos_text":  []int{absTextStart, textEnd},
			},
		})
	}

	return &pipeline.Document{Segments: segs, Format: "srt"}, nil
}

func renderSRT(doc *pipeline.Document, original io.Reader, bw *bufio.Writer) error {
	// 读取原始文件全部字节
	raw, err := io.ReadAll(original)
	if err != nil {
		return fmt.Errorf("srt: read original: %w", err)
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
		// 写入替换区间之前的字节
		bw.Write(raw[cursor:rep.textStart])
		// 写入译文
		bw.WriteString(rep.text)
		cursor = rep.textEnd
	}
	// 写入剩余字节
	bw.Write(raw[cursor:])
	return bw.Flush()
}

// blockWithOffset 记录一个 block 在原始字节中的起止偏移。
type blockWithOffset struct {
	start int
	end   int
}

// splitBlocksWithOffsets 按空行分割，返回每个 block 的字节偏移。
func splitBlocksWithOffsets(raw []byte) []blockWithOffset {
	var blocks []blockWithOffset
	lines := bytes.Split(raw, []byte("\n"))
	offset := 0
	blockStart := -1

	for _, line := range lines {
		lineLen := len(line)
		if bytes.TrimSpace(line) == nil || bytes.TrimSpace(line) == nil && len(line) == 0 {
			// 空行：结束当前 block
			if blockStart >= 0 {
				blocks = append(blocks, blockWithOffset{start: blockStart, end: offset})
				blockStart = -1
			}
			offset += lineLen + 1 // +1 for \n
			continue
		}
		// 非空行
		if blockStart < 0 {
			blockStart = offset
		}
		offset += lineLen + 1 // +1 for \n
	}
	// 最后一个 block
	if blockStart >= 0 {
		blocks = append(blocks, blockWithOffset{start: blockStart, end: offset})
	}
	return blocks
}
