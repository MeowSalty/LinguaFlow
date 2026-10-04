package service

import (
	"errors"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/deletionentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
	"testing"
	"time"
)

func TestStorageExportReplayKeepsFrozenIdentity(t *testing.T) {
	ctx, client, s, p, u, _ := storageLifecycleFixture(t)
	r := storageUpload(t, ctx, s, p, u, "snapshot.txt", "source\n")
	first, err := s.CreateExport(ctx, u.ID, p.ID, r.ID, "frozen-export")
	if err != nil {
		t.Fatal(err)
	}
	client.Resource.UpdateOneID(r.ID).AddTranslationGeneration(1).ExecX(ctx)
	client.Project.UpdateOneID(p.ID).AddOutputGeneration(1).ExecX(ctx)
	again, err := s.CreateExport(ctx, u.ID, p.ID, r.ID, "frozen-export")
	if err != nil || again.ID != first.ID || again.ResultArtifactID == nil || *again.ResultArtifactID != *first.ResultArtifactID {
		t.Fatalf("replay changed identity %+v %v", again, err)
	}
	if _, err = s.RebuildExport(ctx, u.ID, p.ID, *first.ResultArtifactID, "frozen-export"); !errors.Is(err, ErrStorageIdempotency) {
		t.Fatal("same key changed create into rebuild")
	}
	rebuilt, err := s.RebuildExport(ctx, u.ID, p.ID, *first.ResultArtifactID, "rebuild-export")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteExport(ctx, u.ID, p.ID, *first.ResultArtifactID); err != nil {
		t.Fatal(err)
	}
	repeated, err := s.RebuildExport(ctx, u.ID, p.ID, *first.ResultArtifactID, "rebuild-export")
	if err != nil || repeated.ID != rebuilt.ID {
		t.Fatalf("deleted original changed rebuild replay %+v %v", repeated, err)
	}
}

func TestStorageExportFrozenSnapshotResumesBeforeFirstWrite(t *testing.T) {
	ctx, client, s, p, u, _ := storageLifecycleFixture(t)
	r := storageUpload(t, ctx, s, p, u, "snapshot.txt", "source\n")
	task, err := s.storage.Begin(ctx, u.ID, p.ID, StorageIntent{Kind: "export", IdempotencyKey: "before-write", ResourceID: r.ID, SourceRevisionID: *r.CurrentSourceRevisionID, SourceGeneration: r.SourceGeneration, TranslationGeneration: r.TranslationGeneration})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.freezeExportSnapshot(ctx, u.ID, task); err != nil {
		t.Fatal(err)
	}
	client.Resource.UpdateOneID(r.ID).AddTranslationGeneration(1).ExecX(ctx)
	resumed, err := s.CreateExport(ctx, u.ID, p.ID, r.ID, "before-write")
	if err != nil {
		t.Fatal(err)
	}
	artifact := client.ExportArtifact.GetX(ctx, *resumed.ResultArtifactID)
	if artifact.TranslationGeneration != r.TranslationGeneration {
		t.Fatal("resume recaptured changed resource")
	}
}

