package service

import (
	"context"
	"fmt"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/blob"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/bloblocation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/exportartifact"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/sourcerevision"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
)

// registerResourceDeletion 在业务删除事务内运行。存储记录以数字化的归属
// 墓碑记录保存归属关系，而不使用级联的业务外键。
func registerResourceDeletion(ctx context.Context, tx *ent.Client, projectID, resourceID int, service *StorageService) error {
	p, err := tx.Project.Get(ctx, projectID)
	if err != nil {
		return err
	}
	if p.StorageState != "active" {
		return ErrStorageMaintenance
	}
	// 移除资源之前先使进行中的结果写入失效。它们带 generation 守卫的提交
	// 会在这一行上串行化，无法"复活"该资源。
	if _, err = tx.Resource.Update().Where(resource.IDEQ(resourceID), resource.ProjectIDEQ(projectID)).AddSourceGeneration(1).Save(ctx); err != nil {
		return err
	}
	if _, err = tx.StorageTask.Update().Where(storagetask.ProjectIDEQ(projectID), storagetask.ResourceIDEQ(resourceID), storagetask.PhaseNEQ("committed")).SetStatus(storagetask.StatusCancelled).SetCleanupStatus(storagetask.CleanupStatusCleanupPending).Save(ctx); err != nil {
		return err
	}
	revisions, err := tx.SourceRevision.Query().Where(sourcerevision.ProjectIDEQ(projectID), sourcerevision.ResourceIDEQ(resourceID)).All(ctx)
	if err != nil {
		return err
	}
	if len(revisions) == 0 {
		legacy, e := tx.Resource.Get(ctx, resourceID)
		if e != nil {
			return e
		}
		if legacy.StoragePath != "" {
			kind, owner := storageProjectOwner(p)
			// 不存在可信 location：保留证据供离线归属校验使用，
			// 绝不把该路径当作删除 key 解释。
			key := fmt.Sprintf("legacy-resource-%d", resourceID)
			exists, e := tx.StorageTask.Query().Where(storagetask.ProjectIDEQ(projectID), storagetask.KindEQ("legacy_cleanup"), storagetask.IdempotencyKeyEQ(key)).Exist(ctx)
			if e != nil {
				return e
			}
			if !exists {
				e = tx.StorageTask.Create().SetOperationID(generateUniqueID()).SetIdempotencyKey(key).SetRequestHash(key).SetProjectID(projectID).SetResourceID(resourceID).SetKind("legacy_cleanup").SetPhase("unverified").SetStatus(storagetask.StatusNeedsAction).SetCleanupStatus(storagetask.CleanupStatusBlocked).SetErrorCode("legacy_location_unverified").SetInput(map[string]any{"legacy_path": legacy.StoragePath, "resource_path": legacy.Path, "format": legacy.Format, "owner_kind": kind, "owner_id": owner}).Exec(ctx)
				if e != nil {
					return e
				}
			}
		}
	}
	blobs := map[int]bool{}
	for _, r := range revisions {
		blobs[r.SourceBlobID] = true
		if err = tx.SourceRevision.UpdateOneID(r.ID).SetDeleted(true).SetCurrent(false).Exec(ctx); err != nil {
			return err
		}
	}
	artifacts, err := tx.ExportArtifact.Query().Where(exportartifact.ProjectIDEQ(projectID), exportartifact.ResourceIDEQ(resourceID)).All(ctx)
	if err != nil {
		return err
	}
	for _, a := range artifacts {
		if a.OutputBlobID != nil {
			blobs[*a.OutputBlobID] = true
		}
		if a.SnapshotBlobID != nil {
			blobs[*a.SnapshotBlobID] = true
		}
		if err = tx.ExportArtifact.UpdateOneID(a.ID).SetStatus(exportartifact.StatusDeleted).Exec(ctx); err != nil {
			return err
		}
	}
	if service == nil {
		service = &StorageService{deleteGrace: 24 * time.Hour}
	}
	for id := range blobs {
		if err = tx.Blob.UpdateOneID(id).SetStatus(blob.StatusDeletePending).ClearActiveLocationID().Exec(ctx); err != nil {
			return err
		}
		locations, e := tx.BlobLocation.Query().Where(bloblocation.BlobIDEQ(id), bloblocation.StatusNotIn(bloblocation.StatusDeleted, bloblocation.StatusDeleting)).All(ctx)
		if e != nil {
			return e
		}
		for _, l := range locations {
			if e = service.retireLocation(ctx, tx, l.ID, projectID); e != nil {
				return e
			}
		}
	}
	return nil
}
