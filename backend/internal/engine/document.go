package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// DocumentSource 提供待翻译的输入文档。
type DocumentSource interface {
	// Open 返回文档内容的 ReadCloser。
	// Engine 在解析完成后负责 Close。
	Open(ctx context.Context) (io.ReadCloser, error)

	// FormatHint 返回文件扩展名（如 ".md"、".srt"），
	// 用于 parser 自动检测。返回空字符串时 Engine 将报错
	// （因为当前所有 parser 都基于扩展名注册）。
	FormatHint() string
}

// DocumentSink 接收翻译后的输出文档。
type DocumentSink interface {
	// Create 返回一个 WriteCloser 用于写入翻译结果。
	// Engine 写入并渲染完成后负责 Close。
	Create(ctx context.Context) (io.WriteCloser, error)
}

// ---------------------------------------------------------------------------
// 文件系统适配器
// ---------------------------------------------------------------------------

// FileReader 从本地文件读取文档。
type FileReader struct {
	Path string
}

func (f *FileReader) Open(ctx context.Context) (io.ReadCloser, error) {
	return os.Open(f.Path)
}

func (f *FileReader) FormatHint() string {
	return filepath.Ext(f.Path)
}

// FileWriter 将翻译结果写入本地文件（原子写入）。
type FileWriter struct {
	Path string
}

func (f *FileWriter) Create(ctx context.Context) (io.WriteCloser, error) {
	dir := filepath.Dir(f.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("engine: mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".lf-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("engine: create temp: %w", err)
	}
	return &atomicFile{tmp: tmp, target: f.Path}, nil
}

// atomicFile 实现原子写入：先写临时文件，Close 时 rename 到目标路径。
type atomicFile struct {
	tmp    *os.File
	target string
}

func (a *atomicFile) Write(p []byte) (int, error) { return a.tmp.Write(p) }

func (a *atomicFile) Close() error {
	tmpName := a.tmp.Name()
	if err := a.tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, a.target); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// 内存适配器（Web 服务 / 测试）
// ---------------------------------------------------------------------------

// BytesReader 从内存字节切片读取文档。
type BytesReader struct {
	Data      []byte
	Extension string // 如 ".md"
}

func (b *BytesReader) Open(ctx context.Context) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(b.Data)), nil
}

func (b *BytesReader) FormatHint() string { return b.Extension }

// BytesWriter 将翻译结果捕获到内存缓冲区。
type BytesWriter struct {
	buf bytes.Buffer
}

func (b *BytesWriter) Create(ctx context.Context) (io.WriteCloser, error) {
	return &nopWriteCloser{&b.buf}, nil
}

// Bytes 返回已写入的字节切片。
func (b *BytesWriter) Bytes() []byte { return b.buf.Bytes() }

// ---------------------------------------------------------------------------
// io.Reader / io.Writer 适配器（通用流式输入/输出）
// ---------------------------------------------------------------------------

// ReaderSource 包装任意 io.Reader 作为输入源。
type ReaderSource struct {
	Reader    io.Reader
	Extension string
}

func (r *ReaderSource) Open(ctx context.Context) (io.ReadCloser, error) {
	if rc, ok := r.Reader.(io.ReadCloser); ok {
		return rc, nil
	}
	return io.NopCloser(r.Reader), nil
}

func (r *ReaderSource) FormatHint() string { return r.Extension }

// WriterSink 包装任意 io.Writer 作为输出目标。
type WriterSink struct {
	Writer io.Writer
}

func (w *WriterSink) Create(ctx context.Context) (io.WriteCloser, error) {
	if wc, ok := w.Writer.(io.WriteCloser); ok {
		return wc, nil
	}
	return &nopWriteCloser{w.Writer}, nil
}

// ---------------------------------------------------------------------------
// 辅助类型
// ---------------------------------------------------------------------------

// nopWriteCloser 包装一个 io.Writer，使其满足 io.WriteCloser。
// Close 是空操作。
type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error { return nil }

// ---------------------------------------------------------------------------
// 便利构造函数
// ---------------------------------------------------------------------------

// FileJob 从文件路径构造 TranslateJob（向后兼容 CLI / Worker 等文件系统调用方）。
func FileJob(inputPath, outputPath string) TranslateJob {
	return TranslateJob{
		Source: &FileReader{Path: inputPath},
		Sink:   &FileWriter{Path: outputPath},
	}
}
