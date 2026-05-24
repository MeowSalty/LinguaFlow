// Package text 实现纯文本（.txt）parser。
//
// 支持两种格式自动检测：
//   - text：普通纯文本，按空行分段
//   - xunity_text：XUnity AutoTranslator 格式（key=value），≥70% 行含 = 时触发
package text

import (
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

func (*Parser) Extensions() []string { return []string{".txt"} }

// Parse 读取全部内容，自动检测格式后分发。
func (*Parser) Parse(_ context.Context, r io.Reader) (*pipeline.Document, error) {
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
func (*Parser) Render(_ context.Context, doc *pipeline.Document, w io.Writer) error {
	switch doc.Format {
	case "xunity_text":
		return renderXUnity(doc, w)
	default:
		return renderText(doc, w)
	}
}

// parseText 按空行分段，每段为一个可翻译 Segment。
func parseText(content string) (*pipeline.Document, error) {
	content = strings.TrimRight(content, "\n")
	if content == "" {
		return &pipeline.Document{Format: "text"}, nil
	}

	lines := strings.Split(content, "\n")
	var (
		segments []pipeline.Segment
		buf      strings.Builder
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

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			flush()
			continue
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

// renderText 用双换行符连接各段，末尾保留一个换行符。
func renderText(doc *pipeline.Document, w io.Writer) error {
	for i, seg := range doc.Segments {
		if i > 0 {
			if _, err := io.WriteString(w, "\n\n"); err != nil {
				return err
			}
		}
		out := seg.Target
		if out == "" {
			out = seg.Source
		}
		if _, err := io.WriteString(w, out); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(w, "\n"); err != nil {
		return err
	}
	return nil
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}

func init() {
	parser.Register("text", New())
}
