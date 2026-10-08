package service

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/diskspace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagewrite"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

// SetDiskCoordinator is startup-only. All local drivers and this service must
// share the same coordinator; required directories include SQLite and enabled
// caches. Remote object capacity is not inferred from these observations.
func (s *StorageService) SetDiskCoordinator(c *diskspace.Coordinator, requiredDirectories ...string) {
	s.disk = c
	s.diskDirectories = append([]string(nil), requiredDirectories...)
}

func (s *StorageService) SetDiskDiagnosticDirectories(paths ...string) {
	s.diskDiagnosticDirectories = append([]string(nil), paths...)
}
func (s *StorageService) reserveLocal(ctx context.Context, size int64) (*diskspace.Reservation, error) {
	if s.disk == nil {
		return nil, storage.ErrDiskSpaceUnknown
	}
	for _, directory := range s.diskDirectories {
		if err := s.disk.Check(ctx, directory, 0); err != nil {
			return nil, err
		}
	}
	return s.disk.Reserve(ctx, s.workDir, size)
}

func (s *StorageService) checkMaterialization(ctx context.Context, target int, size int64) error {
	if s.disk == nil {
		return storage.ErrDiskSpaceUnknown
	}
	if size < 0 {
		return storage.ErrPayloadTooLarge
	}
	plan := make(map[string]int64)
	for _, directory := range s.diskDirectories {
		plan[directory] = 0
	}
	plan[s.workDir] = size
	// Only inspect already registered drivers. Resolving here could perform
	// remote I/O while the caller holds a database transaction.
	s.mu.Lock()
	driver := s.drivers[target]
	s.mu.Unlock()
	if local, ok := driver.(interface{ PhysicalWriteDirectory() string }); ok {
		directory := local.PhysicalWriteDirectory()
		if directory == "" {
			return storage.ErrDiskSpaceUnknown
		}
		if size > math.MaxInt64-plan[directory] {
			return storage.ErrPayloadTooLarge
		}
		plan[directory] += size
	}
	return s.disk.CheckPlan(ctx, plan)
}

// ReconcileLocalTemporary runs before workers or requests start. Only input
// copies belonging to completed cleanup or committed writes are discarded.
// Unknown files and unfinished input copies stay intact and count against the
// processing budget; their physical size is already included in disk probes.
func (s *StorageService) ReconcileLocalTemporary(ctx context.Context) error {
	entries, err := os.ReadDir(s.workDir)
	if err != nil {
		return diskspace.Classify(err)
	}
	var remaining int64
	retained := make(map[string]int64)
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			return err
		}
		info, e := entry.Info()
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".input") {
			attempt := strings.TrimSuffix(name, ".input")
			done, e := s.client.StorageWrite.Query().Where(storagewrite.AttemptIDEQ(attempt), storagewrite.PhaseIn("committed", "cleaned")).Exist(ctx)
			if e != nil {
				return e
			}
			if done {
				e = os.Remove(filepath.Join(s.workDir, name))
				if e != nil && !errors.Is(e, os.ErrNotExist) {
					return diskspace.Classify(e)
				}
				continue
			}
		}
		if info.Size() > math.MaxInt64-remaining {
			return storage.ErrPayloadTooLarge
		}
		remaining += info.Size()
		retained[name] = info.Size()
	}
	s.mu.Lock()
	s.tempBytes = remaining
	s.startupTemporary = retained
	s.mu.Unlock()
	return nil
}

// cleanupInput only removes the input named by a durable attempt after the
// attempt has been exclusively claimed for cleanup. It never scans siblings.
func (s *StorageService) cleanupInput(attempt string) error {
	if attempt == "" || filepath.Base(attempt) != attempt || strings.ContainsAny(attempt, `/\\`) {
		return storage.ErrInvalidKey
	}
	name := attempt + ".input"
	if err := os.Remove(filepath.Join(s.workDir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return diskspace.Classify(err)
	}
	s.mu.Lock()
	s.tempBytes -= s.startupTemporary[name]
	delete(s.startupTemporary, name)
	s.mu.Unlock()
	return nil
}
