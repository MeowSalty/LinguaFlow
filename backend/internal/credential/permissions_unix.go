//go:build !windows

package credential

import (
	"errors"
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

func createPrivateTempFile(dir string) (*os.File, error) {
	f, err := os.CreateTemp(dir, ".private-*")
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0600); err != nil {
		return nil, errors.Join(err, f.Close(), os.Remove(f.Name()))
	}
	if err := checkFilePermissions(f); err != nil {
		return nil, errors.Join(err, f.Close(), os.Remove(f.Name()))
	}
	return f, nil
}

func createPrivateDirectory(path string) (created bool, err error) {
	if err := os.Mkdir(path, 0700); err != nil {
		return false, err
	}
	if err := restrictDirectory(path); err != nil {
		return true, errors.Join(err, os.Remove(path))
	}
	info, err := os.Lstat(path)
	if err == nil && (!info.IsDir() || info.Mode().Perm() != 0700) {
		err = fmt.Errorf("verify private directory %q: %w", path, os.ErrPermission)
	}
	if err != nil {
		return true, errors.Join(err, os.Remove(path))
	}
	return true, nil
}

func restrictDirectory(path string) error { return os.Chmod(path, 0700) }

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
