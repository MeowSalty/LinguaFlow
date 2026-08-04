// Package output 把翻译后的 Document 写入目标文件。
// MVP 实现「overwrite」模式：先写入临时文件再 rename，保证失败时不留半成品。
// 「side_by_side」模式为占位。
//
// 注意：当前 Engine 已直接调用 Parser.Render，此包保留供外部调用方使用。
package output

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/parser"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

// ErrUnsupportedMode 输出模式未实现。
var ErrUnsupportedMode = errors.New("output: unsupported mode")

// Writer 把 Document 渲染并写入到指定路径。
type Writer struct {
	cfg      config.OutputConfig
	path     string
	parser   parser.Parser
	original io.Reader // 原始文件内容，位置替换渲染使用
}

func New(cfg config.OutputConfig, p parser.Parser, path string) *Writer {
	return &Writer{cfg: cfg, parser: p, path: path}
}

// WithOriginal 设置原始文件读取器。位置替换渲染模式下必须设置。
func (w *Writer) WithOriginal(r io.Reader) *Writer {
	w.original = r
	return w
}

// Write 把 doc 渲染到目标文件。原子写入：临时文件 → rename。
func (w *Writer) Write(ctx context.Context, doc *pipeline.Document) error {
	switch w.cfg.Mode {
	case "", "overwrite":
		return w.writeOverwrite(ctx, doc)
	case "side_by_side":
		return ErrUnsupportedMode
	default:
		return fmt.Errorf("%w: %s", ErrUnsupportedMode, w.cfg.Mode)
	}
}

func (w *Writer) writeOverwrite(ctx context.Context, doc *pipeline.Document) error {
	dir := filepath.Dir(w.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("output: mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".lf-*.tmp")
	if err != nil {
		return fmt.Errorf("output: create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		// 若 rename 成功，tmpName 已不存在；否则尝试清理
		_ = os.Remove(tmpName)
	}()

	if err := w.parser.Render(ctx, doc, w.original, tmp); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("output: render: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("output: close temp: %w", err)
	}
	if err := os.Rename(tmpName, w.path); err != nil {
		return fmt.Errorf("output: rename: %w", err)
	}
	return nil
}

// ensure io.Writer dependency is referenced for future side-by-side mode
var _ io.Writer = (*os.File)(nil)
