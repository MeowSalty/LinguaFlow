//go:build darwin

package operation

import "golang.org/x/sys/unix"

func renameNoReplace(a, b string) error { return unix.RenamexNp(a, b, unix.RENAME_EXCL) }
