package storagemigrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/blob"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagespace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
)

type fixture struct {
	migration *Migrator
	client    *ent.Client
	project   *ent.Project
	resource  *ent.Resource
	segment   *ent.Segment
	job       *ent.Job
	root      string
}

func TestDeletedLegacyTombstoneIsObservedWithoutDeletingBytes(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	task, err := f.client.StorageTask.Create().SetOperationID("legacy-cleanup-test").SetIdempotencyKey("legacy-cleanup-test").SetRequestHash("legacy-cleanup-test").SetProjectID(f.project.ID).SetResourceID(f.resource.ID).SetKind("legacy_cleanup").SetStatus(storagetask.StatusNeedsAction).SetCleanupStatus(storagetask.CleanupStatusBlocked).SetInput(map[string]any{"legacy_path": f.resource.StoragePath, "format": "txt", "owner_kind": "user", "owner_id": 1}).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.client.JobResource.Delete().Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.client.Segment.Delete().Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.client.Resource.DeleteOneID(f.resource.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.client.Job.DeleteOneID(f.job.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.client.Project.DeleteOneID(f.project.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	manifest, err := f.migration.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Entries) != 0 || len(manifest.LegacyCleanup) != 1 {
		t.Fatal("deleted legacy evidence missing")
	}
	item := manifest.LegacyCleanup[0]
	if item.TaskID != task.ID || item.ObservedSHA256 == "" || item.Evidence != "current_bytes_observed_ownership_unproven" {
		t.Fatalf("invalid observation: %+v", item)
	}
	if _, err := os.Stat(filepath.Join(f.root, filepath.FromSlash(f.resource.StoragePath))); err != nil {
		t.Fatal("inventory removed original")
	}
	row, err := f.client.StorageTask.Get(ctx, task.ID)
	if err != nil || row.CleanupStatus != storagetask.CleanupStatusBlocked {
		t.Fatal("inventory authorized cleanup")
	}
}

func setup(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	cfg := config.DefaultServerConfig()
	cfg.DataDir = dir
	cfg.AutoMigrate = true
	db, client, err := database.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	user, err := client.User.Create().SetUsername("owner").SetEmail("owner@test.invalid").SetPasswordHash("test-hash").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	project, err := client.Project.Create().SetName("legacy").SetOwnerUserID(user.ID).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "jobs")
	if err := os.MkdirAll(filepath.Join(root, "resources", "project-1"), 0700); err != nil {
		t.Fatal(err)
	}
	key := "resources/project-1/original.txt"
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(key)), []byte("Original text\n"), 0600); err != nil {
		t.Fatal(err)
	}
	resource, err := client.Resource.Create().SetProjectID(project.ID).SetPath("original.txt").SetFormat("txt").SetStoragePath(key).SetTotalSegments(1).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	segment, err := client.Segment.Create().SetResourceID(resource.ID).SetSegmentIndex(0).SetSourceText("Original text").SetTargetText("保留现有译文").SetStatus("approved").SetMeta(`{"pos_lines":[1,1]}`).SetReviewedByID(user.ID).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	job, err := client.Job.Create().SetProjectID(project.ID).SetExecutionPlanID(1).SetStatus("paused").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.JobResource.Create().SetJob(job).SetResource(resource).SetStatus("running").Save(ctx); err != nil {
		t.Fatal(err)
	}
	migration, err := New(db, client, "sqlite", Options{LegacyRoot: root, DefaultRoot: filepath.Join(dir, "objects"), Offline: true, BackupConfirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	return fixture{migration, client, project, resource, segment, job, root}
}

func TestInventoryApplyResumeRollbackPreservesBusinessData(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	manifest, err := f.migration.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Entries) != 1 || manifest.Entries[0].Verification != "verified" || manifest.Entries[0].ObservedSHA256 == "" {
		t.Fatalf("inventory: %+v", manifest.Entries)
	}
	if count, err := f.client.Blob.Query().Count(ctx); err != nil || count != 0 {
		t.Fatal("inventory wrote blob metadata")
	}
	manifestPath := filepath.Join(t.TempDir(), "manifest.json")
	if err := SaveManifest(manifestPath, manifest, true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "保留现有译文") {
		t.Fatal("manifest exposed translation text")
	}
	calls := 0
	crash := errors.New("simulated checkpoint loss")
	err = f.migration.Apply(ctx, manifest, func() error {
		calls++
		if calls == 2 {
			return crash
		}
		return SaveManifest(manifestPath, manifest, false)
	})
	if !errors.Is(err, crash) {
		t.Fatalf("expected crash after resource commit: %v", err)
	}
	manifest, err = ReadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Entries[0].Applied {
		t.Fatal("checkpoint unexpectedly recorded committed resource")
	}
	if err := f.migration.Apply(ctx, manifest, func() error { return SaveManifest(manifestPath, manifest, false) }); err != nil {
		t.Fatal(err)
	}
	if count, err := f.client.Blob.Query().Count(ctx); err != nil || count != 1 {
		t.Fatalf("resume duplicated objects: %d %v", count, err)
	}
	resource, err := f.client.Resource.Get(ctx, f.resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resource.CurrentSourceRevisionID == nil || resource.SourceGeneration != 1 || resource.StoragePath != f.resource.StoragePath {
		t.Fatalf("resource: %+v", resource)
	}
	revision, err := f.client.SourceRevision.Get(ctx, *resource.CurrentSourceRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	if revision.ParserVersion != "legacy_unknown" || string(revision.VerificationState) != "verified" {
		t.Fatalf("revision: %+v", revision)
	}
	segment, err := f.client.Segment.Get(ctx, f.segment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if segment.SourceText != f.segment.SourceText || segment.TargetText == nil || *segment.TargetText != *f.segment.TargetText || segment.Status != f.segment.Status || *segment.Meta != *f.segment.Meta {
		t.Fatal("migration changed source, translation or metadata")
	}
	space, err := f.client.StorageSpace.Get(ctx, manifest.LegacySpaceID)
	if err != nil {
		t.Fatal(err)
	}
	if space.Status != storagespace.StatusReadOnly || space.LiveBytes != int64(len("Original text\n")) {
		t.Fatalf("legacy accounting: %+v", space)
	}
	defaultSpace, err := f.client.StorageSpace.Get(ctx, manifest.DefaultSpaceID)
	if err != nil || defaultSpace.Verified {
		t.Fatal("new default space bypassed physical marker admission")
	}
	job, err := f.client.Job.Get(ctx, f.job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "failed" || job.ErrorMessage == nil || *job.ErrorMessage != blockedJobError {
		t.Fatal("legacy paused job could automatically run")
	}
	if err := f.migration.Rollback(ctx, manifest, func() error { return SaveManifest(manifestPath, manifest, false) }); err != nil {
		t.Fatal(err)
	}
	resource, err = f.client.Resource.Get(ctx, f.resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resource.CurrentSourceRevisionID != nil || resource.SourceGeneration != 2 {
		t.Fatal("source reference not rolled back")
	}
	if project := f.client.Project.GetX(ctx, f.project.ID); project.StorageGeneration != 4 || project.StorageState != "active" {
		t.Fatalf("rollback must preserve monotonic maintenance generations: %+v", project)
	}
	job, err = f.client.Job.Get(ctx, f.job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "paused" || job.ErrorMessage != nil {
		t.Fatal("job baseline not restored")
	}
	if data, err := os.ReadFile(filepath.Join(f.root, filepath.FromSlash(f.resource.StoragePath))); err != nil || string(data) != "Original text\n" {
		t.Fatalf("original file changed: %q %v", data, err)
	}
	if err := f.migration.Rollback(ctx, manifest, func() error { return nil }); err != nil {
		t.Fatalf("idempotent rollback: %v", err)
	}
}

func TestMissingAndHistoricallyEditedSourcesRemainUnverified(t *testing.T) {
	for _, mode := range []string{"missing", "edited", "unknown format"} {
		t.Run(mode, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			switch mode {
			case "missing":
				if err := os.Remove(filepath.Join(f.root, filepath.FromSlash(f.resource.StoragePath))); err != nil {
					t.Fatal(err)
				}
			case "edited":
				if err := f.client.Segment.UpdateOneID(f.segment.ID).SetSourceText("Historical legitimate source edit").Exec(ctx); err != nil {
					t.Fatal(err)
				}
			case "unknown format":
				if err := f.client.Resource.UpdateOneID(f.resource.ID).SetFormat("docx").Exec(ctx); err != nil {
					t.Fatal(err)
				}
			}
			manifest, err := f.migration.Inventory(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if manifest.Entries[0].Verification != "legacy_unverified" {
				t.Fatal("unproven baseline marked verified")
			}
			if err := f.migration.Apply(ctx, manifest, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			revision, err := f.client.SourceRevision.Get(ctx, manifest.Entries[0].RevisionID)
			if err != nil {
				t.Fatal(err)
			}
			if revision.Size != nil || revision.Sha256 != nil || string(revision.VerificationState) != "legacy_unverified" {
				t.Fatal("trusted digest fabricated")
			}
			object, err := f.client.Blob.Get(ctx, manifest.Entries[0].BlobID)
			if err != nil {
				t.Fatal(err)
			}
			if object.Status != blob.StatusReady || object.Size != nil || object.Sha256 != nil {
				t.Fatalf("legacy object: %+v", object)
			}
			segment, err := f.client.Segment.Get(ctx, f.segment.ID)
			if err != nil || segment.TargetText == nil || *segment.TargetText != *f.segment.TargetText {
				t.Fatal("existing translation lost")
			}
		})
	}
}

func TestRollbackRejectsNewEditsAndRepairs(t *testing.T) {
	for _, kind := range []string{"translation", "location"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			manifest, err := f.migration.Inventory(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.migration.Apply(ctx, manifest, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			if kind == "translation" {
				if err := f.client.Segment.UpdateOneID(f.segment.ID).SetTargetText("new translation").Exec(ctx); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := f.client.Blob.UpdateOneID(manifest.Entries[0].BlobID).AddLocationGeneration(1).Exec(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.migration.Rollback(ctx, manifest, func() error { return nil }); !errors.Is(err, ErrChanged) {
				t.Fatalf("rollback accepted new work: %v", err)
			}
			if count, err := f.client.SourceRevision.Query().Count(ctx); err != nil || count != 1 {
				t.Fatal("failed rollback deleted references")
			}
		})
	}
}

func TestInventoryBeforeStorageSchemaAndRejectTraversal(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if err := f.client.Resource.UpdateOneID(f.resource.ID).SetStoragePath("../outside.txt").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"current_source_revision_id", "source_generation", "translation_generation"} {
		if _, err := f.migration.db.ExecContext(ctx, "ALTER TABLE resources DROP COLUMN "+column); err != nil {
			t.Fatal(err)
		}
	}
	manifest, err := f.migration.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Entries) != 1 || !manifest.Entries[0].Rejected || manifest.Entries[0].Evidence != "unsafe_storage_path" {
		t.Fatalf("unsafe reference accepted: %+v", manifest.Entries)
	}
}

func TestApplyRequiresOfflineAndUnchangedBaseline(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	manifest, err := f.migration.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.migration.options.Offline = false
	if err := f.migration.Apply(ctx, manifest, func() error { return nil }); err == nil {
		t.Fatal("apply without offline confirmation")
	}
	f.migration.options.Offline = true
	if err := f.client.Segment.UpdateOneID(f.segment.ID).SetTargetText("edited after inventory").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.migration.Apply(ctx, manifest, func() error { return nil }); !errors.Is(err, ErrChanged) {
		t.Fatalf("stale inventory: %v", err)
	}
	if count, err := f.client.StorageTask.Query().Count(ctx); err != nil || count != 0 {
		t.Fatal("stale inventory created migration state")
	}
}

func TestInventoryRejectsSharedPathsAndNestedRoots(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if _, err := f.client.Resource.Create().SetProjectID(f.project.ID).SetPath("copy.txt").SetFormat("txt").SetStoragePath(f.resource.StoragePath).Save(ctx); err != nil {
		t.Fatal(err)
	}
	manifest, err := f.migration.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Entries) != 2 {
		t.Fatal("missing inventory entries")
	}
	for _, entry := range manifest.Entries {
		if !entry.Rejected || entry.Evidence != "shared_storage_path_unproven" {
			t.Fatal("shared path accepted")
		}
	}
	options := f.migration.options
	options.DefaultRoot = filepath.Join(options.LegacyRoot, "objects")
	if _, err := New(f.migration.db, f.client, "sqlite", options); err == nil {
		t.Fatal("nested roots accepted")
	}
	manifest.DefaultRoot = options.DefaultRoot
	if err := manifest.validate(); err == nil {
		t.Fatal("nested manifest roots accepted")
	}
}

func TestChangedBytesDoNotStartMigration(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	manifest, err := f.migration.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, filepath.FromSlash(f.resource.StoragePath)), []byte("different bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.migration.Apply(ctx, manifest, func() error { return nil }); !errors.Is(err, ErrChanged) {
		t.Fatalf("changed bytes accepted: %v", err)
	}
	if count, err := f.client.StorageTask.Query().Count(ctx); err != nil || count != 0 {
		t.Fatal("changed bytes created migration state")
	}
	project, err := f.client.Project.Get(ctx, f.project.ID)
	if err != nil || project.StorageState != "active" {
		t.Fatal("changed bytes left project gated")
	}
}
