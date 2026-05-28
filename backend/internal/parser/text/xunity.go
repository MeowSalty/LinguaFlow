package text

import (
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

	for _, line := range lines {
		if !strings.Contains(line, "=") {
			// 不含 = 的行，Skip 保留
			segments = append(segments, pipeline.Segment{
				ID:     shortHash(line),
				Source: line,
				Skip:   true,
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
			})
			continue
		}

		// 可翻译行：key → Source, value → Target
		segments = append(segments, pipeline.Segment{
			ID:     shortHash(keyTrimmed),
			Source: keyTrimmed,
			Target: strings.TrimSpace(after),
			Meta: map[string]any{
				"xunity_raw": line,
			},
		})
	}

	return &pipeline.Document{
		Segments: segments,
		Format:   "xunity_text",
	}, nil
}

// renderXUnity 渲染 XUnity 格式。
// 渲染规则：
//   - Skip 段 → 原样输出
//   - 可翻译段 → key=Target（Target 为空则回退到 Source）
func renderXUnity(doc *pipeline.Document, w io.Writer) error {
	bw := &strings.Builder{}

	for _, seg := range doc.Segments {
		if seg.Skip {
			if _, err := fmt.Fprintln(bw, seg.Source); err != nil {
				return err
			}
			continue
		}

		// 可翻译行
		rawLine, _ := seg.Meta["xunity_raw"].(string)
		if rawLine == "" {
			// 无原始行信息，回退格式 key=value
			value := seg.Target
			if value == "" {
				value = seg.Source
			}
			if _, err := fmt.Fprintf(bw, "%s=%s\n", seg.Source, value); err != nil {
				return err
			}
			continue
		}

		// 用 Target 替换 = 右侧内容，保留原始行格式
		before, _, found := strings.Cut(rawLine, "=")
		if !found {
			// 不可能发生，兜底
			if _, err := fmt.Fprintln(bw, rawLine); err != nil {
				return err
			}
			continue
		}

		value := seg.Target
		if value == "" {
			value = seg.Source
		}
		if _, err := fmt.Fprintf(bw, "%s=%s\n", before, value); err != nil {
			return err
		}
	}

	_, err := io.WriteString(w, bw.String())
	return err
}
