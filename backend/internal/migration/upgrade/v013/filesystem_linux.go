//go:build linux

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
		// FUSE can represent sshfs and other remote filesystems; their rename
		// and durability guarantees are outside this offline tool's contract.
		switch uint64(stat.Type) {
		case 0x6969, 0x517B, 0xFF534D42, 0x73757245, 0x01021997, 0x00C36400, 0x5346414F, 0x65735546:
			return errors.New("local migration does not support network or FUSE filesystems")
		}
		return nil
	}
}

func renameLocalNoReplace(source, target string) error {
	return unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, target, unix.RENAME_NOREPLACE)
}
