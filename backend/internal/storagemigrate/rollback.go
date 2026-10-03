package storagemigrate

import (
	"context"
	"errors"
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/backuppin"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/blob"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/bloblocation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobresource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
)

// Rollback 绝不删除源文件或更改已翻译内容。它拒绝
// 撤销任何此后被编辑、修复或被新作业/备份引用的
// 已导入源。回滚应用二进制与 schema 仍需要
// 兼容的迁移前数据库备份。
func (m *Migrator) Rollback(ctx context.Context, manifest *Manifest, checkpoint func() error) error {
	if err := m.checkOffline(manifest); err != nil {
		return err
	}
	op, err := m.client.StorageTask.Query().Where(storagetask.OperationIDEQ(manifest.OperationID)).Only(ctx)
	if err != nil {
		return err
	}
	if op.Kind != "legacy_migration" || op.RequestHash != migrationFingerprint(manifest) {
		return ErrChanged
	}
	if op.Phase == "rolled_back" {
		manifest.Phase = "rolled_back"
		return checkpoint()
	}
	manifest.LegacySpaceID = number(op.Input["legacy_space_id"])
	manifest.DefaultSpaceID = number(op.Input["default_space_id"])
	// 开始回滚前对整个集合做预检。新增变更无法通过
	// 重置 generation 或仅切换默认空间来变得安全。
	for i := range manifest.Entries {
		if manifest.Entries[i].Rejected {
			continue
		}
		if err := m.checkRollbackEntry(ctx, m.client, manifest, &manifest.Entries[i]); err != nil {
			return err
		}
	}
	if err := transaction(ctx, m.client, func(client *ent.Client) error {
		for _, p := range manifest.Projects {
			row, err := client.Project.Get(ctx, p.ID)
			if err != nil {
				return err
			}
			if row.StorageGeneration != p.Generation+1 {
				return ErrChanged
			}
			ids, err := client.Resource.Query().Where(resource.ProjectIDEQ(p.ID)).Order(ent.Asc(resource.FieldID)).IDs(ctx)
			if err != nil {
				return err
			}
			if digest(ids) != digest(p.ResourceIDs) || row.OutputGeneration != p.OutputGeneration {
				return ErrChanged
			}
			newTask, err := client.StorageTask.Query().Where(storagetask.ProjectIDEQ(p.ID), storagetask.CreatedAtGTE(manifest.CreatedAt)).Exist(ctx)
			if err != nil {
				return err
			}
			if newTask {
				return errors.New("rollback refused: project has accepted a new storage operation")
			}
			if row.StorageState != "active" && row.StorageState != "legacy_migration" && row.StorageState != "legacy_rollback" {
				return ErrChanged
			}
			if err := client.Project.UpdateOneID(p.ID).SetStorageState("legacy_rollback").Exec(ctx); err != nil {
				return err
			}
		}
		for _, saved := range manifest.Jobs {
			row, err := client.Job.Get(ctx, saved.ID)
			if err != nil {
				return err
			}
			if row.Status != "failed" || row.ErrorMessage == nil || *row.ErrorMessage != blockedJobError {
				return ErrChanged
			}
		}
		return client.StorageTask.UpdateOneID(op.ID).SetStatus(storagetask.StatusNeedsAction).SetPhase("rolling_back").Exec(ctx)
	}); err != nil {
		return err
	}
	manifest.Phase = "rolling_back"
	if err := checkpoint(); err != nil {
		return err
	}
	for i := range manifest.Entries {
		entry := &manifest.Entries[i]
		if entry.Rejected {
			continue
		}
		if err := transaction(ctx, m.client, func(client *ent.Client) error {
			if err := m.checkRollbackEntry(ctx, client, manifest, entry); err != nil {
				return err
			}
			if !entry.Applied {
				return nil
			}
			location, err := client.BlobLocation.Get(ctx, entry.LocationID)
			if err != nil {
				return err
			}
			if err := client.Resource.UpdateOneID(entry.ResourceID).ClearCurrentSourceRevisionID().SetSourceGeneration(entry.SourceGeneration).Exec(ctx); err != nil {
				return err
			}
			if err := client.SourceRevision.DeleteOneID(entry.RevisionID).Exec(ctx); err != nil {
				return err
			}
			if err := client.Blob.UpdateOneID(entry.BlobID).ClearActiveLocationID().Exec(ctx); err != nil {
				return err
			}
			if err := client.BlobLocation.DeleteOneID(entry.LocationID).Exec(ctx); err != nil {
				return err
			}
			if err := client.Blob.DeleteOneID(entry.BlobID).Exec(ctx); err != nil {
				return err
			}
			return client.StorageSpace.UpdateOneID(manifest.LegacySpaceID).AddLiveBytes(-location.Size).Exec(ctx)
		}); err != nil {
			return err
		}
		entry.Applied = false
		entry.BlobID = 0
		entry.LocationID = 0
		entry.RevisionID = 0
		if err := checkpoint(); err != nil {
			return err
		}
	}
	if err := transaction(ctx, m.client, func(client *ent.Client) error {
		for _, p := range manifest.Projects {
			row, err := client.Project.Get(ctx, p.ID)
			if err != nil {
				return err
			}
			if row.StorageGeneration != p.Generation+1 || row.StorageState != "legacy_rollback" {
				return ErrChanged
			}
			update := client.Project.UpdateOneID(p.ID).SetStorageGeneration(p.Generation).SetStorageState("active")
			if p.PreviousSpaceID == nil {
				update.ClearStorageSpaceID()
			} else {
				update.SetStorageSpaceID(*p.PreviousSpaceID)
			}
			if err := update.Exec(ctx); err != nil {
				return err
			}
		}
		for _, saved := range manifest.Jobs {
			update := client.Job.UpdateOneID(saved.ID).SetStatus(saved.Status)
			if saved.Error == nil {
				update.ClearErrorMessage()
			} else {
				update.SetErrorMessage(*saved.Error)
			}
			if err := update.Exec(ctx); err != nil {
				return err
			}
		}
		return client.StorageTask.UpdateOneID(op.ID).SetStatus(storagetask.StatusCancelled).SetPhase("rolled_back").SetErrorCode("").Exec(ctx)
	}); err != nil {
		return err
	}
	manifest.Phase = "rolled_back"
	return checkpoint()
}

