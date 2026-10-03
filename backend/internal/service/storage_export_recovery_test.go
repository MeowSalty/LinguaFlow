package service

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/blob"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagewrite"
)

func TestStorageExportRecoveryPreparedSnapshotAndOutput(t *testing.T) {
	ctx, client, s, p, u, driver := storageLifecycleFixture(t)
	r := storageUpload(t, ctx, s, p, u, "source.txt", "source\n")
	task, err := s.storage.Begin(ctx, u.ID, p.ID, StorageIntent{Kind: "export", IdempotencyKey: "snapshot-crash", ResourceID: r.ID, SourceRevisionID: *r.CurrentSourceRevisionID, SourceGeneration: r.SourceGeneration, TranslationGeneration: r.TranslationGeneration})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.captureSnapshot(ctx, u.ID, p.ID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := s.storage.Stage(ctx, task, bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if err = staged.Close(); err != nil {
		t.Fatal(err)
	}
	// 模拟在校验之后、元数据事务之前退出进程。
	task, err = s.prepareExportArtifact(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	const savedOutput = "persisted-rendered-output\n"
	staged, err = s.storage.Stage(ctx, task, bytes.NewBufferString(savedOutput), int64(len(savedOutput)))
	if err != nil {
		t.Fatal(err)
	}
	if err = staged.Close(); err != nil {
		t.Fatal(err)
	}
	before := driver.puts
	if err = s.storage.ProcessTasks(ctx); err != nil {
		t.Fatal(err)
	}
	if driver.puts != before {
		t.Fatal("recovery uploaded the output again")
	}
	task = client.StorageTask.GetX(ctx, task.ID)
	if task.Status != storagetask.StatusCompleted {
		t.Fatalf("%s: %s", task.Status, task.ErrorCode)
	}
	if n := client.StorageWrite.Query().Where(storagewrite.TaskIDEQ(task.ID)).CountX(ctx); n != 2 {
		t.Fatalf("writes=%d", n)
	}
	f, _, err := s.DownloadExport(ctx, u.ID, p.ID, *task.ResultArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil || string(got) != savedOutput {
		t.Fatalf("output=%q err=%v", got, err)
	}
}

func TestStorageExportRecoverySharedSnapshotSurvivesOriginalDeletion(t *testing.T) {
	ctx, client, s, p, u, _ := storageLifecycleFixture(t)
	r := storageUpload(t, ctx, s, p, u, "source.txt", "source\n")
	first, err := s.CreateExport(ctx, u.ID, p.ID, r.ID, "first")
	if err != nil {
		t.Fatal(err)
	}
	old := client.ExportArtifact.GetX(ctx, *first.ResultArtifactID)
	second, err := s.RebuildExport(ctx, u.ID, p.ID, old.ID, "rebuild")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteExport(ctx, u.ID, p.ID, old.ID); err != nil {
		t.Fatal(err)
	}
	if b := client.Blob.GetX(ctx, *old.SnapshotBlobID); b.Status != blob.StatusReady || b.ActiveLocationID == nil {
		t.Fatal("shared snapshot retired")
	}
	if err = s.storage.ProcessTasks(ctx); err != nil {
		t.Fatal(err)
	}
	second = client.StorageTask.GetX(ctx, second.ID)
	if second.Status != storagetask.StatusCompleted {
		t.Fatalf("%s: %s", second.Status, second.ErrorCode)
	}
	if err = s.DeleteExport(ctx, u.ID, p.ID, *second.ResultArtifactID); err != nil {
		t.Fatal(err)
	}
	if b := client.Blob.GetX(ctx, *old.SnapshotBlobID); b.Status != blob.StatusDeletePending {
		t.Fatal("unreferenced snapshot leaked")
	}
}

func TestStorageExportRecoveryWorkerFindsPreparedSnapshot(t *testing.T) {
	ctx, client, s, p, u, _ := storageLifecycleFixture(t)
	r := storageUpload(t, ctx, s, p, u, "source.txt", "source\n")
	task, err := s.storage.Begin(ctx, u.ID, p.ID, StorageIntent{Kind: "export", IdempotencyKey: "snapshot-worker", ResourceID: r.ID, SourceRevisionID: *r.CurrentSourceRevisionID, SourceGeneration: r.SourceGeneration, TranslationGeneration: r.TranslationGeneration})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.captureSnapshot(ctx, u.ID, p.ID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := s.storage.Stage(ctx, task, bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if err = staged.Close(); err != nil {
		t.Fatal(err)
	}
	if err = s.storage.ProcessTasks(ctx); err != nil {
		t.Fatal(err)
	}
	task = client.StorageTask.GetX(ctx, task.ID)
	if task.Status != storagetask.StatusCompleted {
		t.Fatalf("%s: %s", task.Status, task.ErrorCode)
	}
}
