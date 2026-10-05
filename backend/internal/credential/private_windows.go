//go:build windows

package credential

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// privateWindowsPath 对普通路径保留 Win32 规范化，仅在需要时添加扩展前缀，
// 与 os.OpenFile 处理长路径的方式一致。
func privateWindowsPath(path string) (*uint16, error) {
	if path == "" {
		return nil, windows.ERROR_PATH_NOT_FOUND
	}
	native := filepath.FromSlash(path)
	if strings.HasPrefix(native, `\\?\`) || strings.HasPrefix(native, `\??\`) || strings.HasPrefix(native, `\\.\`) {
		return windows.UTF16PtrFromString(native)
	}
	full, err := windows.FullPath(native)
	if err != nil {
		return nil, err
	}
	// 目录创建还要给 MAX_PATH 以内的 8.3 短文件名留出余量。
	if len(utf16.Encode([]rune(full))) >= 248 {
		if strings.HasPrefix(full, `\\`) {
			native = `\\?\UNC\` + full[2:]
		} else {
			native = `\\?\` + full
		}
	}
	return windows.UTF16PtrFromString(native)
}

func privateWindowsSecurity(directory bool) (*windows.SECURITY_DESCRIPTOR, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	inheritance := ""
	if directory {
		inheritance = "OICI"
	}
	// 创建时显式指定所有者，也顺带覆盖了默认所有者为 Administrators 的提权令牌，
	// 无需事后请求 WRITE_OWNER。
	return windows.SecurityDescriptorFromString("O:" + user.User.Sid.String() +
		"D:P(A;" + inheritance + ";FA;;;SY)(A;" + inheritance + ";FA;;;" + user.User.Sid.String() + ")")
}

func createPrivateTempFile(dir string) (*os.File, error) {
	if dir == "" {
		dir = os.TempDir()
	}
	sd, err := privateWindowsSecurity(false)
	if err != nil {
		return nil, fmt.Errorf("build private file security descriptor: %w", err)
	}
	defer runtime.KeepAlive(sd)
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	for range 128 {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, fmt.Errorf("generate private temporary filename: %w", err)
		}
		path := filepath.Join(dir, ".private-"+hex.EncodeToString(random[:]))
		native, err := privateWindowsPath(path)
		if err != nil {
			return nil, &os.PathError{Op: "create private temporary file", Path: path, Err: err}
		}
		handle, err := windows.CreateFile(native, windows.GENERIC_READ|windows.GENERIC_WRITE,
			windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, &sa, windows.CREATE_NEW,
			windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			continue
		}
		if err != nil {
			return nil, &os.PathError{Op: "create private temporary file", Path: path, Err: err}
		}
		file := os.NewFile(uintptr(handle), path)
		if err := checkCreatedPrivateHandle(handle, false); err != nil {
			return nil, errors.Join(&os.PathError{Op: "verify private temporary file", Path: path, Err: err}, file.Close(), os.Remove(path))
		}
		return file, nil
	}
	return nil, &os.PathError{Op: "create private temporary file", Path: dir, Err: os.ErrExist}
}

func createPrivateDirectory(path string) (created bool, err error) {
	sd, err := privateWindowsSecurity(true)
	if err != nil {
		return false, err
	}
	defer runtime.KeepAlive(sd)
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	native, err := privateWindowsPath(path)
	if err != nil {
		return false, err
	}
	if err := windows.CreateDirectory(native, &sa); err != nil {
		return false, err
	}
	// 只有本次调用创建的目录才会走到这段清理逻辑。
	defer func() {
		if err != nil {
			err = errors.Join(err, os.Remove(path))
		}
	}()
	handle, err := openPrivateDirectory(path, windows.READ_CONTROL)
	if err != nil {
		return true, err
	}
	defer func() { err = errors.Join(err, windows.CloseHandle(handle)) }()
	return true, checkCreatedPrivateHandle(handle, true)
}

func openPrivateDirectory(path string, access uint32) (windows.Handle, error) {
	native, err := privateWindowsPath(path)
	if err != nil {
		return windows.InvalidHandle, &os.PathError{Op: "open private directory", Path: path, Err: err}
	}
	// 仅含元数据访问权的句柄不参与 Windows 的共享检查。加入目录列举权限后，
	// 被省略的删除共享才能真正钉住对象；该权限本就包含在本操作所支持的
	// Modify 权限之中。
	handle, err := windows.CreateFile(native, access|windows.FILE_LIST_DIRECTORY, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return windows.InvalidHandle, &os.PathError{Op: "open private directory", Path: path, Err: err}
	}
	var info windows.ByHandleFileInformation
	err = windows.GetFileInformationByHandle(handle, &info)
	if err == nil && (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0) {
		err = fmt.Errorf("must be a directory without a reparse point: %w", os.ErrPermission)
	}
	if err != nil {
		return windows.InvalidHandle, errors.Join(&os.PathError{Op: "inspect private directory", Path: path, Err: err}, windows.CloseHandle(handle))
	}
	return handle, nil
}

func restrictDirectory(path string) (err error) {
	sd, err := privateWindowsSecurity(true)
	if err != nil {
		return fmt.Errorf("build private directory security descriptor %q: %w", path, err)
	}
	defer runtime.KeepAlive(sd)
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	wantedOwner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	// 所有者隐式拥有 WRITE_DAC，但没有 WRITE_OWNER。在两个阶段全程保持此句柄
	// 以不含删除共享的方式打开，从而钉住目录。
	handle, err := openPrivateDirectory(path, windows.READ_CONTROL|windows.WRITE_DAC)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, windows.CloseHandle(handle)) }()
	if err := windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		return &os.PathError{Op: "set private directory DACL", Path: path, Err: err}
	}
	actual, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return &os.PathError{Op: "read private directory owner", Path: path, Err: err}
	}
	defer runtime.KeepAlive(actual)
	owner, _, err := actual.Owner()
	if err != nil {
		return err
	}
	if owner == nil || !owner.Equals(wantedOwner) {
		// 修改 DACL 后，已有句柄的已授予访问权不会随之增加。
		var ownerHandle windows.Handle
		ownerHandle, err = openPrivateDirectory(path, windows.READ_CONTROL|windows.WRITE_OWNER)
		if err != nil {
			return fmt.Errorf("open private directory for owner update %q: %w", path, err)
		}
		defer func() { err = errors.Join(err, windows.CloseHandle(ownerHandle)) }()
		var before, after windows.ByHandleFileInformation
		if err := windows.GetFileInformationByHandle(handle, &before); err != nil {
			return err
		}
		if err := windows.GetFileInformationByHandle(ownerHandle, &after); err != nil {
			return err
		}
		if before.VolumeSerialNumber != after.VolumeSerialNumber || before.FileIndexHigh != after.FileIndexHigh || before.FileIndexLow != after.FileIndexLow {
			return fmt.Errorf("private directory changed during owner update %q: %w", path, os.ErrPermission)
		}
		if err := windows.SetSecurityInfo(ownerHandle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION, wantedOwner, nil, nil, nil); err != nil {
			return &os.PathError{Op: "set private directory owner", Path: path, Err: err}
		}
	}
	if err := checkCreatedPrivateHandle(handle, true); err != nil {
		return &os.PathError{Op: "verify private directory", Path: path, Err: err}
	}
	return nil
}

// 创建路径承诺的权限强于只读的密钥环策略：对象由当前用户所有，
// 且两个允许的主体均拥有完全控制权。
func checkCreatedPrivateHandle(handle windows.Handle, directory bool) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	defer runtime.KeepAlive(sd)
	if err := checkSecurityDescriptor(sd, user.User.Sid, system); err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	denied := func() error {
		return fmt.Errorf("private Windows object must be owned by the current account and grant only that account and SYSTEM full control: %w", os.ErrPermission)
	}
	if owner == nil || !owner.Equals(user.User.Sid) || dacl == nil || dacl.AceCount != 2 {
		return denied()
	}
	var flags byte
	if directory {
		flags = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
	}
	var userAllowed, systemAllowed bool
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return err
		}
		const fileFullControl = 0x1f01ff // FILE_ALL_ACCESS，含 WRITE_DAC 与 WRITE_OWNER。
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != flags || ace.Mask != fileFullControl {
			return denied()
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		userAllowed = userAllowed || sid.Equals(user.User.Sid)
		systemAllowed = systemAllowed || sid.Equals(system)
	}
	if !userAllowed || !systemAllowed {
		return denied()
	}
	return nil
}
