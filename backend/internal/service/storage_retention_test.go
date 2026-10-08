package service

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/bloblocation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/exportartifact"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

func TestStorageRetentionPreservesDurableConsumers(t *testing.T) {
	for _, consumer := range []string{"none", "job", "export", "backup"} {
		t.Run(consumer, func(t *testing.T) {
			ctx, c, r, p, u, _ := storageLifecycleFixture(t)
			res := storageUpload(t, ctx, r, p, u, "one.txt", "one")
			old := c.SourceRevision.GetX(ctx, *res.CurrentSourceRevisionID)
			b := c.Blob.GetX(ctx, old.SourceBlobID)
			loc := *b.ActiveLocationID
			preview, e := r.PreviewSourceUpdate(ctx, u.ID, p.ID, res.ID, UploadedFile{Size: 3, Reader: bytes.NewBufferString("two")})
			if e != nil {
				t.Fatal(e)
			}
			if _, _, e = r.CommitSourceUpdate(ctx, u.ID, p.ID, res.ID, preview.TaskID, preview.SourceGeneration, preview.TranslationGeneration); e != nil {
				t.Fatal(e)
			}
			c.SourceRevision.UpdateOneID(old.ID).SetRetainUntil(time.Now().Add(-time.Hour)).ExecX(ctx)
			switch consumer {
			case "job":
				j := c.Job.Create().SetProjectID(p.ID).SetExecutionPlanID(1).SetStatus("completed").SaveX(ctx)
				c.JobResource.Create().SetJobID(j.ID).SetResourceID(res.ID).SetSourceRevisionID(old.ID).SetStatus("completed").SaveX(ctx)
			case "export":
				c.ExportArtifact.Create().SetProjectID(p.ID).SetResourceID(res.ID).SetSourceRevisionID(old.ID).SetRendererVersion("test").SetFilename("one.txt").SetStatus(exportartifact.StatusReady).SaveX(ctx)
			case "backup":
				c.BackupPin.Create().SetLocationID(loc).SetBackupID("backup").SetExpiresAt(time.Now().Add(time.Hour)).SaveX(ctx)
			}
			if e = r.storage.expireSourceRevisions(ctx); e != nil {
				t.Fatal(e)
			}
			after := c.SourceRevision.GetX(ctx, old.ID)
			if consumer == "none" {
				if !after.Deleted || c.BlobLocation.GetX(ctx, loc).Status != bloblocation.StatusRetired {
					t.Fatal("expired unreferenced version was not retired")
				}
			} else if after.Deleted || c.BlobLocation.GetX(ctx, loc).Status != bloblocation.StatusLive {
				t.Fatal("retention removed a referenced version")
			}
		})
	}
}

func TestStorageRetentionLogicalQuotaIsSharedAcrossOwnerProjects(t *testing.T) {
	ctx, c, r, p, u, _ := storageLifecycleFixture(t)
	setStorageTestPolicy(t, c, `{"mode":"site_only","default_choice":"site","logical_limit_bytes":5}`)
	storageUpload(t, ctx, r, p, u, "one.txt", "one")
	other, e := r.projects.CreateProject(ctx, u.ID, CreateProjectInput{Name: "other"})
	if e != nil {
		t.Fatal(e)
	}
	_, e = r.uploadStoredResource(ctx, u.ID, other.ID, UploadedFile{Filename: "two.txt", Path: "two.txt", Size: 3, Reader: bytes.NewBufferString("two")})
	if !errors.Is(e, storage.ErrLimit) {
		t.Fatalf("second project bypassed owner quota: %v", e)
	}
	if c.Resource.Query().CountX(ctx) != 1 {
		t.Fatal("rejected upload left a resource")
	}
}

func TestStorageRetentionLegacyDeletionKeepsUnverifiedEvidence(t *testing.T) {
	ctx, c, r, p, u, d := storageLifecycleFixture(t)
	legacy := c.Resource.Create().SetProjectID(p.ID).SetPath("legacy.txt").SetFormat("txt").SetStoragePath("../unverified.txt").SaveX(ctx)
	if e := r.DeleteResource(ctx, u.ID, p.ID, legacy.ID); e != nil {
		t.Fatal(e)
	}
	task := c.StorageTask.Query().Where(storagetask.KindEQ("legacy_cleanup"), storagetask.ResourceIDEQ(legacy.ID)).OnlyX(ctx)
	if task.Status != storagetask.StatusNeedsAction || task.CleanupStatus != storagetask.CleanupStatusBlocked || task.Input["legacy_path"] != "../unverified.txt" {
		t.Fatal("legacy cleanup evidence was lost")
	}
	if e := r.storage.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	if d.deletes != 0 || c.BlobLocation.Query().CountX(ctx) != 0 {
		t.Fatal("unverified path became a physical deletion target")
	}
}

func TestStorageRetentionDeletionUsesConfiguredGrace(t *testing.T) {
	for _, entireProject := range []bool{false, true} {
		t.Run(map[bool]string{false: "resource", true: "project"}[entireProject], func(t *testing.T) {
			ctx, c, r, p, u, _ := storageLifecycleFixture(t)
			r.storage.deleteGrace = 2 * time.Hour
			res := storageUpload(t, ctx, r, p, u, "one.txt", "one")
			rev := c.SourceRevision.GetX(ctx, *res.CurrentSourceRevisionID)
			b := c.Blob.GetX(ctx, rev.SourceBlobID)
			start := time.Now()
			var err error
			if entireProject {
				_, err = r.projects.DeleteProject(ctx, u.ID, p.ID)
			} else {
				err = r.DeleteResource(ctx, u.ID, p.ID, res.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			location := c.BlobLocation.GetX(ctx, *b.ActiveLocationID)
			if location.RetainUntil == nil || location.RetainUntil.Before(start.Add(2*time.Hour)) || location.RetainUntil.After(start.Add(2*time.Hour+time.Minute)) {
				t.Fatalf("unexpected deletion grace: %v", location.RetainUntil)
			}
		})
	}
}
