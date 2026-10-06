//go:build windows

package v013

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"golang.org/x/sys/windows"
)

func TestLocalMigrationUnderModifyOnlyParent(t *testing.T) {
	if windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("the Modify-only migration regression requires a non-elevated token")
	}
	// localFixture creates a synthetic database below t.TempDir. Only this
	// temporary parent is changed; no real instance or workspace ACL is touched.
	dir := localFixture(t, true)
	parent := filepath.Dir(dir)
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;0x1301bf;;;AU)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	err = windows.SetNamedSecurityInfo(parent, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil)
	runtime.KeepAlive(sd)
	if err != nil {
		t.Fatal(err)
	}
	parentSecurity, err := windows.GetNamedSecurityInfo(parent, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	reference := filepath.Join(parent, "ordinary-file")
	if err := os.WriteFile(reference, nil, 0600); err != nil {
		t.Fatal(err)
	}
	p, err := windows.UTF16PtrFromString(reference)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(p, windows.WRITE_OWNER,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err == nil {
		windows.CloseHandle(h)
		t.Fatal("invalid migration fixture: ordinary file was granted WRITE_OWNER")
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("WRITE_OWNER probe failed for another reason: %v", err)
	}

	original := readLocalBytes(t, filepath.Join(dir, localDatabaseName))
	rehearsal, err := RunLocal(context.Background(), LocalOptions{DataDir: dir})
	if err != nil {
		t.Fatalf("rehearse under Modify-only parent: %v", err)
	}
	if rehearsal.Applied || rehearsal.CredentialsCreated != 2 {
		t.Fatalf("unexpected rehearsal result: %+v", rehearsal)
	}
	if !bytes.Equal(original, readLocalBytes(t, filepath.Join(dir, localDatabaseName))) {
		t.Fatal("rehearsal changed the original database")
	}
	if _, err := os.Stat(filepath.Join(dir, "credentials-keyring.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rehearsal published a keyring: %v", err)
	}
	stages, err := filepath.Glob(filepath.Join(parent, ".LinguaFlow.v013-stage-*"))
	if err != nil || len(stages) != 0 {
		t.Fatalf("rehearsal left staging directories: %v (%v)", stages, err)
	}

	result, err := RunLocal(context.Background(), LocalOptions{DataDir: dir, Apply: true})
	if err != nil {
		t.Fatalf("apply under Modify-only parent: %v", err)
	}
	if !result.Applied || result.BackupDir == "" || result.CredentialsCreated != 2 {
		t.Fatalf("unexpected apply result: %+v", result)
	}
	if !bytes.Equal(original, readLocalBytes(t, filepath.Join(result.BackupDir, localDatabaseName))) {
		t.Fatal("backup did not preserve the original database")
	}
	keys, err := credential.LoadKeyring(filepath.Join(dir, "credentials-keyring.json"))
	if err != nil || keys.ActiveKeyID() == "" {
		t.Fatalf("published private keyring is not usable: %v", err)
	}
	after, err := windows.GetNamedSecurityInfo(parent, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || parentSecurity.String() != after.String() {
		t.Fatalf("migration changed the existing parent security: %v", err)
	}
}
