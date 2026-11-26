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
	if _, err := s.GetResourceIdentity(ctx, actor, projectID, resourceID); err != nil {
		return nil, err
	}
	q := s.client.ExportArtifact.Query().Where(exportartifact.ProjectIDEQ(projectID), exportartifact.ResourceIDEQ(resourceID)).Select(exportartifact.FieldID, exportartifact.FieldProjectID, exportartifact.FieldResourceID, exportartifact.FieldSourceRevisionID, exportartifact.FieldSnapshotBlobID, exportartifact.FieldStatus, exportartifact.FieldRendererVersion, exportartifact.FieldFilename, exportartifact.FieldDeletionTaskID).Order(ent.Desc(exportartifact.FieldID)).Limit(100)
	if !includeDeleted {
		q.Where(exportartifact.StatusNEQ(exportartifact.StatusDeleted))
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.populateExportRebuildable(ctx, rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// populateExportRebuildable observes only metadata required by this response.
// Actual rebuild execution retains its own fresh validation.
func (s *ResourceService) populateExportRebuildable(ctx context.Context, rows []*ent.ExportArtifact) error {
	revisionIDs := make([]int, 0, len(rows))
	for _, row := range rows {
		row.Rebuildable = false
		if row.Status != exportartifact.StatusDeleted && row.SnapshotBlobID != nil && row.RendererVersion == storageRendererVersion {
			revisionIDs = append(revisionIDs, row.SourceRevisionID)
		}
	}
	if len(revisionIDs) == 0 {
		return nil
	}
	revisions, err := s.client.SourceRevision.Query().Where(sourcerevision.IDIn(revisionIDs...), sourcerevision.DeletedEQ(false)).Select(sourcerevision.FieldID, sourcerevision.FieldProjectID, sourcerevision.FieldResourceID, sourcerevision.FieldSourceBlobID).All(ctx)
	if err != nil {
		return err
	}
	byID := make(map[int]*ent.SourceRevision, len(revisions))
	for _, row := range revisions {
		byID[row.ID] = row
	}
	valid := make(map[int]*ent.SourceRevision, len(rows))
	blobIDs := make([]int, 0, 2*len(rows))
	for _, row := range rows {
		rev := byID[row.SourceRevisionID]
		if row.Status == exportartifact.StatusDeleted || row.SnapshotBlobID == nil || row.RendererVersion != storageRendererVersion || rev == nil || rev.ProjectID != row.ProjectID || rev.ResourceID != row.ResourceID {
			continue
		}
		valid[row.ID] = rev
		blobIDs = append(blobIDs, *row.SnapshotBlobID, rev.SourceBlobID)
	}
	if len(blobIDs) == 0 {
		return nil
	}
	readyIDs, err := s.client.Blob.Query().Where(blob.IDIn(blobIDs...), blob.StatusEQ(blob.StatusReady), blob.ActiveLocationIDNotNil()).IDs(ctx)
	if err != nil {
		return err
	}
	ready := make(map[int]bool, len(readyIDs))
	for _, id := range readyIDs {
		ready[id] = true
	}
	for _, row := range rows {
		if rev := valid[row.ID]; rev != nil {
			row.Rebuildable = ready[*row.SnapshotBlobID] && ready[rev.SourceBlobID]
		}
	}
	return nil
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
