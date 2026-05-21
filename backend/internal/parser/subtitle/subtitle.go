// Package subtitle 是字幕格式（.srt/.vtt/.ass）的占位 parser。
// 调用 Parse/Render 会返回 ErrNotImplemented。完整实现待后续阶段。
package subtitle

import (
	"context"
	"io"

	"github.com/MeowSalty/LinguaFlow/backend/internal/parser"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

type Parser struct{}

func (*Parser) Extensions() []string { return []string{".srt", ".vtt", ".ass"} }

func (*Parser) Parse(context.Context, io.Reader) (*pipeline.Document, error) {
	return nil, parser.ErrNotImplemented
}

func (*Parser) Render(context.Context, *pipeline.Document, io.Writer) error {
	return parser.ErrNotImplemented
}

func init() { parser.Register("subtitle", &Parser{}) }
