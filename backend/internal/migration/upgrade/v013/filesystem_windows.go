//go:build windows

package v013

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func validateLocalVolume(path string) error {
	if strings.HasPrefix(path, `\\`) {
		return errors.New("local migration does not support network or device paths")
	}
	root, err := windows.UTF16PtrFromString(filepath.VolumeName(path) + `\`)
	if err != nil {
		return err
	}
	if windows.GetDriveType(root) != windows.DRIVE_FIXED {
		return errors.New("local migration requires a local fixed disk")
	}
	return validateSQLitePathSpelling(path)
}

// SQLite migration classifies credential paths relative to the directory that
// will be swapped. Reject alternate Windows spellings rather than letting an
// internal key appear external and remain tied to the old directory. This is
// deliberately migration-only; the credential package supports native paths.
func validateSQLitePathSpelling(path string) (err error) {
	if strings.HasPrefix(path, `\\?\`) || strings.HasPrefix(path, `\\.\`) {
		return errors.New("SQLite migration requires standard absolute paths without extended or device prefixes")
	}
	current := path
	var file *os.File
	for {
		file, err = os.Open(current)
		if err == nil {
			break
		}
		parent := filepath.Dir(current)
		if !errors.Is(err, os.ErrNotExist) || parent == current {
			return err
		}
		current = parent
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	buffer := make([]uint16, 512)
	for {
		// Flags 0 requests FILE_NAME_NORMALIZED and VOLUME_NAME_DOS, expanding
		// short names and resolving drive aliases to the actual directory.
		n, err := windows.GetFinalPathNameByHandle(windows.Handle(file.Fd()), &buffer[0], uint32(len(buffer)), 0)
		if err != nil {
			return fmt.Errorf("resolve SQLite migration path: %w", err)
		}
		if n >= uint32(len(buffer)) {
			if n >= 32768 {
				return errors.New("SQLite migration path exceeds the Windows path limit")
			}
			buffer = make([]uint16, n+1)
			continue
		}
		canonical := windows.UTF16ToString(buffer[:n])
		if strings.HasPrefix(canonical, `\\?\UNC\`) {
			canonical = `\\` + strings.TrimPrefix(canonical, `\\?\UNC\`)
		} else {
			canonical = strings.TrimPrefix(canonical, `\\?\`)
		}
		if !sameLocalPath(filepath.Clean(current), filepath.Clean(canonical)) {
			return errors.New("SQLite migration requires standard absolute paths without short-name or drive aliases")
		}
		return nil
	}
}

func renameLocalNoReplace(source, target string) error {
	from, err := windows.UTF16PtrFromString(`\\?\` + filepath.Clean(source))
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(`\\?\` + filepath.Clean(target))
	if err != nil {
		return err
	}
	// No REPLACE_EXISTING and no COPY_ALLOWED: one same-volume rename must
	// either preserve both existing directories or move into an absent name.
	return windows.MoveFileEx(from, to, 0)
}

func rejectReparsePoint(path string) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attributes, err := windows.GetFileAttributes(name)
	if err != nil {
		return err
	}
	if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("local migration refuses reparse points: %s", path)
	}
	return nil
}

func directoryIdentity(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		return "", err
	}
	return fmt.Sprintf("%08x:%08x%08x", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow), nil
}

func lockMigrationFile(file *os.File) (func() error, error) {
	overlapped := &windows.Overlapped{}
	handle := windows.Handle(file.Fd())
	if err := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlapped); err != nil {
		return nil, fmt.Errorf("another local migration holds the directory lock: %w", err)
	}
	return func() error { return windows.UnlockFileEx(handle, 0, 1, 0, overlapped) }, nil
}

// Windows has no directory FlushFileBuffers equivalent. Recovery verifies
// identities and receipts rather than assuming a pair of renames is atomic.
func syncLocalDirectory(string) error { return nil }
