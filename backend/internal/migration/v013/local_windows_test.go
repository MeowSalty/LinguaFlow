//go:build windows

package v013

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestLocalWindowsOpenFilePreventsDirectorySwitch(t *testing.T) {
	journal, path := preparedLocalSwitch(t)
	name, err := windows.UTF16PtrFromString(filepath.Join(journal.SourceDir, "content"))
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	if err := switchLocalDirectories(journal, renameAbsent); err == nil {
		t.Fatal("directory containing a file without delete sharing was renamed")
	}
	result, _, err := recoverLocal(journal.SourceDir, path, true)
	if err != nil || result.Recovery != "restored" {
		t.Fatalf("recovery=%+v err=%v", result, err)
	}
	if string(readLocalBytes(t, filepath.Join(journal.SourceDir, "content"))) != "old" {
		t.Fatal("locked original changed")
	}
}

func TestLocalWindowsRejectsSymlink(t *testing.T) {
	dir := localFixture(t, false)
	target := filepath.Join(t.TempDir(), "external.txt")
	if err := os.WriteFile(target, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "external-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink privilege unavailable: %v", err)
	}
	if _, err := RunLocal(t.Context(), LocalOptions{DataDir: dir, Apply: true}); err == nil {
		t.Fatal("followed external link")
	}
	if string(readLocalBytes(t, target)) != "outside" {
		t.Fatal("external target changed")
	}
}

func TestSQLiteWindowsRejectsJunctionSidecarsBeforeOpeningSource(t *testing.T) {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		t.Run(suffix, func(t *testing.T) {
			dir := localFixture(t, false)
			external := t.TempDir()
			marker := filepath.Join(external, "preserve.txt")
			if err := os.WriteFile(marker, []byte("preserve external directory"), 0600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(dir, localDatabaseName+suffix)
			// Directory junction creation does not require the symlink privilege,
			// so Windows CI can exercise reparse protection without elevation.
			if output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, external).CombinedOutput(); err != nil {
				t.Fatalf("create test junction: %v %s", err, output)
			}
			target := filepath.Join(t.TempDir(), "copy.db")
			if err := snapshotSQLite(t.Context(), filepath.Join(dir, localDatabaseName), target); err == nil {
				t.Fatalf("junction not refused by preflight: %v", err)
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatal("SQLite was opened before sidecar validation")
			}
			if string(readLocalBytes(t, marker)) != "preserve external directory" {
				t.Fatal("external junction target changed")
			}
		})
	}
}
