package localstore

import (
	"context"
	"errors"
	"github.com/MeowSalty/LinguaFlow/backend/internal/diskspace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"os"
	"strings"
	"testing"
)

func TestDiskProtectionPreservesReadAndDelete(t *testing.T) {
	available := uint64(1000)
	unknown := false
	c, _ := diskspace.New(diskspace.Threshold{Bytes: 100}, func(context.Context, string) (diskspace.Observation, error) {
		if unknown {
			return diskspace.Observation{}, errors.New("private directory")
		}
		return diskspace.Observation{FilesystemID: "a", TotalBytes: 1000, AvailableBytes: available}, nil
	})
	s, err := NewWithCoordinator(t.TempDir(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	object, err := s.PutNew(context.Background(), "objects/first", strings.NewReader("abc"), 3)
	if err != nil {
		t.Fatal(err)
	}
	available = 102
	if _, err = s.PutNew(context.Background(), "objects/second", strings.NewReader("abc"), 3); !errors.Is(err, storage.ErrDiskSpaceInsufficient) {
		t.Fatal(err)
	}
	if _, err = s.root.Stat(stageKey("objects/second")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("admission created a stage: %v", err)
	}
	unknown = true
	if _, err = s.PutNew(context.Background(), "objects/second", strings.NewReader("abc"), 3); !errors.Is(err, storage.ErrDiskSpaceUnknown) {
		t.Fatal(err)
	}
	r, err := s.Open(context.Background(), object)
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	if err = s.Delete(context.Background(), object); err != nil {
		t.Fatal(err)
	}
}
func TestDiskWriteRecheckKeepsExactStage(t *testing.T) {
	calls := 0
	c, _ := diskspace.New(diskspace.Threshold{Bytes: 100}, func(context.Context, string) (diskspace.Observation, error) {
		calls++
		available := uint64(1000)
		if calls > 1 {
			available = 100
		}
		return diskspace.Observation{FilesystemID: "a", TotalBytes: 1000, AvailableBytes: available}, nil
	})
	s, err := NewWithCoordinator(t.TempDir(), c)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	key := "objects/first"
	if _, err = s.PutNew(context.Background(), key, strings.NewReader("abc"), 3); !errors.Is(err, storage.ErrDiskSpaceInsufficient) {
		t.Fatal(err)
	}
	if _, err = s.root.Stat(stageKey(key)); err != nil {
		t.Fatalf("lost registered stage: %v", err)
	}
	if err = s.Delete(context.Background(), storage.Object{Key: key}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.root.Stat(stageKey(key)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}
