// Package subtitle 实现 .srt / .vtt / .ass 字幕格式的解析与渲染。
//
// 解析策略
//
//   - SRT：按空行分割为若干 cue block；每个 block 的时序行（含 -->）之前为
//     序号行、之后为字幕文本。Segment.Source 存放文本，Meta 存放序号及时序。
//   - VTT：跳过 "WEBVTT" 头部（存入 doc.Vars），后续 block 解析方式与 SRT 类
//     似，但时间戳使用小数点（.）分隔毫秒，且可能附加 cue settings。
//   - ASS：将 [Events] 节中每条 Dialogue 行作为可翻译单元；Text 字段（末段）提
//     取为 Segment.Source，行前缀存入 Meta；其余头部/样式/非 Dialogue 行存为
//     Skip 段，渲染时原样回写。
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

func (*Parser) Render(_ context.Context, doc *pipeline.Document, w io.Writer) error {
	bw := bufio.NewWriter(w)

	switch doc.Format {
	case "srt":
		return renderSRT(doc, bw)
	case "vtt":
		return renderVTT(doc, bw)
	case "ass":
		return renderASS(doc, bw)
	default:
		return fmt.Errorf("subtitle: unsupported format %q", doc.Format)
	}
}

// init 注册 subtitle parser。
func init() {
	parser.Register("subtitle", New())
}
