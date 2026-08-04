package ziputil

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

// onlyReader 仅实现 io.Reader，用于测试 OpenZip 回退路径。
type onlyReader struct {
	r io.Reader
}

func (o onlyReader) Read(p []byte) (int, error) { return o.r.Read(p) }

func makeTestZip(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create entry %q: %v", name, err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("write entry %q: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

func TestOpenZip_ZeroCopy(t *testing.T) {
	raw := makeTestZip(t, map[string][]byte{
		"hello.txt": []byte("hello world"),
	})
	r := bytes.NewReader(raw)

	zr, err := OpenZip(r, 1<<20)
	if err != nil {
		t.Fatalf("OpenZip: %v", err)
	}
	data, err := ReadEntry(zr, "hello.txt", 1<<20)
	if err != nil {
		t.Fatalf("ReadEntry: %v", err)
	}
	if string(data) != "hello world" {
		t.Fatalf("got %q, want %q", data, "hello world")
	}
}

func TestOpenZip_Fallback(t *testing.T) {
	raw := makeTestZip(t, map[string][]byte{
		"a.txt": []byte("fallback"),
	})
	// 仅实现 Read，强制走回退路径。
	r := onlyReader{r: bytes.NewReader(raw)}

	zr, err := OpenZip(r, 1<<20)
	if err != nil {
		t.Fatalf("OpenZip: %v", err)
	}
	data, err := ReadEntry(zr, "a.txt", 1<<20)
	if err != nil {
		t.Fatalf("ReadEntry: %v", err)
	}
	if string(data) != "fallback" {
		t.Fatalf("got %q, want %q", data, "fallback")
	}
}

func TestOpenZip_TooLarge(t *testing.T) {
	raw := makeTestZip(t, map[string][]byte{
		"big.txt": []byte(strings.Repeat("x", 100)),
	})

	// 零拷贝路径：size 超限
	_, err := OpenZip(bytes.NewReader(raw), 10)
	if err == nil {
		t.Fatal("expected error for oversized zero-copy input")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("unexpected error: %v", err)
	}

	// 回退路径：size 超限
	_, err = OpenZip(onlyReader{r: bytes.NewReader(raw)}, 10)
	if err == nil {
		t.Fatal("expected error for oversized fallback input")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReadBounded_Overflow(t *testing.T) {
	r := strings.NewReader(strings.Repeat("a", 100))
	_, err := ReadBounded(r, 50)
	if err == nil {
		t.Fatal("expected overflow error")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !errors.Is(err, ErrDecompressedSizeExceeded) {
		t.Fatalf("expected errors.Is(err, ErrDecompressedSizeExceeded), got %v", err)
	}
}

func TestCopyEntry_Overflow(t *testing.T) {
	// 构造一个可解压但限制极小的条目。
	raw := makeTestZip(t, map[string][]byte{
		"payload.txt": []byte(strings.Repeat("z", 200)),
	})
	src, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	if len(src.File) != 1 {
		t.Fatalf("expected 1 file, got %d", len(src.File))
	}

	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	err = CopyEntry(zw, src.File[0], 50)
	if err == nil {
		t.Fatal("expected overflow error from CopyEntry")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !errors.Is(err, ErrDecompressedSizeExceeded) {
		t.Fatalf("expected errors.Is(err, ErrDecompressedSizeExceeded), got %v", err)
	}
	// 关闭可能因未写完失败，忽略。
	_ = zw.Close()
}

// TestCopyEntryUnbounded_NoLimit 验证直通复制路径不受解压尺寸限制，
// 能复制超过 capped 路径会拒绝的条目（模拟真实资产复制场景）。
func TestCopyEntryUnbounded_NoLimit(t *testing.T) {
	big := bytes.Repeat([]byte("z"), 500) // 显著大于旧 50 字节上限
	raw := makeTestZip(t, map[string][]byte{
		"asset.bin": big,
	})
	src, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}

	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	if err := CopyEntryUnbounded(zw, src.File[0]); err != nil {
		t.Fatalf("CopyEntryUnbounded: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// 读回校验内容一致。
	zr, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	got, err := ReadEntry(zr, "asset.bin", int64(len(big)))
	if err != nil {
		t.Fatalf("ReadEntry: %v", err)
	}
	if !bytes.Equal(got, big) {
		t.Fatalf("content mismatch: got %d bytes, want %d", len(got), len(big))
	}
}

func TestReadEntry_NotFound(t *testing.T) {
	raw := makeTestZip(t, map[string][]byte{
		"exists.txt": []byte("ok"),
	})
	zr, err := OpenZip(bytes.NewReader(raw), 1<<20)
	if err != nil {
		t.Fatalf("OpenZip: %v", err)
	}
	_, err = ReadEntry(zr, "missing.txt", 1<<20)
	if err == nil {
		t.Fatal("expected not found error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWriteEntry_AndRead(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if err := WriteEntry(zw, "w.txt", []byte("written"), zip.Deflate); err != nil {
		t.Fatalf("WriteEntry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	zr, err := OpenZip(bytes.NewReader(buf.Bytes()), 1<<20)
	if err != nil {
		t.Fatalf("OpenZip: %v", err)
	}
	data, err := ReadEntry(zr, "w.txt", 1<<20)
	if err != nil {
		t.Fatalf("ReadEntry: %v", err)
	}
	if string(data) != "written" {
		t.Fatalf("got %q", data)
	}
}

func TestReadFile_UsesMax(t *testing.T) {
	raw := makeTestZip(t, map[string][]byte{
		"f.txt": []byte("0123456789"),
	})
	zr, err := OpenZip(bytes.NewReader(raw), 1<<20)
	if err != nil {
		t.Fatalf("OpenZip: %v", err)
	}
	_, err = ReadFile(zr.File[0], 5)
	if err == nil {
		t.Fatal("expected overflow")
	}
	// 正常读取
	data, err := ReadFile(zr.File[0], 100)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "0123456789" {
		t.Fatalf("got %q", data)
	}
}

// 确保 onlyReader 真的不暴露 Seeker/ReaderAt。
func TestOnlyReader_NoExtraInterfaces(t *testing.T) {
	var r io.Reader = onlyReader{r: strings.NewReader("x")}
	if _, ok := r.(io.Seeker); ok {
		t.Fatal("onlyReader should not implement Seeker")
	}
	if _, ok := r.(io.ReaderAt); ok {
		t.Fatal("onlyReader should not implement ReaderAt")
	}
	// 防止编译器优化掉未使用导入
	_ = fmt.Sprintf("%T", r)
}
