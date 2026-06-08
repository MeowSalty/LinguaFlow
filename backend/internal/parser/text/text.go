// Package text 实现纯文本（.txt）parser。
//
// 支持两种格式自动检测：
//   - text：普通纯文本，按空行分段，位置替换策略（行号范围）
//   - xunity_text：XUnity AutoTranslator 格式（key=value），≥70% 行含 = 时触发
package text

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/parser"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

type Parser struct{}

func New() *Parser { return &Parser{} }

func (*Parser) Extensions() []string { return []string{".txt"} }

// Parse 读取全部内容，自动检测格式后分发。
func (p *Parser) Parse(_ context.Context, r io.Reader) (*pipeline.Document, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	content := string(data)
	content = strings.ReplaceAll(content, "\r\n", "\n")

	if strings.TrimSpace(content) == "" {
		return &pipeline.Document{Format: "text"}, nil
	}

	if isXUnity(content) {
		return parseXUnity(content)
	}
	return parseText(content)
}

// Render 根据 doc.Format 分发渲染。
func (*Parser) Render(_ context.Context, doc *pipeline.Document, original io.Reader, w io.Writer) error {
	switch doc.Format {
	case "xunity_text":
		return renderXUnity(doc, original, w)
	default:
		return renderText(doc, original, w)
	}
}

// parseText 按空行分段，每段为一个可翻译 Segment。记录行号范围。
func parseText(content string) (*pipeline.Document, error) {
	content = strings.TrimRight(content, "\n")
	if content == "" {
		return &pipeline.Document{Format: "text"}, nil
	}

	lines := strings.Split(content, "\n")
	var (
		segments []pipeline.Segment
		buf      strings.Builder
		segStart int // 当前段落起始行号（1-based）
		lineNo   int // 当前行号（1-based）
	)
	flush := func() {
		if buf.Len() == 0 {
			return
		}
		text := strings.TrimRight(buf.String(), "\n")
		segments = append(segments, pipeline.Segment{
			ID:     shortHash(text),
			Source: text,
			Meta: map[string]any{
				"pos_lines": []int{segStart, lineNo},
			},
		})
		buf.Reset()
	}

	for _, line := range lines {
		lineNo++
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if buf.Len() == 0 {
			segStart = lineNo
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	flush()

	return &pipeline.Document{
		Segments: segments,
		Format:   "text",
	}, nil
}

// renderText 读取原始文件，按行号范围替换译文。
func renderText(doc *pipeline.Document, original io.Reader, w io.Writer) error {
	// 读取原始文件所有行
	scanner := bufio.NewScanner(original)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("text: read original: %w", err)
	}

	// 构建替换区间
	type replacement struct {
		startLine int    // 1-based inclusive
		endLine   int    // 1-based inclusive
		text      string // 译文
	}
	var replacements []replacement
	for _, seg := range doc.Segments {
		pos, ok := seg.Meta["pos_lines"].([]int)
		if !ok || len(pos) < 2 {
			continue
		}
		target := seg.Target
		if target == "" {
			target = seg.Source
		}
		replacements = append(replacements, replacement{
			startLine: pos[0],
			endLine:   pos[1],
			text:      target,
		})
	}

	// 按行号范围替换
	bw := bufio.NewWriter(w)
	lineIdx := 0 // 0-based index into lines
	for _, rep := range replacements {
		// 写入替换区间之前的行（原样）
		for lineIdx < rep.startLine-1 && lineIdx < len(lines) {
			if _, err := bw.WriteString(lines[lineIdx]); err != nil {
				return err
			}
			if err := bw.WriteByte('\n'); err != nil {
				return err
			}
			lineIdx++
		}
		// 写入译文
		if _, err := bw.WriteString(rep.text); err != nil {
			return err
		}
		if err := bw.WriteByte('\n'); err != nil {
			return err
		}
		// 跳过原始行（已替换）
		lineIdx = rep.endLine
	}
	// 写入剩余行
	for lineIdx < len(lines) {
		if _, err := bw.WriteString(lines[lineIdx]); err != nil {
			return err
		}
		if err := bw.WriteByte('\n'); err != nil {
			return err
		}
		lineIdx++
	}
	return bw.Flush()
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}

func init() {
	parser.Register("text", New())
}
