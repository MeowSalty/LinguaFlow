//go:build windows

package operation

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func foldPath(s string) string { return strings.ToLower(s) }

// Atomic replacement must remain possible while read-only status/verify holds
// the old document handle. The handle continues reading that complete version.
func openRead(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

func ordinaryPath(path string) error {
	if strings.HasPrefix(path, `\\`) {
		return Fail(Incompatible, "network, device and extended paths are not accepted")
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attrs, err := windows.GetFileAttributes(p)
	if err != nil {
		return err
	}
	if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return Fail(Incompatible, "reparse points are not accepted")
	}
	buffer := make([]uint16, 32768)
	var n uint32
	if attrs&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		// A journal read may keep an unlinked old version open while its name
		// already denotes the replacement. Validate spelling at the name; every
		// ancestor directory still receives the stronger handle identity check.
		n, err = windows.GetLongPathName(p, &buffer[0], uint32(len(buffer)))
	} else {
		f, openErr := openRead(path)
		if openErr != nil {
			return openErr
		}
		defer f.Close()
		n, err = windows.GetFinalPathNameByHandle(windows.Handle(f.Fd()), &buffer[0], uint32(len(buffer)), 0)
	}
	if err != nil {
		return err
	}
	if n >= uint32(len(buffer)) {
		return Fail(Incompatible, "path exceeds limit")
	}
	canonical := strings.TrimPrefix(windows.UTF16ToString(buffer[:n]), `\\?\`)
	if !strings.EqualFold(filepath.Clean(canonical), filepath.Clean(path)) {
		return Fail(Incompatible, "short-name and drive aliases are not accepted")
	}
	return nil
}
func Identity(path string) (string, error) {
	f, err := openRead(path)
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
func lockFile(f *os.File) (func() error, error) {
	overlapped := &windows.Overlapped{}
	h := windows.Handle(f.Fd())
	if err := windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlapped); err != nil {
		return nil, err
	}
	return func() error { return windows.UnlockFileEx(h, 0, 1, 0, overlapped) }, nil
}
func renameNoReplace(a, b string) error {
	from, err := windows.UTF16PtrFromString(a)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(b)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH)
}

func replaceFile(a, b string) error {
	from, err := windows.UTF16PtrFromString(a)
	if err != nil {
		return err
	}
	to, err := windows.UTF16FromString(filepath.Base(b))
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(from, windows.DELETE|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	parent, err := openRead(filepath.Dir(b))
	if err != nil {
		return err
	}
	defer parent.Close()
	// Win32 MoveFileEx cannot replace a file with outstanding readers even
	// when they share DELETE. Windows 10 POSIX rename keeps those old handles
	// valid while publishing the complete replacement at the same name.
	type renameInfo struct {
		Flags          uint32
		RootDirectory  windows.Handle
		FileNameLength uint32
		FileName       [1]uint16
	}
	var info renameInfo
	length := (len(to) - 1) * 2
	buffer := make([]byte, int(unsafe.Offsetof(info.FileName))+length)
	rename := (*renameInfo)(unsafe.Pointer(&buffer[0]))
	rename.Flags = windows.FILE_RENAME_REPLACE_IF_EXISTS | windows.FILE_RENAME_POSIX_SEMANTICS
	rename.RootDirectory = windows.Handle(parent.Fd())
	rename.FileNameLength = uint32(length)
	copy(unsafe.Slice(&rename.FileName[0], len(to)-1), to[:len(to)-1])
	var status windows.IO_STATUS_BLOCK
	// FileRenameInformationEx is the NT information class corresponding to
	// Win32 FileRenameInfoEx and supports the POSIX replacement flag.
	const fileRenameInformationEx = 65
	if err := windows.NtSetInformationFile(h, &status, &buffer[0], uint32(len(buffer)), fileRenameInformationEx); err != nil {
		return err
	}
	return windows.FlushFileBuffers(h)
}

// Windows has no directory fsync; durable receipts and identities cover recovery.
func syncDir(string) error { return nil }
