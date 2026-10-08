//go:build windows

package credential

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestPublishedPrivateFileHasProtectedWindowsACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	if _, err := PrepareKeyring(path, true); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("private key file inherits directory ACL")
	}
	owner, _, err := sd.Owner()
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if owner == nil || !owner.Equals(user.User.Sid) {
		t.Fatal("private key file is not owned by the current account")
	}
	sddl := sd.String()
	for _, broadPrincipal := range []string{";;;WD)", ";;;BU)", ";;;AU)"} {
		if strings.Contains(sddl, broadPrincipal) {
			t.Fatalf("private key grants broad access: %s", sddl)
		}
	}
}

func TestLoadKeyringChecksActualWindowsACLWithoutRepair(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	account := user.User.Sid.String()
	private := "(A;;FA;;;SY)(A;;FA;;;" + account + ")"
	for _, test := range []struct {
		name    string
		sddl    string
		allowed bool
	}{
		{name: "private", sddl: "D:P" + private, allowed: true},
		{name: "read_only", sddl: "D:P(A;;FA;;;SY)(A;;FR;;;" + account + ")", allowed: true},
		{name: "everyone_read", sddl: "D:P" + private + "(A;;FR;;;WD)"},
		{name: "authenticated_users_write", sddl: "D:P" + private + "(A;;FW;;;AU)"},
		{name: "users_metadata", sddl: "D:P" + private + "(A;;RC;;;BU)"},
		{name: "another_account", sddl: "D:P" + private + "(A;;FR;;;S-1-5-21-1111-2222-3333-4444)"},
		{name: "inherited", sddl: "D:" + private},
		{name: "null_dacl", sddl: "D:NO_ACCESS_CONTROL"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "keyring.json")
			if _, err := PrepareKeyring(path, true); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			sd, err := windows.SecurityDescriptorFromString(test.sddl)
			if err != nil {
				t.Fatal(err)
			}
			dacl, _, err := sd.DACL()
			if err != nil {
				t.Fatal(err)
			}
			flags := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION)
			if test.name == "inherited" {
				flags = windows.DACL_SECURITY_INFORMATION | windows.UNPROTECTED_DACL_SECURITY_INFORMATION
			}
			if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, flags, nil, nil, dacl, nil); err != nil {
				t.Fatal(err)
			}
			securityBefore, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION)
			if err != nil {
				t.Fatal(err)
			}
			for _, create := range []bool{false, true} {
				_, err := PrepareKeyring(path, create)
				if test.allowed {
					if err != nil {
						t.Fatal(err)
					}
				} else if !errors.Is(err, os.ErrPermission) || !strings.Contains(err.Error(), "Windows ACL") {
					t.Fatalf("insecure ACL accepted or unexplained: %v", err)
				}
				if err != nil && strings.Contains(err.Error(), string(before)) {
					t.Fatal("permission error exposed keyring contents")
				}
			}
			securityAfter, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.OWNER_SECURITY_INFORMATION)
			if err != nil {
				t.Fatal(err)
			}
			if securityBefore.String() != securityAfter.String() {
				t.Fatal("permission check changed Windows ACL")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("permission check changed keyring contents")
			}
		})
	}
}
