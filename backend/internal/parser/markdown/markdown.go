// Package markdown 实现按段落分割的 Markdown parser。
//
// MVP 策略：把文档按「空行」拆为若干段（block）。Render 时用单个空行连接。
// 这能正确保留段落顺序与代码块完整性（代码块内部不含空行的常见情况）。
// 后续可替换为更精细的 AST 解析（goldmark）。
package markdown

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/parser"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

type Parser struct{}

func New() *Parser { return &Parser{} }

func (*Parser) Extensions() []string { return []string{".md", ".markdown", ".mdx"} }

func (*Parser) Parse(_ context.Context, r io.Reader) (*pipeline.Document, error) {
	scanner := bufio.NewScanner(r)
	// 单行最长 1 MiB
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	var (
		segments []pipeline.Segment
		buf      strings.Builder
		inFence  bool
	)
	flush := func() {
		if buf.Len() == 0 {
			return
		}
		text := strings.TrimRight(buf.String(), "\n")
		segments = append(segments, pipeline.Segment{
			ID:     shortHash(text),
			Source: text,
		})
		buf.Reset()
	}

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
		}
		if !inFence && strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	flush()

	return &pipeline.Document{
		Segments: segments,
		Format:   "markdown",
	}, nil
}

func (*Parser) Render(_ context.Context, doc *pipeline.Document, w io.Writer) error {
	bw := bufio.NewWriter(w)
	for i, seg := range doc.Segments {
		if i > 0 {
			if _, err := bw.WriteString("\n\n"); err != nil {
				return err
			}
		}
		out := seg.Target
		if out == "" {
			out = seg.Source
		}
		if _, err := bw.WriteString(out); err != nil {
			return err
		}
	}
	// 末尾保留一个换行符，符合多数 Markdown 习惯
	if _, err := bw.WriteString("\n"); err != nil {
		return err
	}
	return bw.Flush()
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}

func init() {
	parser.Register("markdown", New())
}
