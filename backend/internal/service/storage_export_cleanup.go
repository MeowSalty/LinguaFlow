package service

import (
	"context"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/blob"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/deletionentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/exportartifact"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/sourcerevision"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
)

// ExportRebuildable checks known metadata only; actual object I/O is deferred to execution.
func (s *ResourceService) ExportRebuildable(ctx context.Context, a *ent.ExportArtifact) (bool, error) {
	return exportRebuildable(ctx, s.client, a)
}

func exportRebuildable(ctx context.Context, client *ent.Client, a *ent.ExportArtifact) (bool, error) {
	if a == nil || a.Status == exportartifact.StatusDeleted || a.SnapshotBlobID == nil || a.RendererVersion != storageRendererVersion {
		return false, nil
	}
	revision, err := client.SourceRevision.Query().Where(sourcerevision.IDEQ(a.SourceRevisionID), sourcerevision.ProjectIDEQ(a.ProjectID), sourcerevision.ResourceIDEQ(a.ResourceID), sourcerevision.DeletedEQ(false)).Only(ctx)
	if ent.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	count, err := client.Blob.Query().Where(blob.IDIn(*a.SnapshotBlobID, revision.SourceBlobID), blob.StatusEQ(blob.StatusReady), blob.ActiveLocationIDNotNil()).Count(ctx)
	if err != nil {
		return false, err
	}
	expected := 2
	if *a.SnapshotBlobID == revision.SourceBlobID {
		expected = 1
	}
	return count == expected, nil
}

func (s *ResourceService) ListExportsIncludingDeleted(ctx context.Context, actor, projectID, resourceID int, includeDeleted bool) ([]*ent.ExportArtifact, error) {
	if _, err := s.GetResource(ctx, actor, projectID, resourceID); err != nil {
		return nil, err
	}
	q := s.client.ExportArtifact.Query().Where(exportartifact.ProjectIDEQ(projectID), exportartifact.ResourceIDEQ(resourceID)).Order(ent.Desc(exportartifact.FieldID)).Limit(100)
	if !includeDeleted {
		q.Where(exportartifact.StatusNEQ(exportartifact.StatusDeleted))
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range rows {
		a.Rebuildable, err = s.ExportRebuildable(ctx, a)
		if err != nil {
			return nil, err
		}
	}
	return rows, nil
}

func (s *StorageService) refreshDeletionTaskCleanup(ctx context.Context) error {
	tasks, err := s.client.StorageTask.Query().Where(storagetask.KindEQ("export_delete"), storagetask.StatusEQ(storagetask.StatusCompleted), storagetask.CleanupStatusNEQ(storagetask.CleanupStatusDone)).Order(ent.Asc(storagetask.FieldUpdatedAt)).Limit(s.cfg.ReconcileBatchSize).All(ctx)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		entries, e := s.client.DeletionEntry.Query().Where(deletionentry.DeletionTaskIDEQ(task.ID)).All(ctx)
		if e != nil {
			return e
		}
		status := storagetask.CleanupStatusDone
		code := ""
		for _, entry := range entries {
			switch entry.Status {
			case deletionentry.StatusBlocked:
				status = storagetask.CleanupStatusBlocked
				code = entry.ErrorCode
			case deletionentry.StatusRunning:
				if status != storagetask.CleanupStatusBlocked {
					status = storagetask.CleanupStatusRunning
				}
			case deletionentry.StatusPending:
				if status == storagetask.CleanupStatusDone {
					status = storagetask.CleanupStatusCleanupPending
				}
			}
		}
		if e = s.client.StorageTask.UpdateOneID(task.ID).SetCleanupStatus(status).SetErrorCode(code).Exec(ctx); e != nil {
			return e
		}
	}
	return nil
}
