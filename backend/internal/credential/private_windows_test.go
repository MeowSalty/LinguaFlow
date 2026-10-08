//go:build windows

package credential

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 这些夹具只在 testing 分配的目录之下刻意改动 ACL，
// 绝不依赖、也绝不修复代码检出目录本身的安全设置。
func modifyOnlyParent(t *testing.T) string {
	t.Helper()
	if windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("the Modify-only regression must run with a non-elevated token")
	}
	parent := t.TempDir()
	setPrivateTestDACL(t, parent, "D:P(A;OICI;FA;;;SY)(A;OICI;0x1301bf;;;AU)")
	reference := filepath.Join(parent, "ordinary-file")
	if err := os.WriteFile(reference, nil, 0600); err != nil {
		t.Fatal(err)
	}
	assertLacksWriteOwner(t, reference)
	return parent
}

func setPrivateTestDACL(t *testing.T, path, sddl string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil)
	runtime.KeepAlive(sd)
	if err != nil {
		t.Fatalf("set fixture DACL for %q: %v", path, err)
	}
}

func openPrivateTestHandle(t *testing.T, path string, access uint32) windows.Handle {
	t.Helper()
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(p, access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatalf("open fixture %q with access %#x: %v", path, access, err)
	}
	t.Cleanup(func() {
		if err := windows.CloseHandle(h); err != nil {
			t.Errorf("close fixture handle: %v", err)
		}
	})
	return h
}

