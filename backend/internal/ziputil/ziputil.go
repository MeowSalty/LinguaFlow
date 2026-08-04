// Package ziputil 提供带解压尺寸防护的 ZIP 读写辅助函数。
//
// OpenZip 优先走 io.ReaderAt 零拷贝路径（os.File / bytes.Reader），
// 仅在 Reader 不支持随机访问时回退到全量缓冲。
package ziputil

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
)

// MaxDecompressedEntrySize 是单个 ZIP 条目解压后的默认最大允许字节数。
// 用于防御高压缩率 zip 炸弹：压缩流上限不能阻止解压后内存爆炸。
// 仅在需要将条目完整读入内存（解析/翻译）时应用；
// 原样直通的资产复制走 CopyEntryUnbounded，不受此限制。
const MaxDecompressedEntrySize int64 = 200 << 20

// ErrDecompressedSizeExceeded 在条目解压后字节数超过调用方指定的上限时返回。
// 调用方可通过 errors.Is(err, ErrDecompressedSizeExceeded) 区分“超限”与其它读取失败。
var ErrDecompressedSizeExceeded = errors.New("ziputil: decompressed size exceeds limit")

// OpenZip 从 io.Reader 打开 zip，优先零拷贝路径。
//
// 若 r 同时实现 io.Seeker 与 io.ReaderAt（如 *os.File、*bytes.Reader），
// 用 Seek 取 size 后 zip.NewReader(ra, size) 随机访问，不缓冲整个归档。
// 否则回退到 io.LimitReader + io.ReadAll（带 maxCompressed 上限）。
//
// 重要：接口检查必须在任何 io.LimitReader 包裹之前。
func OpenZip(r io.Reader, maxCompressed int64) (*zip.Reader, error) {
	// 零拷贝路径：先检查接口，避免 LimitReader 隐藏底层类型。
	if seeker, ok := r.(io.Seeker); ok {
		if ra, ok := r.(io.ReaderAt); ok {
			size, err := seeker.Seek(0, io.SeekEnd)
			if err != nil {
				return nil, fmt.Errorf("ziputil: seek end: %w", err)
			}
			if _, err := seeker.Seek(0, io.SeekStart); err != nil {
				return nil, fmt.Errorf("ziputil: seek start: %w", err)
			}
			if size > maxCompressed {
				return nil, fmt.Errorf("ziputil: compressed size %d exceeds max %d", size, maxCompressed)
			}
			zr, err := zip.NewReader(ra, size)
			if err != nil {
				return nil, fmt.Errorf("ziputil: open zip: %w", err)
			}
			return zr, nil
		}
	}

	// 回退：全量读取（带压缩上限）。
	lr := io.LimitReader(r, maxCompressed+1)
	data, err := io.ReadAll(lr)
	if err != nil {
		return nil, fmt.Errorf("ziputil: read: %w", err)
	}
	if int64(len(data)) > maxCompressed {
		return nil, fmt.Errorf("ziputil: compressed size exceeds max %d", maxCompressed)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("ziputil: open zip: %w", err)
	}
	return zr, nil
}

// ReadFile 打开单个 zip.File 并读取其解压内容，带 maxDecompressed 上限。
func ReadFile(f *zip.File, maxDecompressed int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("ziputil: open %q: %w", f.Name, err)
	}
	defer rc.Close()
	data, err := ReadBounded(rc, maxDecompressed)
	if err != nil {
		return nil, fmt.Errorf("ziputil: read %q: %w", f.Name, err)
	}
	return data, nil
}

// ReadEntry 在 zip.Reader 中按 name 查找条目并读取（path.Clean 规范化匹配）。
func ReadEntry(zr *zip.Reader, name string, maxDecompressed int64) ([]byte, error) {
	clean := path.Clean(name)
	clean = trimLeadingSlash(clean)
	for _, f := range zr.File {
		if path.Clean(f.Name) == clean || f.Name == name {
			return ReadFile(f, maxDecompressed)
		}
	}
	return nil, fmt.Errorf("ziputil: entry %q not found", name)
}

// CopyEntry 将 src 条目流式复制到 zw，保留原始 FileHeader，带解压上限。
func CopyEntry(zw *zip.Writer, src *zip.File, maxDecompressed int64) error {
	rc, err := src.Open()
	if err != nil {
		return fmt.Errorf("ziputil: open %q: %w", src.Name, err)
	}
	defer rc.Close()

	w, err := zw.CreateHeader(&src.FileHeader)
	if err != nil {
		return fmt.Errorf("ziputil: create header %q: %w", src.Name, err)
	}

	lr := io.LimitReader(rc, maxDecompressed+1)
	n, err := io.Copy(w, lr)
	if err != nil {
		return fmt.Errorf("ziputil: copy %q: %w", src.Name, err)
	}
	if n > maxDecompressed {
		return fmt.Errorf("%w: entry %q (%d bytes)", ErrDecompressedSizeExceeded, src.Name, maxDecompressed)
	}
	return nil
}

// CopyEntryUnbounded 将 src 条目流式复制到 zw，保留原始 FileHeader，不限制解压大小。
//
// 用于原样直通的资产（字体、图片、CSS、音频等）：这些条目不经解析、
// 不进入内存，直接从压缩流拷到目标压缩流，因此不存在解压炸弹的内存风险。
// 解压上限仅对需要全量缓冲的读取路径有意义。
func CopyEntryUnbounded(zw *zip.Writer, src *zip.File) error {
	rc, err := src.Open()
	if err != nil {
		return fmt.Errorf("ziputil: open %q: %w", src.Name, err)
	}
	defer rc.Close()

	w, err := zw.CreateHeader(&src.FileHeader)
	if err != nil {
		return fmt.Errorf("ziputil: create header %q: %w", src.Name, err)
	}

	if _, err := io.Copy(w, rc); err != nil {
		return fmt.Errorf("ziputil: copy %q: %w", src.Name, err)
	}
	return nil
}

// WriteEntry 将 data 写入 zw 条目，指定压缩方法。
func WriteEntry(zw *zip.Writer, name string, data []byte, method uint16) error {
	header := &zip.FileHeader{
		Name:   name,
		Method: method,
	}
	w, err := zw.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("ziputil: create header %q: %w", name, err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("ziputil: write %q: %w", name, err)
	}
	return nil
}

// ReadBounded 读取 r 全部内容，限制不超过 max 字节，超出返回错误。
func ReadBounded(r io.Reader, max int64) ([]byte, error) {
	lr := io.LimitReader(r, max+1)
	data, err := io.ReadAll(lr)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%w (%d bytes)", ErrDecompressedSizeExceeded, max)
	}
	return data, nil
}

func trimLeadingSlash(name string) string {
	for len(name) > 0 && name[0] == '/' {
		name = name[1:]
	}
	return name
}
