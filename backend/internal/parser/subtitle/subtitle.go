// Package subtitle 实现 .srt / .vtt / .ass 字幕格式的解析与渲染。
//
// 位置替换策略
//
//   - SRT/VTT：Parse 时记录每个字幕块在原始文件中的字节偏移，
//     Render 时读取原始文件字节，按偏移替换文本部分。
//   - ASS：Parse 时记录每行 Dialogue 在原始文件中的行索引和前缀，
//     Render 时读取原始文件行，替换特定行。
package subtitle

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/parser"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

// Parser 可解析 .srt / .vtt / .ass 文件。
type Parser struct{}

// New 构造一个 Parser。
func New() *Parser { return &Parser{} }

// Extensions 返回支持的字幕扩展名。
func (*Parser) Extensions() []string { return []string{".srt", ".vtt", ".ass"} }

func (*Parser) Parse(_ context.Context, r io.Reader) (*pipeline.Document, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("subtitle: read input: %w", err)
	}
	content := string(data)
	content = strings.ReplaceAll(content, "\r\n", "\n")

	if strings.TrimSpace(content) == "" {
		return &pipeline.Document{Format: "subtitle"}, nil
	}

	firstLine := content
	if idx := strings.IndexByte(content, '\n'); idx >= 0 {
		firstLine = content[:idx]
	}
	firstLine = strings.TrimSpace(firstLine)

	switch {
	case firstLine == "WEBVTT":
		return parseVTT(content)
	case firstLine == "[Script Info]":
		return parseASS(content)
	default:
		return parseSRT(content)
	}
}

func (*Parser) Render(_ context.Context, doc *pipeline.Document, original io.Reader, w io.Writer) error {
	bw := bufio.NewWriter(w)

	switch doc.Format {
	case "srt":
		return renderSRT(doc, original, bw)
	case "vtt":
		return renderVTT(doc, original, bw)
	case "ass":
		return renderASS(doc, original, bw)
	default:
		return fmt.Errorf("subtitle: unsupported format %q", doc.Format)
	}
}

// init 注册 subtitle parser。
func init() {
	parser.Register("subtitle", New())
}