func assertLacksWriteOwner(t *testing.T, path string) {
	t.Helper()
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(p, windows.WRITE_OWNER,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err == nil {
		windows.CloseHandle(h)
		t.Fatal("invalid regression fixture: WRITE_OWNER was granted")
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("WRITE_OWNER probe failed for another reason: %v", err)
	}
	// 所有者权限仍允许修复 DACL；仅 Modify 权限时不得与
	// WRITE_DAC 或 WRITE_OWNER 混为一谈。
	openPrivateTestHandle(t, path, windows.READ_CONTROL|windows.WRITE_DAC)
}

func assertCreatedPrivateSecurity(t *testing.T, h windows.Handle, directory bool) {
	t.Helper()
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.KeepAlive(sd)
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.Equals(user.User.Sid) {
		t.Fatalf("created object owner is not the current account: %v", err)
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("created object DACL is not protected: %v", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		t.Fatalf("created object has no DACL: %v", err)
	}
	if dacl.AceCount != 2 {
		t.Fatalf("want exactly two private allow ACEs, got %d", dacl.AceCount)
	}
	wantFlags := byte(0)
	if directory {
		wantFlags = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
	}
	var foundUser, foundSystem bool
	for i := uint16(0); i < dacl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(i), &ace); err != nil {
			t.Fatal(err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE ||
			ace.Header.AceFlags != wantFlags || ace.Mask != 0x1f01ff { // FILE_ALL_ACCESS（完全控制）
			t.Fatalf("unexpected private ACE: type=%d flags=%#x mask=%#x", ace.Header.AceType, ace.Header.AceFlags, ace.Mask)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		switch {
		case sid.Equals(user.User.Sid):
			foundUser = true
		case sid.Equals(system):
			foundSystem = true
		default:
			t.Fatalf("unexpected allowed principal: %s", sid.String())
		}
	}
	if !foundUser || (!foundSystem && !user.User.Sid.Equals(system)) {
		t.Fatal("private ACL does not grant both the current account and SYSTEM")
	}
}

func TestPrivateCreationUnderModifyOnlyParent(t *testing.T) {
	parent := modifyOnlyParent(t)
	f, err := createPrivateTempFile(parent)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	assertCreatedPrivateSecurity(t, windows.Handle(f.Fd()), false)
	info, err := f.Stat()
	if err != nil || info.Size() != 0 {
		t.Fatalf("new private temporary file is not empty: %v", err)
	}
	var handleFlags uint32
	getHandleInformation := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetHandleInformation")
	ok, _, callErr := getHandleInformation.Call(f.Fd(), uintptr(unsafe.Pointer(&handleFlags)))
	if ok == 0 {
		t.Fatalf("inspect handle inheritance: %v", callErr)
	}
	if handleFlags&windows.HANDLE_FLAG_INHERIT != 0 {
		t.Fatal("private file handle is inheritable")
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareKeyring(filepath.Join(parent, "keyring.json"), true); err != nil {
		t.Fatalf("publish under Modify-only parent: %v", err)
	}
	dir := filepath.Join(parent, "new-private-directory")
	if err := CreatePrivateDirectory(dir); err != nil {
		t.Fatal(err)
	}
	assertCreatedPrivateSecurity(t, openPrivateTestHandle(t, dir, windows.READ_CONTROL), true)
	child := filepath.Join(dir, "inherited-child")
	if err := os.WriteFile(child, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(child, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sd.String(), ";;;AU)") {
		t.Fatal("child inherited the original broad parent permissions")
	}
}

func TestPreparePrivateDirectoryRepairsModifyOnlyOwner(t *testing.T) {
	parent := modifyOnlyParent(t)
	path := filepath.Join(parent, "existing-tool-directory")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	assertLacksWriteOwner(t, path)
	if err := PreparePrivateDirectory(path); err != nil {
		t.Fatalf("repair self-owned directory without WRITE_OWNER: %v", err)
	}
	assertCreatedPrivateSecurity(t, openPrivateTestHandle(t, path, windows.READ_CONTROL), true)
}

func TestCreatePrivateDirectoryCollisionPreservesExistingObject(t *testing.T) {
	for _, directory := range []bool{false, true} {
		name := "file"
		if directory {
			name = "directory"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "existing")
			content := path
			if directory {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				content = filepath.Join(path, "sentinel")
			}
			if err := os.WriteFile(content, []byte("must remain unchanged"), 0600); err != nil {
				t.Fatal(err)
			}
			before, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
				windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
			if err != nil {
				t.Fatal(err)
			}
			if err := CreatePrivateDirectory(path); !errors.Is(err, os.ErrExist) {
				t.Fatalf("exclusive creation did not report existing path: %v", err)
			}
			if !directory {
				if err := PreparePrivateDirectory(path); err == nil {
					t.Fatal("accepted an ordinary file as a private directory")
				}
			}
			after, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
				windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
			if err != nil || before.String() != after.String() {
				t.Fatalf("collision changed existing object security: %v", err)
			}
			data, err := os.ReadFile(content)
			if err != nil || !bytes.Equal(data, []byte("must remain unchanged")) {
				t.Fatalf("collision changed existing contents: %v", err)
			}
		})
	}
}

// 保留一个已获授权的句柄，让测试清理永远不必再从失败夹具安装的
// 刻意受限 ACL 中获取权限。
func restorePrivateTestDACL(t *testing.T, path string) {
	t.Helper()
	h := openPrivateTestHandle(t, path, windows.READ_CONTROL|windows.WRITE_DAC)
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		dacl, _, err := sd.DACL()
		if err == nil {
			err = windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT,
				windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
				nil, nil, dacl, nil)
		}
		runtime.KeepAlive(sd)
		if err != nil {
			t.Errorf("restore fixture DACL: %v", err)
		}
	})
}

func TestPrivateCreationDeniedDoesNotLeaveObjects(t *testing.T) {
	parent := t.TempDir()
	restorePrivateTestDACL(t, parent)
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	// 拒绝新增文件/子目录，同时允许 ReadDir，以便验证清理是否彻底。
	setPrivateTestDACL(t, parent, "D:P(D;;0x6;;;"+user.User.Sid.String()+")(A;OICI;FA;;;SY)(A;OICI;FRFX;;;"+user.User.Sid.String()+")")
	if f, err := createPrivateTempFile(parent); !errors.Is(err, os.ErrPermission) {
		if f != nil {
			f.Close()
		}
		t.Fatalf("private file creation did not preserve permission error: %v", err)
	}
	if err := CreatePrivateDirectory(filepath.Join(parent, "denied")); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("private directory creation did not preserve permission error: %v", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed creation left temporary objects: entries=%d err=%v", len(entries), err)
	}
}

