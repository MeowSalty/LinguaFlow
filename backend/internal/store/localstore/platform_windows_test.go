//go:build windows

package localstore

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

func TestWindowsJunctionCannotEscapeRoot(t *testing.T) {
	directory := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(directory, "junction")
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	// 目录联接（junction）不需要 symlink 特权，在普通开发者账户上
	// 也能触及 Windows 的 reparse 边界。
	if output, err := exec.Command("cmd", "/d", "/c", "mklink", "/J", link, outside).CombinedOutput(); err != nil {
		t.Fatalf("create test junction: %v: %s", err, output)
	}
	store, err := New(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.PutNew(context.Background(), "junction/new", strings.NewReader("bad"), 3); !errors.Is(err, storage.ErrInvalidKey) {
		t.Fatalf("write through junction: %v", err)
	}
	if _, err := store.Open(context.Background(), storage.Object{Key: "junction/keep"}); !errors.Is(err, storage.ErrInvalidKey) {
		t.Fatalf("read through junction: %v", err)
	}
	if err := store.Delete(context.Background(), storage.Object{Key: "junction/keep"}); !errors.Is(err, storage.ErrInvalidKey) {
		t.Fatalf("delete through junction: %v", err)
	}
	if nested, err := New(link); err == nil {
		nested.Close()
		t.Fatal("junction accepted as root")
	}
	if data, err := os.ReadFile(filepath.Join(outside, "keep")); err != nil || string(data) != "outside" {
		t.Fatalf("outside bytes changed: %q %v", data, err)
	}
	capabilities, err := store.Capabilities(context.Background())
	if err != nil || capabilities.DirectorySync {
		t.Fatalf("Windows must report missing directory sync guarantee: %+v %v", capabilities, err)
	}
}
