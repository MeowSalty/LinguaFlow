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
	privatePrincipal := func(sid *windows.SID) bool {
		return sid != nil && sid.IsValid() && (sid.Equals(user.User.Sid) || sid.Equals(system))
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
	// An absent or NULL DACL grants everyone full control.
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
		// Only ordinary allow/deny ACEs are accepted. Conditional or object ACEs
		// need a full authorization evaluation and must not bypass this policy.
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || uintptr(ace.Header.AceSize) < unsafe.Sizeof(*ace) {
			return denied()
		}
		if ace.Mask != 0 && !privatePrincipal((*windows.SID)(unsafe.Pointer(&ace.SidStart))) {
			return denied()
		}
	}
	return nil
}

func restrictFile(path string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;SY)(A;;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	// Elevated tokens can otherwise default the owner to Administrators. Set
	// the owner explicitly so generated files satisfy the same read policy.
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, user.User.Sid, nil, dacl, nil)
}

// NTFS journals hard-link publication; Unix directory handles need an explicit
// sync, whereas Windows does not expose directory FlushFileBuffers semantics.
func syncDirectory(string) error { return nil }
