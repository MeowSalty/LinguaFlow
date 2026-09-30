//go:build !windows

package credential

import (
	"fmt"
	"os"
)

func checkFilePermissions(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("%w: group and other access must be disabled (use mode 0600 or 0400)", os.ErrPermission)
	}
	return nil
}

func restrictFile(path string) error { return os.Chmod(path, 0600) }

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
