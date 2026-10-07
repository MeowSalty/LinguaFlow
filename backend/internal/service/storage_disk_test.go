package service

import (
	"context"
	"errors"
	"github.com/MeowSalty/LinguaFlow/backend/internal/diskspace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/localstore"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStorageDiskTemporaryMeteringAndRequiredDirectories(t *testing.T) {
	ctx := context.Background()
	s, err := NewStorageService(testClient(t), nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	available := uint64(1000)
	databaseUnknown := false
	c, _ := diskspace.New(diskspace.Threshold{Bytes: 100}, func(_ context.Context, path string) (diskspace.Observation, error) {
		if path == "database" && databaseUnknown {
			return diskspace.Observation{}, errors.New("database probe failed")
		}
		return diskspace.Observation{FilesystemID: "a", TotalBytes: 1000, AvailableBytes: available}, nil
	})
	s.SetDiskCoordinator(c, "database")
	f, err := s.temporary(5)
	if err != nil {
		t.Fatal(err)
	}
	// Reader-only input forces io.Copy to choose destination.ReadFrom.
	n, err := io.Copy(f, struct{ io.Reader }{strings.NewReader("hello")})
	if err != nil || n != 5 {
		t.Fatalf("%d %v", n, err)
	}
	if _, err = f.Write([]byte("!")); !errors.Is(err, ErrStorageTooLarge) {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	partial, err := s.temporary(900)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = partial.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err = partial.Seal(1); err != nil {
		t.Fatal(err)
	}
	if err = c.Check(ctx, "other", 900); err != nil {
		t.Fatalf("sealed unused reservation retained: %v", err)
	}
	if _, err = partial.Write([]byte("x")); !errors.Is(err, ErrStorageTooLarge) {
		t.Fatalf("write after seal: %v", err)
	}
	partial.Close()
	if err = c.Check(ctx, "other", 900); err != nil {
		t.Fatalf("reservation leaked: %v", err)
	}
	databaseUnknown = true
	if _, err = s.temporary(1); !errors.Is(err, storage.ErrDiskSpaceUnknown) {
		t.Fatal(err)
	}
	databaseUnknown = false
	available = 100
	if _, err = s.temporary(1); !errors.Is(err, storage.ErrDiskSpaceInsufficient) {
		t.Fatal(err)
	}
	available = 1000
	restored, err := s.temporary(1)
	if err != nil {
		t.Fatal(err)
	}
	restored.Close()
}

func TestStorageDiskMissingCoordinatorFailsClosed(t *testing.T) {
	s := &StorageService{}
	if _, err := s.reserveLocal(context.Background(), 1); !errors.Is(err, storage.ErrDiskSpaceUnknown) {
		t.Fatal(err)
	}
}

func TestStorageDiskBeginPrecheckPreservesReplay(t *testing.T) {
	ctx, _, resources, p, u, _ := storageLifecycleFixture(t)
	available := uint64(1000)
	c, _ := diskspace.New(diskspace.Threshold{Bytes: 100}, func(context.Context, string) (diskspace.Observation, error) {
		return diskspace.Observation{FilesystemID: "a", TotalBytes: 1000, AvailableBytes: available}, nil
	})
	resources.storage.SetDiskCoordinator(c)
	in := StorageIntent{Kind: "upload", IdempotencyKey: "disk-replay", Size: 5}
	first, err := resources.storage.Begin(ctx, u.ID, p.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	available = 0
	replay, err := resources.storage.Begin(ctx, u.ID, p.ID, in)
	if err != nil || replay.ID != first.ID {
		t.Fatalf("replay %v %v", replay, err)
	}
	in.IdempotencyKey = "disk-new"
	if _, err = resources.storage.Begin(ctx, u.ID, p.ID, in); !errors.Is(err, storage.ErrDiskSpaceInsufficient) {
		t.Fatal(err)
	}
}

func TestStorageDiskOriginalLocalReadNeedsNoFreeSpace(t *testing.T) {
	ctx, _, resources, p, u, _ := storageLifecycleFixture(t)
	driver, err := localstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { driver.Close() })
	resources.storage.RegisterDriver(resources.storage.defaultSpaceID, driver)
	r := storageUpload(t, ctx, resources, p, u, "read.txt", "hello\n")
	c, _ := diskspace.New(diskspace.Threshold{Bytes: 100}, func(context.Context, string) (diskspace.Observation, error) {
		return diskspace.Observation{}, errors.New("unknown")
	})
	resources.storage.SetDiskCoordinator(c)
	f, err := resources.OriginalFile(ctx, u.ID, p.ID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(f)
	if err != nil || string(content) != "hello\n" {
		t.Fatalf("%q %v", content, err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	for _, held := range resources.storage.readers {
		if held != 0 {
			t.Fatal("reader pin leaked")
		}
	}
}

func TestStorageDiskChecksBothInputAndLocalObjectBeforeReceiving(t *testing.T) {
	ctx, client, resources, p, u, _ := storageLifecycleFixture(t)
	available := uint64(112)
	c, _ := diskspace.New(diskspace.Threshold{Bytes: 100}, func(context.Context, string) (diskspace.Observation, error) {
		return diskspace.Observation{FilesystemID: "shared", TotalBytes: 1000, AvailableBytes: available}, nil
	})
	driver, err := localstore.NewWithCoordinator(t.TempDir(), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { driver.Close() })
	resources.storage.SetDiskCoordinator(c)
	resources.storage.RegisterDriver(resources.storage.defaultSpaceID, driver)
	in := StorageIntent{Kind: "upload", IdempotencyKey: "peak", Size: 6}
	task, err := resources.storage.Begin(ctx, u.ID, p.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	available = 111 // one six-byte copy fits, but the two-copy peak does not.
	in.IdempotencyKey = "peak-too-large"
	if _, err = resources.storage.Begin(ctx, u.ID, p.ID, in); !errors.Is(err, storage.ErrDiskSpaceInsufficient) {
		t.Fatal(err)
	}
	input := strings.NewReader("hello\n")
	if _, err = resources.storage.Stage(ctx, task, input, 6); !errors.Is(err, storage.ErrDiskSpaceInsufficient) {
		t.Fatal(err)
	}
	if input.Len() != 6 {
		t.Fatal("received content before validating the peak")
	}
	if n := client.StorageWrite.Query().CountX(ctx); n != 0 {
		t.Fatalf("created write before peak check: %d", n)
	}
	available = 112
	stage, err := resources.storage.Stage(ctx, task, input, 6)
	if err != nil {
		t.Fatal(err)
	}
	if err = stage.Close(); err != nil {
		t.Fatal(err)
	}
	if err = c.Check(ctx, driver.PhysicalWriteDirectory(), 12); err != nil {
		t.Fatalf("physical reservation leaked: %v", err)
	}
}

func TestStorageDiskStartupRetainsUnknownAndReleasesExactInput(t *testing.T) {
	s, err := NewStorageService(testClient(t), nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"read-orphan": "orphan", "attempt.input": "input", "original.txt": "original"} {
		if err = os.WriteFile(filepath.Join(s.workDir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.ReconcileLocalTemporary(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.tempBytes != 19 {
		t.Fatalf("retained bytes %d", s.tempBytes)
	}
	// Caller supplies a claimed durable attempt; unrelated filenames survive.
	if err = s.cleanupInput("attempt"); err != nil {
		t.Fatal(err)
	}
	if s.tempBytes != 14 {
		t.Fatalf("remaining bytes %d", s.tempBytes)
	}
	if err = s.cleanupInput("attempt"); err != nil {
		t.Fatal(err)
	}
	if s.tempBytes != 14 {
		t.Fatal("double refund")
	}
	if _, err = os.Stat(filepath.Join(s.workDir, "original.txt")); err != nil {
		t.Fatal(err)
	}
	if err = s.cleanupInput("../original"); !errors.Is(err, storage.ErrInvalidKey) {
		t.Fatal(err)
	}
}
