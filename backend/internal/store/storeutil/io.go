// Package storeutil 保存各适配器共享的有界字节与 key 规则。
package storeutil

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

// ValidateKey 刻意只接受一个小巧的可移植子集：key 是不透明的服务端生成
// 标识符，绝非用户路径。保留前缀属于 Local 的可恢复发布文件。
func ValidateKey(key string) error {
	if len(key) == 0 || len(key) > 1024 || strings.ContainsAny(key, "\\:*?\"<>|\x00") {
		return storage.ErrInvalidKey
	}
	for _, part := range strings.Split(key, "/") {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, ".lf-") || strings.TrimSpace(part) != part || strings.HasSuffix(part, ".") {
			return storage.ErrInvalidKey
		}
		for _, c := range part {
			if c < 32 || c == 127 {
				return storage.ErrInvalidKey
			}
		}
		base, _, _ := strings.Cut(strings.ToUpper(part), ".")
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || (len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '0' && base[3] <= '9') {
			return storage.ErrInvalidKey
		}
	}
	return nil
}

// ExactReader 限制读取量，并在把最后一块交给写入方之前检查是否存在多余字节。
// 取消操作无法中断任意阻塞中的 Reader；取消上传时调用方必须自行关闭
// 网络请求体。
type ExactReader struct {
	ctx       context.Context
	r         io.Reader
	remaining int64
	done      bool
	err       error
}

func NewExactReader(ctx context.Context, r io.Reader, size int64) (*ExactReader, error) {
	if size < 0 || r == nil {
		return nil, storage.ErrLimit
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &ExactReader{ctx: ctx, r: r, remaining: size}, nil
}

func (r *ExactReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		r.err = err
		return 0, err
	}
	if r.err != nil {
		return 0, r.err
	}
	if r.done {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	if r.remaining == 0 {
		return 0, r.finish()
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.r.Read(p)
	r.remaining -= int64(n)
	if err != nil && !errors.Is(err, io.EOF) {
		r.err = err
		return n, err
	}
	if r.remaining == 0 {
		if errors.Is(err, io.EOF) {
			r.done = true
			return n, io.EOF
		}
		return n, r.finish()
	}
	if errors.Is(err, io.EOF) {
		r.err = storage.ErrCorrupt
		return n, r.err
	}
	return n, nil
}

func (r *ExactReader) finish() error {
	var extra [1]byte
	n, err := io.ReadFull(r.r, extra[:])
	if n > 0 {
		r.err = storage.ErrLimit
		return r.err
	}
	if !errors.Is(err, io.EOF) {
		r.err = err
		return err
	}
	r.done = true
	return io.EOF
}

// Complete 在提供方报告成功之后调用。特别要注意：HTTP 客户端可能
// 完全不读取零长度的请求体。
func (r *ExactReader) Complete() error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	if r.err != nil {
		return r.err
	}
	if r.remaining != 0 {
		return storage.ErrCorrupt
	}
	if !r.done {
		if err := r.finish(); !errors.Is(err, io.EOF) {
			return err
		}
	}
	return nil
}

type ContextReadCloser struct {
	Context context.Context
	io.ReadCloser
}

func (r *ContextReadCloser) Read(p []byte) (int, error) {
	if err := r.Context.Err(); err != nil {
		return 0, err
	}
	return r.ReadCloser.Read(p)
}
