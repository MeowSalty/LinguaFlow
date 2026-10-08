//go:build !windows

package credential

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateCreationUnixModes(t *testing.T) {
	parent := t.TempDir()
	f, err := createPrivateTempFile(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 || info.Size() != 0 {
		t.Fatalf("new temporary file: mode=%v size=%d", info.Mode(), info.Size())
	}
	path := filepath.Join(parent, "private")
	if err := CreatePrivateDirectory(path); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatalf("new private directory: %v", info.Mode())
	}
}

func TestPrivateDirectoryUnixExclusiveAndPrepare(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing")
	if err := os.Mkdir(path, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0755); err != nil {
		t.Fatal(err)
	}
	if err := CreatePrivateDirectory(path); !errors.Is(err, os.ErrExist) {
		t.Fatalf("exclusive creation must refuse existing directory: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0755 {
		t.Fatal("exclusive creation changed existing directory mode")
	}
	if err := PreparePrivateDirectory(path); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatal("preparation did not restrict existing directory")
	}
}

func TestPrivateDirectoryUnixRejectsFileAndSymlink(t *testing.T) {
	parent := t.TempDir()
	file := filepath.Join(parent, "file")
	if err := os.WriteFile(file, []byte("preserved"), 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{file, link} {
		if err := CreatePrivateDirectory(path); !errors.Is(err, os.ErrExist) {
			t.Fatalf("exclusive creation must refuse %q: %v", path, err)
		}
		if err := PreparePrivateDirectory(path); err == nil {
			t.Fatalf("preparation accepted non-directory %q", path)
		}
	}
	after, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || before.Mode() != after.Mode() {
		t.Fatal("rejected symlink changed target directory")
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "preserved" {
		t.Fatalf("rejected file changed contents: %v", err)
	}
}
