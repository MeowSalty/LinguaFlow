package localstore

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/drivertest"
)

func TestDriverContract(t *testing.T) {
	drivertest.Run(t, func(t *testing.T) storage.Driver {
		store, err := New(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { store.Close() })
		return store
	})
}

func TestInterruptedPublicationReconciliation(t *testing.T) {
	directory := t.TempDir()
	store, err := New(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutNew(context.Background(), "a/object", strings.NewReader("partial"), 50); err == nil {
		t.Fatal("short write accepted")
	}
	store.Close()
	store, err = New(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	object := storage.Object{Key: "a/object"}
	if _, err := store.Stat(context.Background(), object); !errors.Is(err, storage.ErrUnavailable) {
		t.Fatalf("unpublished stage must be reconciled: %v", err)
	}
	if _, err := store.PutNew(context.Background(), object.Key, strings.NewReader("new"), 3); !errors.Is(err, storage.ErrUnavailable) {
		t.Fatalf("must not overwrite previous attempt: %v", err)
	}
	if err := store.Delete(context.Background(), object); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Stat(context.Background(), object); !errors.Is(err, storage.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, filepath.FromSlash(stageKey(object.Key)))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stage remains: %v", err)
	}
}

func TestCrashAfterLinkBeforeStageRemoval(t *testing.T) {
	directory := t.TempDir()
	store, err := New(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := "committed"
	if err := store.root.WriteFile(stageKey(key), []byte("final bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.root.Link(stageKey(key), key); err != nil {
		t.Fatal(err)
	}
	reader, err := store.Open(context.Background(), storage.Object{Key: key})
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || string(data) != "final bytes" {
		t.Fatalf("committed bytes: %q %v", data, err)
	}
	if _, err := store.PutNew(context.Background(), key, strings.NewReader("other"), 5); !errors.Is(err, storage.ErrExists) {
		t.Fatal(err)
	}
}

func TestSymlinkCannotEscapeRoot(t *testing.T) {
	directory := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "existing"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(directory, "link")); err != nil {
		t.Skipf("symlink creation not permitted: %v", err)
	}
	store, err := New(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.PutNew(context.Background(), "link/new", strings.NewReader("bad"), 3); !errors.Is(err, storage.ErrInvalidKey) {
		t.Fatalf("put traversal: %v", err)
	}
	if _, err := store.Open(context.Background(), storage.Object{Key: "link/existing"}); !errors.Is(err, storage.ErrInvalidKey) {
		t.Fatalf("open traversal: %v", err)
	}
	if err := store.Delete(context.Background(), storage.Object{Key: "link/existing"}); !errors.Is(err, storage.ErrInvalidKey) {
		t.Fatalf("delete traversal: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(outside, "existing")); err != nil || string(data) != "keep" {
		t.Fatalf("outside changed: %q %v", data, err)
	}
}

func TestConcurrentCreateHasOneWinner(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var wg sync.WaitGroup
	results := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Go(func() {
			_, err := store.PutNew(context.Background(), "one-key", strings.NewReader("data"), 4)
			results <- err
		})
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, storage.ErrExists) && !errors.Is(err, storage.ErrUnavailable) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("winners = %d", winners)
	}
}

func TestOpenPreservesRandomAccessAndCancellation(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	object, err := store.PutNew(context.Background(), "archive", strings.NewReader("abcdef"), 6)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	file, err := store.Open(ctx, object)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, ok := file.(io.ReaderAt)
	if !ok {
		t.Fatal("local object lost ReaderAt")
	}
	if _, ok := file.(io.Seeker); !ok {
		t.Fatal("local object lost Seeker")
	}
	var result [2]byte
	if _, err := reader.ReadAt(result[:], 2); err != nil || string(result[:]) != "cd" {
		t.Fatalf("random access: %q %v", result, err)
	}
	cancel()
	if _, err := reader.ReadAt(result[:], 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("read cancelled: %v", err)
	}
	if _, err := io.Copy(io.Discard, file); !errors.Is(err, context.Canceled) {
		t.Fatalf("copy bypassed cancellation: %v", err)
	}
}