func TestStorageExportDeletionTracksBlockedCleanupAndTombstone(t *testing.T) {
	ctx, client, s, p, u, driver := storageLifecycleFixture(t)
	s.storage.deleteGrace = 0
	r := storageUpload(t, ctx, s, p, u, "snapshot.txt", "source\n")
	task, err := s.CreateExport(ctx, u.ID, p.ID, r.ID, "delete-export")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.storage.ProcessTasks(ctx); err != nil {
		t.Fatal(err)
	}
	artifact := client.ExportArtifact.GetX(ctx, *task.ResultArtifactID)
	if err = s.DeleteExport(ctx, u.ID, p.ID, artifact.ID); err != nil {
		t.Fatal(err)
	}
	deleted := client.ExportArtifact.GetX(ctx, artifact.ID)
	if deleted.DeletionTaskID == nil {
		t.Fatal("missing deletion task")
	}
	deletionID := *deleted.DeletionTaskID
	client.Project.UpdateOneID(p.ID).SetStorageState("draining").ExecX(ctx)
	if err = s.DeleteExport(ctx, u.ID, p.ID, artifact.ID); err != nil {
		t.Fatal(err)
	}
	client.Project.UpdateOneID(p.ID).SetStorageState("active").ExecX(ctx)
	if got := client.ExportArtifact.GetX(ctx, artifact.ID); *got.DeletionTaskID != deletionID {
		t.Fatal("repeat deletion changed task")
	}
	visible, err := s.ListExportsIncludingDeleted(ctx, u.ID, p.ID, r.ID, false)
	if err != nil || len(visible) != 0 {
		t.Fatal("default list leaked deleted artifact")
	}
	tombstones, err := s.ListExportsIncludingDeleted(ctx, u.ID, p.ID, r.ID, true)
	if err != nil || len(tombstones) != 1 || tombstones[0].DeletionTaskID == nil || tombstones[0].Rebuildable {
		t.Fatal("tombstone missing safe cleanup facts")
	}
	client.DeletionEntry.Update().Where(deletionentry.DeletionTaskIDEQ(deletionID)).SetNotBefore(time.Now().Add(-time.Hour)).ExecX(ctx)
	driver.failDeleteResponse = true
	if err = s.storage.collect(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.storage.refreshDeletionTaskCleanup(ctx); err != nil {
		t.Fatal(err)
	}
	dt := client.StorageTask.GetX(ctx, deletionID)
	if dt.Status != storagetask.StatusCompleted || dt.CleanupStatus != storagetask.CleanupStatusBlocked {
		t.Fatalf("wrong independent cleanup state %+v", dt)
	}
	if len(StorageAllowedActions(dt)) != 0 {
		t.Fatal("deletion task exposed unsupported actions")
	}
	if client.StorageSpace.GetX(ctx, *p.StorageSpaceID).PendingDeleteBytes == 0 {
		t.Fatal("unknown deletion prematurely freed quota")
	}
	client.DeletionEntry.Update().Where(deletionentry.DeletionTaskIDEQ(deletionID)).ClearNextRetryAt().ExecX(ctx)
	if err = s.storage.collect(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.storage.refreshDeletionTaskCleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if client.StorageTask.GetX(ctx, deletionID).CleanupStatus != storagetask.CleanupStatusDone {
		t.Fatal("confirmed cleanup not projected")
	}
	if client.StorageSpace.GetX(ctx, *p.StorageSpaceID).PendingDeleteBytes != 0 {
		t.Fatal("confirmed cleanup did not free quota")
	}
}

func TestStorageExportRebuildabilityUsesRetainedMetadata(t *testing.T) {
	ctx, client, s, p, u, _ := storageLifecycleFixture(t)
	r := storageUpload(t, ctx, s, p, u, "snapshot.txt", "source\n")
	task, err := s.CreateExport(ctx, u.ID, p.ID, r.ID, "eligibility")
	if err != nil {
		t.Fatal(err)
	}
	a := client.ExportArtifact.GetX(ctx, *task.ResultArtifactID)
	if ok, err := s.ExportRebuildable(ctx, a); err != nil || !ok {
		t.Fatal("valid metadata not rebuildable")
	}
	client.SourceRevision.UpdateOneID(a.SourceRevisionID).SetDeleted(true).ExecX(ctx)
	if ok, err := s.ExportRebuildable(ctx, a); err != nil || ok {
		t.Fatal("deleted source reported rebuildable")
	}
	if _, err = s.RebuildExport(ctx, u.ID, p.ID, a.ID, "deleted-source"); !errors.Is(err, ErrRepairMismatch) {
		t.Fatalf("rebuild skipped metadata check %v", err)
	}
}
