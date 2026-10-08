//go:build !windows

package v013

import (
	"fmt"
	"os"
	"syscall"
)

func rejectReparsePoint(string) error { return nil }

func validateSQLitePathSpelling(string) error { return nil }

func directoryIdentity(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("filesystem directory identity is unavailable")
	}
	return fmt.Sprintf("%x:%x", stat.Dev, stat.Ino), nil
}

func lockMigrationFile(file *os.File) (func() error, error) {
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, fmt.Errorf("another local migration holds the directory lock: %w", err)
	}
	return func() error { return syscall.Flock(int(file.Fd()), syscall.LOCK_UN) }, nil
}

func syncLocalDirectory(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}
