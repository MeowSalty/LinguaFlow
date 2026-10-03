package ziputil

import (
	"archive/zip"
	"compress/flate"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"sync"
)

var ErrArchiveEntriesExceeded = errors.New("ziputil: archive entry count exceeds limit")
var ErrOutputSizeExceeded = errors.New("ziputil: output size exceeds limit")

type Limits struct {
	MaxEntries       int
	MaxExpandedBytes int64
	MaxOutputBytes   int64
}

func DefaultLimits() Limits {
	return Limits{MaxEntries: 10000, MaxExpandedBytes: 1 << 30, MaxOutputBytes: 512 << 20}
}

type limitsKey struct{}

// WithLimits 为单次解析/渲染操作应用部署限制。每次打开 ZIP 都有
// 自己的有界解压计数器；不存在任何可变的包级全局状态。
func WithLimits(ctx context.Context, limits Limits) context.Context {
	return context.WithValue(ctx, limitsKey{}, limits)
}
func LimitsFromContext(ctx context.Context) Limits {
	if limits, ok := ctx.Value(limitsKey{}).(Limits); ok {
		return limits
	}
	return DefaultLimits()
}

type readBudget struct {
	mu        sync.Mutex
	remaining int64
	ctx       context.Context
	exhausted bool
}
type budgetReader struct {
	*readBudget
	io.ReadCloser
}

func (r *budgetReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if r.exhausted {
		return 0, ErrDecompressedSizeExceeded
	}
	if int64(len(p)) > r.remaining+1 {
		p = p[:r.remaining+1]
	}
	n, err := r.ReadCloser.Read(p)
	if int64(n) > r.remaining {
		r.exhausted = true
		return 0, ErrDecompressedSizeExceeded
	}
	r.remaining -= int64(n)
	return n, err
}

type Archive struct {
	Reader *zip.Reader
	budget *readBudget
}

func (a *Archive) Err() error {
	a.budget.mu.Lock()
	defer a.budget.mu.Unlock()
	if err := a.budget.ctx.Err(); err != nil {
		return err
	}
	if a.budget.exhausted {
		return ErrDecompressedSizeExceeded
	}
	return nil
}

func constrainArchive(ctx context.Context, reader *zip.Reader) (*Archive, error) {
	limits := LimitsFromContext(ctx)
	if limits.MaxEntries <= 0 || limits.MaxExpandedBytes <= 0 || limits.MaxExpandedBytes == math.MaxInt64 || limits.MaxOutputBytes <= 0 {
		return nil, ErrDecompressedSizeExceeded
	}
	if len(reader.File) > limits.MaxEntries {
		return nil, ErrArchiveEntriesExceeded
	}
	var total uint64
	for _, file := range reader.File {
		if file.UncompressedSize64 > uint64(limits.MaxExpandedBytes)-total {
			return nil, ErrDecompressedSizeExceeded
		}
		total += file.UncompressedSize64
	}
	budget := &readBudget{remaining: limits.MaxExpandedBytes, ctx: ctx}
	reader.RegisterDecompressor(zip.Store, func(r io.Reader) io.ReadCloser { return &budgetReader{budget, io.NopCloser(r)} })
	reader.RegisterDecompressor(zip.Deflate, func(r io.Reader) io.ReadCloser { return &budgetReader{budget, flate.NewReader(r)} })
	return &Archive{Reader: reader, budget: budget}, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

type contextReaderAt struct {
	ctx    context.Context
	reader io.ReaderAt
}

func (r contextReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.ReadAt(p, off)
}

type outputWriter struct {
	ctx       context.Context
	writer    io.Writer
	remaining int64
}

func (w *outputWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(p)) > w.remaining {
		return 0, ErrOutputSizeExceeded
	}
	n, err := w.writer.Write(p)
	w.remaining -= int64(n)
	return n, err
}

// OutputWriter 限制最终归档的实际大小，包括其中央目录。
func OutputWriter(ctx context.Context, writer io.Writer) io.Writer {
	return &outputWriter{ctx: ctx, writer: writer, remaining: LimitsFromContext(ctx).MaxOutputBytes}
}

// preflightDirectory 在 archive/zip 构建 File 切片之前统计中央目录头。
// 仅凭声明的 16 位计数并不安全，因为 ZIP 读取器还会接受回绕计数与 ZIP64。
// 这里不分配任何名称或 extra 字段。
func preflightDirectory(reader io.ReaderAt, size int64, maxEntries int) error {
	if maxEntries <= 0 {
		return ErrArchiveEntriesExceeded
	}
	if size < 22 {
		return zip.ErrFormat
	}
	length := int64(65557)
	if size < length {
		length = size
	}
	tail := make([]byte, length)
	if _, err := reader.ReadAt(tail, size-length); err != nil {
		return err
	}
	end := -1
	for i := len(tail) - 22; i >= 0; i-- {
		if binary.LittleEndian.Uint32(tail[i:i+4]) == 0x06054b50 && i+22+int(binary.LittleEndian.Uint16(tail[i+20:i+22])) == len(tail) {
			end = i
			break
		}
	}
	if end < 0 {
		return zip.ErrFormat
	}
	eocd := size - length + int64(end)
	records := uint64(binary.LittleEndian.Uint16(tail[end+10 : end+12]))
	directorySize := uint64(binary.LittleEndian.Uint32(tail[end+12 : end+16]))
	directoryEnd := eocd
	if records == 0xffff || directorySize == 0xffffffff || binary.LittleEndian.Uint32(tail[end+16:end+20]) == 0xffffffff {
		if eocd < 20 {
			return zip.ErrFormat
		}
		var locator [20]byte
		if _, err := reader.ReadAt(locator[:], eocd-20); err != nil {
			return err
		}
		if binary.LittleEndian.Uint32(locator[:4]) != 0x07064b50 {
			return zip.ErrFormat
		}
		offset := binary.LittleEndian.Uint64(locator[8:16])
		if offset > uint64(size-56) {
			return zip.ErrFormat
		}
		var header [56]byte
		if _, err := reader.ReadAt(header[:], int64(offset)); err != nil {
			return err
		}
		if binary.LittleEndian.Uint32(header[:4]) != 0x06064b50 {
			return zip.ErrFormat
		}
		records = binary.LittleEndian.Uint64(header[32:40])
		directorySize = binary.LittleEndian.Uint64(header[40:48])
		directoryEnd = int64(offset)
	}
	if records > uint64(maxEntries) {
		return ErrArchiveEntriesExceeded
	}
	if directorySize > uint64(directoryEnd) {
		return zip.ErrFormat
	}
	offset := directoryEnd - int64(directorySize)
	count := 0
	for offset < directoryEnd {
		var header [46]byte
		if directoryEnd-offset < int64(len(header)) {
			return zip.ErrFormat
		}
		if _, err := reader.ReadAt(header[:], offset); err != nil {
			return err
		}
		if binary.LittleEndian.Uint32(header[:4]) != 0x02014b50 {
			return zip.ErrFormat
		}
		count++
		if count > maxEntries {
			return ErrArchiveEntriesExceeded
		}
		span := int64(46) + int64(binary.LittleEndian.Uint16(header[28:30])) + int64(binary.LittleEndian.Uint16(header[30:32])) + int64(binary.LittleEndian.Uint16(header[32:34]))
		if span > directoryEnd-offset {
			return zip.ErrFormat
		}
		offset += span
	}
	if uint64(count) != records {
		return zip.ErrFormat
	}
	return nil
}
