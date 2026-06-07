// Package parser 定义文档解析器接口与扩展名注册表。
// MVP 仅实现 markdown，subtitle / json 等以占位形式注册。
package parser

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

// Parser 把原始字节流解析为可翻译的 Document，并能将翻译后的 Document 回写。
type Parser interface {
	// Extensions 返回该 parser 处理的小写扩展名（含点，如 ".md"）。
	Extensions() []string
	Parse(ctx context.Context, r io.Reader) (*pipeline.Document, error)
	Render(ctx context.Context, doc *pipeline.Document, w io.Writer) error
}

// ErrNotImplemented 由占位 parser 返回。
var ErrNotImplemented = errors.New("parser: not implemented")

// ErrNoParser 找不到匹配扩展名的 parser。
var ErrNoParser = errors.New("parser: no parser for extension")

// byName / byExt 在 init 阶段由各 parser 包写入，main 后只读。
// Go 内存模型保证所有 init 先于 main 执行（happens-before），因此无需加锁。
var (
	byName = map[string]Parser{}
	byExt  = map[string]Parser{}
)

// Register 注册 parser。仅应在 init 中调用。
func Register(name string, p Parser) {
	byName[name] = p
	for _, ext := range p.Extensions() {
		byExt[strings.ToLower(ext)] = p
	}
}

// Get 按名字获取 parser。
func Get(name string) (Parser, bool) {
	p, ok := byName[name]
	return p, ok
}

// DetectByExt 根据文件路径扩展名选择 parser。
func DetectByExt(path string) (Parser, error) {
	ext := strings.ToLower(filepath.Ext(path))
	p, ok := byExt[ext]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNoParser, ext)
	}
	return p, nil
}
