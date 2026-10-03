package service

import (
	"errors"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

func TestStorageOfflineTasksCannotBeMutatedThroughInteractiveActions(t *testing.T) {
	ctx, client, resources, project, owner, _ := storageLifecycleFixture(t)
	for _, kind := range []string{"legacy_migration", "legacy_cleanup", "future_task", "storage_probe", "storage_marker"} {
		task := client.StorageTask.Create().SetProjectID(project.ID).SetKind(kind).SetOperationID(kind).SetIdempotencyKey(kind).SetRequestHash(kind).SetStatus(storagetask.StatusNeedsAction).SetPhase("applying").SaveX(ctx)
		if actions := resources.storage.TaskActions(ctx, owner.ID, task); len(actions) != 0 {
			t.Fatalf("offline %s advertised interactive actions: %v", kind, actions)
		}
		if _, err := resources.storage.Retry(ctx, owner.ID, project.ID, task.ID); !errors.Is(err, storage.ErrUnsupported) {
			t.Fatalf("offline %s retry: %v", kind, err)
		}
		if _, err := resources.storage.Cancel(ctx, owner.ID, project.ID, task.ID); !errors.Is(err, storage.ErrUnsupported) {
			t.Fatalf("offline %s cancel: %v", kind, err)
		}
		current := client.StorageTask.GetX(ctx, task.ID)
		if current.Status != task.Status || current.Phase != task.Phase {
			t.Fatalf("interactive action mutated offline %s", kind)
		}
	}
}
