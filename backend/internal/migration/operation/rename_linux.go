//go:build linux

package operation

import "golang.org/x/sys/unix"

func renameNoReplace(a, b string) error {
	return unix.Renameat2(unix.AT_FDCWD, a, unix.AT_FDCWD, b, unix.RENAME_NOREPLACE)
}
