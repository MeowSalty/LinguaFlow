package text

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

// isXUnity 检测是否为 XUnity AutoTranslator 格式。
// 判定标准：非空行中 ≥70% 包含 "=" 且 = 左侧非空。
func isXUnity(content string) bool {
	lines := strings.Split(content, "\n")
	var valid, eqLines int
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		valid++
		before, after, found := strings.Cut(trimmed, "=")
		if found && strings.TrimSpace(before) != "" {
			_ = after
			eqLines++
		}
	}
	if valid == 0 {
		return false
	}
	return float64(eqLines)/float64(valid) >= 0.7
}

// parseXUnity 解析 XUnity AutoTranslator 格式的文本。
//
// 每一行格式为：
//
//	key=value
//
// 处理规则：
//   - 含 = 的行 → 翻译 Segment（Source=key, Target=value）
//   - 不含 = 的行 → Skip Segment（原样保留，不翻译）
//   - = 左侧为空 → Skip Segment（不翻译）
func parseXUnity(content string) (*pipeline.Document, error) {
	content = strings.TrimRight(content, "\n")
	if content == "" {
		return &pipeline.Document{Format: "xunity_text"}, nil
	}

	lines := strings.Split(content, "\n")
	segments := make([]pipeline.Segment, 0, len(lines))

	for i, line := range lines {
		if !strings.Contains(line, "=") {
			// 不含 = 的行，Skip 保留
			segments = append(segments, pipeline.Segment{
				ID:     shortHash(line),
				Source: line,
				Skip:   true,
				Meta: map[string]any{
					"pos_line": i, // 0-based 行索引
				},
			})
			continue
		}

		// 分割 key=value
		before, after, found := strings.Cut(line, "=")
		_ = found // 肯定找到

		keyTrimmed := strings.TrimSpace(before)
		if keyTrimmed == "" {
			// = 左侧为空 → Skip
			segments = append(segments, pipeline.Segment{
				ID:     shortHash(line),
				Source: line,
				Skip:   true,
				Meta: map[string]any{
					"pos_line": i,
				},
			})
			continue
		}

		// 可翻译行：key → Source, value → Target
		segments = append(segments, pipeline.Segment{
			ID:     shortHash(keyTrimmed),
			Source: keyTrimmed,
			Target: strings.TrimSpace(after),
			Meta: map[string]any{
				"pos_line": i, // 0-based 行索引
			},
		})
	}

	return &pipeline.Document{
		Segments: segments,
		Format:   "xunity_text",
	}, nil
}

// renderXUnity 渲染 XUnity 格式。位置替换策略：
// 读取原始文件行，按 pos_line 替换可翻译行的 value 部分。
func renderXUnity(doc *pipeline.Document, original io.Reader, w io.Writer) error {
	// 读取原始文件所有行
	scanner := bufio.NewScanner(original)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("xunity: read original: %w", err)
	}

	// 构建行索引 → Segment 的映射
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

	// 按行输出，可翻译行替换 value 部分
	bw := bufio.NewWriter(w)
	for i, line := range lines {
		seg, ok := lineToSeg[i]
		if !ok {
			// 原样输出
			bw.WriteString(line)
			bw.WriteByte('\n')
			continue
		}
		// 替换 = 右侧内容
		before, _, found := strings.Cut(line, "=")
		if !found {
			// 不可能发生（Parse 时已确认），兜底原样
			bw.WriteString(line)
			bw.WriteByte('\n')
			continue
		}
		value := seg.Target
		if value == "" {
			value = seg.Source
		}
		fmt.Fprintf(bw, "%s=%s\n", before, value)
	}
	return bw.Flush()
}
