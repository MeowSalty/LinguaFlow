package service

import (
	"bytes"
	"context"
	"io"
	"strconv"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/bloblocation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/deletionentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tasklife"
)

// Exercise the actual history deletion service against stored source revisions
// and exports. Removing a task reference never transfers file ownership to it.
func TestTaskHistoryStorageReferencesRespectConsumersAndGrace(t *testing.T) {
	for _, consumer := range []string{"none", "current_revision", "other_job", "backup_pin", "source_retention", "export"} {
		t.Run(consumer, func(t *testing.T) {
			ctx, client, resources, project, owner, driver := storageLifecycleFixture(t)
			resources.storage.deleteGrace = 2 * time.Hour
			history := NewTaskHistoryService(client, resources.projects, &tasklife.Coordinator{}, nil)
			history.SetReady(true)
			history.SetLogger(discardLogger())
			res := storageUpload(t, ctx, resources, project, owner, "history.txt", "old source\n")
			old := client.SourceRevision.GetX(ctx, *res.CurrentSourceRevisionID)
			oldBlob := client.Blob.GetX(ctx, old.SourceBlobID)
			oldLocation := client.BlobLocation.GetX(ctx, *oldBlob.ActiveLocationID)
			var oldExportID int
			var oldExportContents []byte
			if consumer == "export" {
				client.Segment.Update().Where(segment.ResourceIDEQ(res.ID)).SetTargetText("旧版译文").SetStatus(segment.StatusEdited).ExecX(ctx)
				oldExportID, oldExportContents = historyStorageExport(t, ctx, resources, project.ID, owner.ID, res.ID, "old-export")
			}
			original := "old source\n"
			if consumer != "current_revision" {
				original = "new source\n"
				preview, err := resources.PreviewSourceUpdate(ctx, owner.ID, project.ID, res.ID, UploadedFile{Size: int64(len(original)), Reader: bytes.NewBufferString(original)})
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err = resources.CommitSourceUpdate(ctx, owner.ID, project.ID, res.ID, preview.TaskID, preview.SourceGeneration, preview.TranslationGeneration); err != nil {
					t.Fatal(err)
				}
			}
			res = client.Resource.GetX(ctx, res.ID)
			client.Segment.Update().Where(segment.ResourceIDEQ(res.ID)).SetTargetText("保留译文").SetStatus(segment.StatusEdited).ExecX(ctx)
			exportID, exportContents := historyStorageExport(t, ctx, resources, project.ID, owner.ID, res.ID, "current-export")
			if !bytes.Contains(exportContents, []byte("保留译文")) {
				t.Fatalf("export fixture has no translation: %q", exportContents)
			}
			retainUntil := time.Now().UTC().Add(-time.Hour)
			if consumer == "source_retention" {
				retainUntil = time.Now().UTC().Add(24 * time.Hour)
			}
			client.SourceRevision.UpdateOneID(old.ID).SetRetainUntil(retainUntil).ExecX(ctx)
			removed := historyStorageJob(t, ctx, client, project.ID, res.ID, old.ID, oldLocation.ObjectKey)
			var other *ent.Job
			if consumer == "other_job" {
				other = historyStorageJob(t, ctx, client, project.ID, res.ID, old.ID, "")
			}
			var pin *ent.BackupPin
			if consumer == "backup_pin" {
				pin = client.BackupPin.Create().SetLocationID(oldLocation.ID).SetBackupID("task-history-test").SetExpiresAt(time.Now().UTC().Add(24 * time.Hour)).SaveX(ctx)
			}
			storageTasksBefore := client.StorageTask.Query().CountX(ctx)
			exportsBefore := client.ExportArtifact.Query().CountX(ctx)
			driver.mu.Lock()
			deletesBefore := driver.deletes
			objectsBefore := len(driver.objects)
			driver.mu.Unlock()
			if err := history.Delete(ctx, owner.ID, HistoryTarget{Kind: OperationTranslation, ID: strconv.Itoa(removed.ID), ProjectID: project.ID}); err != nil {
				t.Fatal(err)
			}
			if client.Job.Query().Where(job.IDEQ(removed.ID)).ExistX(ctx) {
				t.Fatal("history deletion left task")
			}
			if client.SourceRevision.GetX(ctx, old.ID).Deleted || client.BlobLocation.GetX(ctx, oldLocation.ID).Status != bloblocation.StatusLive {
				t.Fatal("history deletion itself retired source data")
			}
			if client.StorageTask.Query().CountX(ctx) != storageTasksBefore || client.ExportArtifact.Query().CountX(ctx) != exportsBefore {
				t.Fatal("history deletion changed storage tasks or export artifacts")
			}
			if pin != nil && !client.BackupPin.Query().ExistX(ctx) {
				t.Fatal("history deletion removed backup pin")
			}
			if other != nil && !client.Job.Query().Where(job.IDEQ(other.ID)).ExistX(ctx) {
				t.Fatal("history deletion removed another task's reference")
			}
			driver.mu.Lock()
			untouched := driver.deletes == deletesBefore && len(driver.objects) == objectsBefore
			driver.mu.Unlock()
			if !untouched {
				t.Fatal("history deletion changed physical objects")
			}
			historyAssertStorageResults(t, ctx, client, resources, owner.ID, project.ID, res, original, exportID, exportContents)
			if oldExportID != 0 {
				if got := historyStorageDownload(t, ctx, resources, owner.ID, project.ID, oldExportID); !bytes.Equal(got, oldExportContents) {
					t.Fatal("history deletion changed retained old export")
				}
			}
			retirementStarted := time.Now().UTC()
			if err := resources.storage.expireSourceRevisions(ctx); err != nil {
				t.Fatal(err)
			}
			if err := resources.storage.collect(ctx); err != nil {
				t.Fatal(err)
			}
			if consumer != "none" {
				if client.SourceRevision.GetX(ctx, old.ID).Deleted || client.BlobLocation.GetX(ctx, oldLocation.ID).Status != bloblocation.StatusLive {
					t.Fatalf("storage ignored %s after task reference was removed", consumer)
				}
				if client.DeletionEntry.Query().Where(deletionentry.LocationIDEQ(oldLocation.ID)).ExistX(ctx) {
					t.Fatal("protected source was scheduled for deletion")
				}
				return
			}
			retired := client.BlobLocation.GetX(ctx, oldLocation.ID)
			entry := client.DeletionEntry.Query().Where(deletionentry.LocationIDEQ(oldLocation.ID)).OnlyX(ctx)
			if !client.SourceRevision.GetX(ctx, old.ID).Deleted || retired.Status != bloblocation.StatusRetired {
				t.Fatal("unreferenced expired source did not enter storage retirement")
			}
			if retired.RetainUntil == nil || retired.RetainUntil.Before(retirementStarted.Add(2*time.Hour)) || !entry.NotBefore.Equal(*retired.RetainUntil) {
				t.Fatal("task deletion bypassed configured physical deletion grace")
			}
			driver.mu.Lock()
			_, stillStored := driver.objects[oldLocation.ObjectKey]
			driver.mu.Unlock()
			if !stillStored {
				t.Fatal("collector deleted source before grace expired")
			}
			// Advance only this test object's storage deadlines. Collection remains
			// an explicit storage action, never part of task history deletion.
			past := time.Now().UTC().Add(-time.Hour)
			client.BlobLocation.UpdateOneID(retired.ID).SetRetainUntil(past).ExecX(ctx)
			client.DeletionEntry.UpdateOneID(entry.ID).SetNotBefore(past).ClearNextRetryAt().ExecX(ctx)
			if err := resources.storage.collect(ctx); err != nil {
				t.Fatal(err)
			}
			if client.BlobLocation.GetX(ctx, retired.ID).Status != bloblocation.StatusDeleted || client.DeletionEntry.GetX(ctx, entry.ID).Status != deletionentry.StatusDone {
				t.Fatal("eligible source was not collected by storage")
			}
			driver.mu.Lock()
			_, stillStored = driver.objects[oldLocation.ObjectKey]
			driver.mu.Unlock()
			if stillStored {
				t.Fatal("confirmed source collection left old object")
			}
			historyAssertStorageResults(t, ctx, client, resources, owner.ID, project.ID, res, original, exportID, exportContents)
		})
	}
}

