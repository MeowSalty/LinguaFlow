package service

import (
	"context"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/backuppin"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/blob"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/bloblocation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/exportartifact"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobresource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/sourcerevision"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
)

// expireSourceRevisions 只退役已过期且没有持久消费者的不可变版本。
// location 删除仍遵守其宽限期与备份固定（pin）。
func (s *StorageService) expireSourceRevisions(ctx context.Context) error {
	if s.maintenance {
		return nil
	}
	now := time.Now().UTC()
	revisions, err := s.client.SourceRevision.Query().Where(sourcerevision.CurrentEQ(false), sourcerevision.DeletedEQ(false), sourcerevision.RetainUntilLTE(now)).Order(ent.Asc(sourcerevision.FieldUpdatedAt), ent.Asc(sourcerevision.FieldID)).Limit(s.cfg.ReconcileBatchSize).All(ctx)
	if err != nil {
		return err
	}
	for _, candidate := range revisions {
		err = withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
			p, e := tx.Project.Get(ctx, candidate.ProjectID)
			if ent.IsNotFound(e) {
				return nil
			}
			if e != nil {
				return e
			}
			if p.StorageState != "active" {
				return nil
			}
			if e = storageProjectGate(ctx, tx, p.ID, p.StorageGeneration); e != nil {
				return e
			}
			n, e := tx.SourceRevision.Update().Where(sourcerevision.IDEQ(candidate.ID), sourcerevision.CurrentEQ(false), sourcerevision.DeletedEQ(false), sourcerevision.RetainUntilLTE(now)).SetCurrent(false).Save(ctx)
			if e != nil || n != 1 {
				return e
			}
			live, e := tx.Resource.Query().Where(resource.CurrentSourceRevisionIDEQ(candidate.ID)).Exist(ctx)
			if e != nil || live {
				return e
			}
			live, e = tx.JobResource.Query().Where(jobresource.SourceRevisionIDEQ(candidate.ID)).Exist(ctx)
			if e != nil || live {
				return e
			}
			live, e = tx.ExportArtifact.Query().Where(exportartifact.SourceRevisionIDEQ(candidate.ID), exportartifact.StatusNEQ(exportartifact.StatusDeleted)).Exist(ctx)
			if e != nil || live {
				return e
			}
			live, e = tx.StorageTask.Query().Where(storagetask.SourceRevisionIDEQ(candidate.ID), storagetask.StatusNotIn(storagetask.StatusCompleted, storagetask.StatusCancelled)).Exist(ctx)
			if e != nil || live {
				return e
			}
			live, e = tx.BackupPin.Query().Where(backuppin.HasLocationWith(bloblocation.BlobIDEQ(candidate.SourceBlobID)), backuppin.ExpiresAtGT(now)).Exist(ctx)
			if e != nil || live {
				return e
			}
			// 只要还有其他修订引用，被复用的逻辑 Blob 就仍然存活。
			live, e = tx.SourceRevision.Query().Where(sourcerevision.SourceBlobIDEQ(candidate.SourceBlobID), sourcerevision.IDNEQ(candidate.ID), sourcerevision.DeletedEQ(false)).Exist(ctx)
			if e != nil || live {
				return e
			}
			if e = tx.SourceRevision.UpdateOneID(candidate.ID).SetDeleted(true).Exec(ctx); e != nil {
				return e
			}
			if e = tx.Blob.UpdateOneID(candidate.SourceBlobID).SetStatus(blob.StatusDeletePending).ClearActiveLocationID().Exec(ctx); e != nil {
				return e
			}
			locations, e := tx.BlobLocation.Query().Where(bloblocation.BlobIDEQ(candidate.SourceBlobID), bloblocation.StatusNotIn(bloblocation.StatusDeleted, bloblocation.StatusDeleting)).All(ctx)
			if e != nil {
				return e
			}
			for _, location := range locations {
				if e = s.retireLocation(ctx, tx, location.ID, p.ID); e != nil {
					return e
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}
