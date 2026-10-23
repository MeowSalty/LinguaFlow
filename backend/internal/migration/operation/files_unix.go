//go:build !windows

package operation

import (
	"fmt"
	"os"
	"syscall"
)

func replaceFile(a, b string) error { return os.Rename(a, b) }

func openRead(path string) (*os.File, error) { return os.Open(path) }

func foldPath(s string) string  { return s }
func ordinaryPath(string) error { return nil }
func Identity(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", Fail(Incompatible, "filesystem identity unavailable")
	}
	return fmt.Sprintf("%x:%x", stat.Dev, stat.Ino), nil
}
func lockFile(f *os.File) (func() error, error) {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, err
	}
	return func() error { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }, nil
}
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
