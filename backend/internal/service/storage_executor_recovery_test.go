package service

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
)

func TestStorageExecutorReadyWorkIsNotStarvedByIdleIntents(t *testing.T) {
	ctx, client, s, p, u, _ := storageLifecycleFixture(t)
	for i := 0; i < 105; i++ {
		if _, err := s.storage.Begin(ctx, u.ID, p.ID, StorageIntent{Kind: "upload", IdempotencyKey: fmt.Sprintf("idle-%d", i), Path: fmt.Sprintf("idle-%d.txt", i), Size: 1}); err != nil {
			t.Fatal(err)
		}
	}
	task, err := s.storage.Begin(ctx, u.ID, p.ID, StorageIntent{Kind: "upload", IdempotencyKey: "ready", Path: "ready.txt", Size: 5})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.storage.Receive(ctx, u.ID, p.ID, task.ID, bytes.NewBufferString("ready"), 5); err != nil {
		t.Fatal(err)
	}
	if err = s.storage.ProcessTasks(ctx); err != nil {
		t.Fatal(err)
	}
	if task = client.StorageTask.GetX(ctx, task.ID); task.Status != storagetask.StatusCompleted {
		t.Fatalf("starved: %s %s", task.Status, task.ErrorCode)
	}
}

func TestStorageExecutorRetryWindowPersistsAndExplicitRetryRenewsIt(t *testing.T) {
	ctx, client, s, p, u, _ := storageLifecycleFixture(t)
	task, err := s.storage.Begin(ctx, u.ID, p.ID, StorageIntent{Kind: "upload", IdempotencyKey: "retry-window", Path: "ready.txt", Size: 5})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.storage.Receive(ctx, u.ID, p.ID, task.ID, bytes.NewBufferString("ready"), 5); err != nil {
		t.Fatal(err)
	}
	client.StorageTask.UpdateOneID(task.ID).SetRetryStartedAt(time.Now().Add(-s.storage.cfg.RetryWindow - time.Second)).ExecX(ctx)
	if err = s.storage.ProcessTasks(ctx); err != nil {
		t.Fatal(err)
	}
	if task = client.StorageTask.GetX(ctx, task.ID); task.Status != storagetask.StatusNeedsAction || task.ErrorCode != "storage_retry_exhausted" {
		t.Fatalf("unbounded retry: %s %s", task.Status, task.ErrorCode)
	}
	if _, err = s.storage.Retry(ctx, u.ID, p.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.storage.ProcessTasks(ctx); err != nil {
		t.Fatal(err)
	}
	if task = client.StorageTask.GetX(ctx, task.ID); task.Status != storagetask.StatusCompleted {
		t.Fatalf("retry failed: %s %s", task.Status, task.ErrorCode)
	}
}

func TestStorageExecutorDoesNotOverwriteCancellationWithFailure(t *testing.T) {
	ctx, client, s, p, u, driver := storageLifecycleFixture(t)
	r := storageUpload(t, ctx, s, p, u, "source.txt", "source\n")
	task, err := s.CreateExport(ctx, u.ID, p.ID, r.ID, "cancel-render")
	if err != nil {
		t.Fatal(err)
	}
	driver.beforePut = func(string) {
		if _, err := s.storage.Cancel(ctx, u.ID, p.ID, task.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.storage.ProcessTasks(ctx); err != nil {
		t.Fatal(err)
	}
	task = client.StorageTask.GetX(ctx, task.ID)
	if task.Status != storagetask.StatusCancelled {
		t.Fatalf("cancel overwritten: %s %s", task.Status, task.ErrorCode)
	}
}