func historyStorageJob(t *testing.T, ctx context.Context, client *ent.Client, projectID, resourceID, revisionID int, outputPath string) *ent.Job {
	t.Helper()
	row := client.Job.Create().SetProjectID(projectID).SetExecutionPlanID(1).SetStatus(JobStatusCompleted).SaveX(ctx)
	client.JobResource.Create().SetJobID(row.ID).SetResourceID(resourceID).SetSourceRevisionID(revisionID).SetStatus(JobResourceStatusCompleted).SetOutputPath(outputPath).ExecX(ctx)
	return row
}

func historyStorageExport(t *testing.T, ctx context.Context, resources *ResourceService, projectID, ownerID, resourceID int, key string) (int, []byte) {
	t.Helper()
	task, err := resources.CreateExport(ctx, ownerID, projectID, resourceID, key)
	if err != nil {
		t.Fatal(err)
	}
	if err = resources.storage.ProcessTasks(ctx); err != nil {
		t.Fatal(err)
	}
	if task.ResultArtifactID == nil {
		t.Fatal("export has no artifact")
	}
	return *task.ResultArtifactID, historyStorageDownload(t, ctx, resources, ownerID, projectID, *task.ResultArtifactID)
}

func historyStorageDownload(t *testing.T, ctx context.Context, resources *ResourceService, ownerID, projectID, artifactID int) []byte {
	t.Helper()
	reader, _, err := resources.DownloadExport(ctx, ownerID, projectID, artifactID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	closeErr := reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	return data
}

func historyAssertStorageResults(t *testing.T, ctx context.Context, client *ent.Client, resources *ResourceService, ownerID, projectID int, before *ent.Resource, original string, exportID int, exportContents []byte) {
	t.Helper()
	current := client.Resource.GetX(ctx, before.ID)
	if current.CurrentSourceRevisionID == nil || *current.CurrentSourceRevisionID != *before.CurrentSourceRevisionID || current.SourceGeneration != before.SourceGeneration || current.TranslationGeneration != before.TranslationGeneration {
		t.Fatal("task cleanup changed current source or translation identity")
	}
	reader, err := resources.OriginalFile(ctx, ownerID, projectID, before.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	closeErr := reader.Close()
	if err != nil || closeErr != nil || string(data) != original {
		t.Fatalf("current original changed: %q %v %v", data, err, closeErr)
	}
	var rendered bytes.Buffer
	if err := resources.RenderTranslatedResource(ctx, ownerID, current, &rendered); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(rendered.Bytes(), []byte("保留译文")) {
		t.Fatalf("translation changed: %q", rendered.Bytes())
	}
	if got := historyStorageDownload(t, ctx, resources, ownerID, projectID, exportID); !bytes.Equal(got, exportContents) {
		t.Fatal("current export changed")
	}
}
