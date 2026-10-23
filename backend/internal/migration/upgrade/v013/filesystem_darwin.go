//go:build darwin

package v013

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func validateLocalVolume(path string) error {
	for {
		var stat unix.Statfs_t
		err := unix.Statfs(path, &stat)
		if errors.Is(err, os.ErrNotExist) && filepath.Dir(path) != path {
			path = filepath.Dir(path)
			continue
		}
		if err != nil {
			return err
		}
		if stat.Flags&unix.MNT_LOCAL == 0 {
			return errors.New("local migration does not support network filesystems")
		}
		return nil
	}
}

func renameLocalNoReplace(source, target string) error {
	return unix.RenamexNp(source, target, unix.RENAME_EXCL)
}
