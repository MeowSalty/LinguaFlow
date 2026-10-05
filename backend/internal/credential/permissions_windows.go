//go:build windows

package credential

import (
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

func checkFilePermissions(f *os.File) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	sd, err := windows.GetSecurityInfo(windows.Handle(f.Fd()), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	defer runtime.KeepAlive(sd)
	return checkSecurityDescriptor(sd, user.User.Sid, system)
}

func checkSecurityDescriptor(sd *windows.SECURITY_DESCRIPTOR, user, system *windows.SID) error {
	privatePrincipal := func(sid *windows.SID) bool {
		return sid != nil && sid.IsValid() && (sid.Equals(user) || sid.Equals(system))
	}
	denied := func() error {
		return fmt.Errorf("%w: owner and protected Windows ACL must restrict access to the current account and SYSTEM", os.ErrPermission)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	if !privatePrincipal(owner) {
		return denied()
	}
	control, _, err := sd.Control()
	if err != nil {
		return err
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return denied()
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	// 缺失或为 NULL 的 DACL 会向所有人授予完全控制权。
	if dacl == nil {
		return denied()
	}
	for i := uint16(0); i < dacl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(i), &ace); err != nil {
			return err
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 || ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		// 只接受普通的允许/拒绝 ACE。条件 ACE 或对象 ACE 需要完整的授权评估，
		// 绝不能绕过本策略。
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || uintptr(ace.Header.AceSize) < unsafe.Sizeof(*ace) {
			return denied()
		}
		if ace.Mask != 0 && !privatePrincipal((*windows.SID)(unsafe.Pointer(&ace.SidStart))) {
			return denied()
		}
	}
	return nil
}

// NTFS 会为硬链接发布记录日志；Unix 的目录句柄需要显式同步，
// 而 Windows 不提供目录级 FlushFileBuffers 语义。
func syncDirectory(string) error { return nil }
