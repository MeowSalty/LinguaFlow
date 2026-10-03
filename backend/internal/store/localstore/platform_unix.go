//go:build !windows

package localstore

import (
	"os"

	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

const directorySyncSupported = true

func ordinary(info os.FileInfo) error {
	if !info.Mode().IsRegular() && !info.IsDir() {
		return storage.ErrInvalidKey
	}
	return nil
}

func syncDirectory(root *os.Root, name string) error {
	dir, err := root.Open(name)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
