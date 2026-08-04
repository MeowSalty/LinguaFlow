package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// FileJob 描述一个文件翻译任务的输入输出路径。
type FileJob struct {
	InputPath  string
	OutputPath string
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

// createAtomicWriter 创建一个原子写入器。
// 先写临时文件，Close 时 rename 到目标路径。
func createAtomicWriter(path string) (io.WriteCloser, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("cli: mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".lf-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("cli: create temp: %w", err)
	}
	return &atomicFile{tmp: tmp, target: path}, nil
}

// nopWriteCloser 包装一个 io.Writer，使其满足 io.WriteCloser。
type nopWriteCloser struct {
	io.Writer
}

func (nopWriteCloser) Close() error { return nil }