func TestPreparePrivateDirectoryDeniedDACL(t *testing.T) {
	path := t.TempDir()
	restorePrivateTestDACL(t, path)
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	// OWNER RIGHTS 会抑制所有者原本隐式拥有的 WRITE_DAC。
	setPrivateTestDACL(t, path, "D:P(A;;RC;;;OW)(A;OICI;FRFX;;;"+user.User.Sid.String()+")(A;OICI;FA;;;SY)")
	before, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if err := PreparePrivateDirectory(path); !errors.Is(err, os.ErrPermission) || !strings.Contains(err.Error(), path) {
		t.Fatalf("DACL repair did not preserve a contextual permission error: %v", err)
	}
	after, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil || before.String() != after.String() {
		t.Fatalf("failed repair changed directory DACL: %v", err)
	}
}

func TestPreparePrivateDirectoryRejectsJunction(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	junction := filepath.Join(parent, "junction")
	for _, path := range []string{target, junction} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	before, err := windows.GetNamedSecurityInfo(target, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	// 挂载点 reparse 记录无需符号链接特权即可创建本地 junction，
	// 普通令牌下的回归测试因此也能覆盖它。
	substitute, err := windows.UTF16FromString(`\??\` + target)
	if err != nil {
		t.Fatal(err)
	}
	printName, err := windows.UTF16FromString(target)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 16+2*(len(substitute)+len(printName)))
	binary.LittleEndian.PutUint32(buffer, windows.IO_REPARSE_TAG_MOUNT_POINT)
	binary.LittleEndian.PutUint16(buffer[4:], uint16(len(buffer)-8))
	binary.LittleEndian.PutUint16(buffer[10:], uint16(2*(len(substitute)-1)))
	binary.LittleEndian.PutUint16(buffer[12:], uint16(2*len(substitute)))
	binary.LittleEndian.PutUint16(buffer[14:], uint16(2*(len(printName)-1)))
	for i, value := range append(substitute, printName...) {
		binary.LittleEndian.PutUint16(buffer[16+2*i:], value)
	}
	h := openPrivateTestHandle(t, junction, windows.GENERIC_WRITE)
	var returned uint32
	if err := windows.DeviceIoControl(h, windows.FSCTL_SET_REPARSE_POINT,
		&buffer[0], uint32(len(buffer)), nil, 0, &returned, nil); err != nil {
		t.Fatalf("create fixture junction: %v", err)
	}
	if err := PreparePrivateDirectory(junction); err == nil {
		t.Fatal("accepted junction as a tool-owned directory")
	}
	after, err := windows.GetNamedSecurityInfo(target, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || before.String() != after.String() {
		t.Fatalf("rejected junction changed target security: %v", err)
	}
}

func TestPreparePrivateDirectoryRequiresListingBeforeMutation(t *testing.T) {
	path := t.TempDir()
	restorePrivateTestDACL(t, path)
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	// 仅有元数据权限无法钉住目录、防止其被替换。
	setPrivateTestDACL(t, path, "D:P(A;;RCWD;;;"+user.User.Sid.String()+")(A;OICI;FA;;;SY)")
	before, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if err := PreparePrivateDirectory(path); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("directory without listing access was not refused: %v", err)
	}
	after, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil || before.String() != after.String() {
		t.Fatalf("failed directory pinning changed DACL: %v", err)
	}
}

func TestWindowsPrivatePathCompatibility(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	longRelative := strings.Repeat("long-component-", 5)
	longRelative = filepath.Join(longRelative, longRelative, longRelative, longRelative, "中文")
	for _, test := range []struct {
		name string
		path string
	}{
		{"relative_unicode", filepath.Join("中文", "相对目录")},
		{"relative_parent_segment", filepath.Join("parent", "child") + `\..\sibling`},
		{"relative_long", longRelative},
		{"absolute_long", filepath.Join(root, "absolute", longRelative)},
		{"extended_prefix", `\\?\` + filepath.Join(root, "extended", longRelative)},
		{"forward_slashes", filepath.ToSlash(filepath.Join(root, "forward", "中文"))},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.MkdirAll(test.path, 0700); err != nil {
				t.Fatal(err)
			}
			f, err := createPrivateTempFile(test.path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			assertCreatedPrivateSecurity(t, windows.Handle(f.Fd()), false)
			dir := filepath.Join(test.path, "private-child")
			if err := CreatePrivateDirectory(dir); err != nil {
				t.Fatal(err)
			}
			if err := PreparePrivateDirectory(dir); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPreparePrivateDirectoryChangesOwnerWhenElevated(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("different-owner fixture requires an already elevated token; the test never elevates")
	}
	path := t.TempDir()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION, administrators, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	setPrivateTestDACL(t, path, "D:P(A;OICI;FA;;;SY)(A;OICI;0x1301bf;;;AU)(A;;WD;;;"+user.User.Sid.String()+")")
	assertLacksWriteOwner(t, path)
	if err := PreparePrivateDirectory(path); err != nil {
		t.Fatal(err)
	}
	assertCreatedPrivateSecurity(t, openPrivateTestHandle(t, path, windows.READ_CONTROL), true)
}

func TestPrivateWindowsPathConversion(t *testing.T) {
	longTail := strings.Repeat(`component\`, 30) + "中文"
	for _, test := range []struct {
		name      string
		path      string
		want      string
		wantError bool
	}{
		{name: "unc", path: `\\server\share\中文`, want: `\\server\share\中文`},
		{name: "long_unc", path: `\\server\share\` + longTail, want: `\\?\UNC\server\share\` + longTail},
		{name: "long_unc_slashes", path: "//server/share/" + filepath.ToSlash(longTail), want: `\\?\UNC\server\share\` + longTail},
		{name: "extended_unc", path: `\\?\UNC\server\share\` + longTail, want: `\\?\UNC\server\share\` + longTail},
		{name: "extended_drive", path: `\\?\C:\folder\..\literal`, want: `\\?\C:\folder\..\literal`},
		{name: "nt_prefix", path: `\??\C:\中文`, want: `\??\C:\中文`},
		{name: "device_prefix", path: `\\.\C:\中文`, want: `\\.\C:\中文`},
		{name: "empty", wantError: true},
		{name: "nul", path: "C:\\folder\x00\\file", wantError: true},
		{name: "extended_nul", path: "\\\\?\\C:\\folder\x00\\file", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			// 转换只做本地字符串处理，不会真正打开网络共享。
			actual, err := privateWindowsPath(test.path)
			if test.wantError {
				if err == nil || actual != nil {
					t.Fatalf("invalid path accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := windows.UTF16PtrToString(actual); got != test.want {
				t.Fatalf("converted path = %q; want %q", got, test.want)
			}
		})
	}
}

func TestPrivateDirectoryHandlePinsObject(t *testing.T) {
	parent := t.TempDir()
	path := filepath.Join(parent, "private")
	renamed := filepath.Join(parent, "renamed")
	if err := CreatePrivateDirectory(path); err != nil {
		t.Fatal(err)
	}
	h, err := openPrivateDirectory(path, windows.READ_CONTROL|windows.WRITE_DAC)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if h != windows.InvalidHandle {
			windows.CloseHandle(h)
		}
	}()
	if err := os.Rename(path, renamed); err == nil {
		t.Fatal("directory could be renamed while its security handle was open")
	}
	if err := os.Remove(path); err == nil {
		t.Fatal("directory could be deleted while its security handle was open")
	}
	if err := windows.CloseHandle(h); err != nil {
		t.Fatal(err)
	}
	h = windows.InvalidHandle
	if err := os.Rename(path, renamed); err != nil {
		t.Fatalf("directory could not be renamed after releasing its handle: %v", err)
	}
}
