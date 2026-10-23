package storagemigrate

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
)

func TestMaintenanceGenerationsSurviveLostCheckpoints(t *testing.T) {
	for _, stop := range []int{1, 2} {
		t.Run(string(rune('0'+stop)), func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			manifest, err := f.migration.Inventory(ctx)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "manifest.json")
			if err := f.migration.Apply(ctx, manifest, func() error { return SaveManifest(path, manifest, false) }); err != nil {
				t.Fatal(err)
			}
			if got := f.client.Project.GetX(ctx, f.project.ID).StorageGeneration; got != 2 {
				t.Fatalf("apply generation: %d", got)
			}
			crash := errors.New("lost rollback checkpoint")
			calls := 0
			err = f.migration.Rollback(ctx, manifest, func() error {
				calls++
				if calls == stop {
					return crash
				}
				return SaveManifest(path, manifest, false)
			})
			if !errors.Is(err, crash) {
				t.Fatalf("rollback interruption: %v", err)
			}
			manifest, err = ReadManifest(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.migration.Rollback(ctx, manifest, func() error { return SaveManifest(path, manifest, false) }); err != nil {
				t.Fatalf("resume rollback: %v", err)
			}
			project := f.client.Project.GetX(ctx, f.project.ID)
			resource := f.client.Resource.GetX(ctx, f.resource.ID)
			if project.StorageGeneration != 4 || project.StorageState != "active" || resource.SourceGeneration != 2 || resource.CurrentSourceRevisionID != nil {
				t.Fatalf("resumed rollback changed generations: project=%+v resource=%+v", project, resource)
			}
		})
	}
}

func TestMaintenanceFinishReplayDoesNotAdvanceGeneration(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	manifest, err := f.migration.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "manifest.json")
	crash := errors.New("lost finish checkpoint")
	calls := 0
	err = f.migration.Apply(ctx, manifest, func() error {
		calls++
		if calls == 3 {
			return crash
		}
		return SaveManifest(path, manifest, false)
	})
	if !errors.Is(err, crash) {
		t.Fatalf("finish interruption: %v", err)
	}
	manifest, err = ReadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.migration.Apply(ctx, manifest, func() error { return SaveManifest(path, manifest, false) }); err != nil {
		t.Fatal(err)
	}
	if got := f.client.Project.GetX(ctx, f.project.ID).StorageGeneration; got != 2 {
		t.Fatalf("finish replay advanced generation: %d", got)
	}
}

func TestRollbackPreviousGenerationProtocolRemainsMonotonic(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	manifest, err := f.migration.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.migration.Apply(ctx, manifest, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	// Simulate a completed operation from the previous binary. Its original
	// manifest and task identity remain valid; only its protocol was implicit.
	op := f.client.StorageTask.Query().Where(storagetask.OperationIDEQ(manifest.OperationID)).OnlyX(ctx)
	delete(op.Input, "generation_protocol")
	f.client.StorageTask.UpdateOneID(op.ID).SetInput(op.Input).ExecX(ctx)
	f.client.Project.UpdateOneID(f.project.ID).SetStorageGeneration(1).ExecX(ctx)
	if err := f.migration.Rollback(ctx, manifest, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got := f.client.Project.GetX(ctx, f.project.ID).StorageGeneration; got != 3 {
		t.Fatalf("legacy rollback generation: %d", got)
	}
}
