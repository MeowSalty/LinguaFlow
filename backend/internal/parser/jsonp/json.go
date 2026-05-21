// Package jsonp 是 JSON / YAML / TOML 等结构化格式的占位 parser。
// 完整实现需指定「翻译哪些字段」的策略，待后续阶段。
package jsonp

import (
	"context"
	"io"

	"github.com/MeowSalty/LinguaFlow/backend/internal/parser"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

type Parser struct{}

func (*Parser) Extensions() []string { return []string{".json", ".yaml", ".yml", ".toml"} }

func (*Parser) Parse(context.Context, io.Reader) (*pipeline.Document, error) {
	return nil, parser.ErrNotImplemented
}

func (*Parser) Render(context.Context, *pipeline.Document, io.Writer) error {
	return parser.ErrNotImplemented
}

func init() { parser.Register("structured", &Parser{}) }