func (m *Migrator) checkRollbackEntry(ctx context.Context, client *ent.Client, manifest *Manifest, entry *Entry) error {
	row, err := client.Resource.Get(ctx, entry.ResourceID)
	if err != nil {
		return err
	}
	snapshot, err := snapshotEnt(ctx, client, row)
	if err != nil {
		return err
	}
	if digest(snapshot) != entry.DatabaseDigest || row.TranslationGeneration != entry.TranslationGeneration {
		return fmt.Errorf("resource %d: %w", entry.ResourceID, ErrChanged)
	}
	object, err := client.Blob.Query().Where(blob.IdentityEQ(fmt.Sprintf("legacy-%s-%d", manifest.OperationID, entry.ResourceID))).Only(ctx)
	if ent.IsNotFound(err) {
		if row.CurrentSourceRevisionID != nil || row.SourceGeneration != entry.SourceGeneration {
			return ErrChanged
		}
		entry.Applied = false
		return nil
	}
	if err != nil {
		return err
	}
	if row.CurrentSourceRevisionID == nil || row.SourceGeneration != entry.SourceGeneration+1 || object.ActiveLocationID == nil || object.LocationGeneration != 1 || object.Status != blob.StatusReady {
		return ErrChanged
	}
	revision, err := client.SourceRevision.Get(ctx, *row.CurrentSourceRevisionID)
	if err != nil {
		return err
	}
	if revision.SourceBlobID != object.ID || !revision.Current || revision.Deleted {
		return ErrChanged
	}
	location, err := client.BlobLocation.Get(ctx, *object.ActiveLocationID)
	if err != nil {
		return err
	}
	if location.SpaceID != manifest.LegacySpaceID || location.ObjectKey != entry.ObjectKey || location.Status != bloblocation.StatusLive {
		return ErrChanged
	}
	pinned, err := client.BackupPin.Query().Where(backuppin.LocationIDEQ(location.ID)).Exist(ctx)
	if err != nil {
		return err
	}
	if pinned {
		return errors.New("rollback refused: imported object is pinned by a backup")
	}
	referenced, err := client.JobResource.Query().Where(jobresource.SourceRevisionIDEQ(revision.ID)).Exist(ctx)
	if err != nil {
		return err
	}
	if referenced {
		return errors.New("rollback refused: a new job references the imported source")
	}
	entry.BlobID = object.ID
	entry.LocationID = location.ID
	entry.RevisionID = revision.ID
	entry.Applied = true
	return nil
}
