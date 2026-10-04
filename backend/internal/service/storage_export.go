package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/blob"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/deletionentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/exportartifact"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagewrite"
)

const storageRendererVersion = "1"

func (s *ResourceService) CreateExport(ctx context.Context, actor, projectID, resourceID int, key string) (*ent.StorageTask, error) {
	ctx, cancel := context.WithTimeout(ctx, s.storage.cfg.TransferTimeout)
	defer cancel()
	if _, err := s.projects.requireProjectAccess(ctx, actor, projectID, true); err != nil {
		return nil, err
	}
	task, err := s.findExportReplay(ctx, actor, projectID, resourceID, 0, key)
	if err != nil {
		return nil, err
	}
	if task != nil && task.ResultArtifactID != nil {
		return task, nil
	}
	if task == nil {
		res, err := s.GetResource(ctx, actor, projectID, resourceID)
		if err != nil {
			return nil, err
		}
		if res.Resource.CurrentSourceRevisionID == nil {
			return nil, ErrRepairMismatch
		}
		task, err = s.storage.Begin(ctx, actor, projectID, StorageIntent{Kind: "export", IdempotencyKey: key, ResourceID: resourceID, SourceRevisionID: *res.Resource.CurrentSourceRevisionID, SourceGeneration: res.Resource.SourceGeneration, TranslationGeneration: res.Resource.TranslationGeneration})
		if err != nil {
			return nil, err
		}
		if task.ResultArtifactID != nil {
			return task, nil
		}
	}
	release, err := s.storage.ClaimTaskExecution(ctx, task.ID)
	if err != nil {
		return task, err
	}
	defer release()
	task, err = s.client.StorageTask.Get(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	if task.ResultArtifactID != nil {
		return task, nil
	}
	if err = storageTerminalError(task); err != nil {
		return task, err
	}
	prepared, err := s.client.StorageWrite.Query().Where(storagewrite.TaskIDEQ(task.ID), storagewrite.PhaseEQ("prepared")).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if !prepared {
		data, err := s.freezeExportSnapshot(ctx, actor, task)
		if err != nil {
			return task, err
		}
		if int64(len(data)) > s.storage.cfg.Limits.MaxMetadataBytes {
			return nil, ErrStorageTooLarge
		}
		stage, err := s.storage.Stage(ctx, task, bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, err
		}
		if err = stage.Close(); err != nil {
			return nil, err
		}
	}
	return s.prepareExportArtifact(ctx, task)
}

// prepared 状态的快照足以支持进程在 artifact 事务之前退出后恢复。
// 其字节内容永远不会从不断变化的译文中重新截取。
func (s *ResourceService) prepareExportArtifact(ctx context.Context, task *ent.StorageTask) (*ent.StorageTask, error) {
	task, err := s.client.StorageTask.Get(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	if task.ResultArtifactID != nil {
		return task, nil
	}
	w, err := s.client.StorageWrite.Query().Where(storagewrite.TaskIDEQ(task.ID), storagewrite.PhaseEQ("prepared")).Only(ctx)
	if ent.IsNotFound(err) && len(task.SourcePlan) > 0 {
		stage, e := s.storage.Stage(ctx, task, bytes.NewReader(task.SourcePlan), int64(len(task.SourcePlan)))
		if e != nil {
			return nil, e
		}
		w = stage.Write
		err = stage.Close()
	}
	if err != nil {
		return nil, err
	}
	f, err := s.storage.materialize(ctx, w.SpaceID, w.ObjectKey, w.ProviderVersion, w.ActualBytes, w.Sha256)
	if err != nil {
		return nil, err
	}
	snapshot, err := s.decodeExportSnapshot(f)
	closeErr := f.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if snapshot.RevisionID == nil || task.SourceRevisionID == nil || *snapshot.RevisionID != *task.SourceRevisionID || task.ResourceID == nil || snapshot.ResourceID != *task.ResourceID || snapshot.ProjectID != task.ProjectID || snapshot.SourceGeneration != task.ExpectedSourceGeneration || snapshot.TranslationGeneration != task.ExpectedTranslationGeneration {
		return nil, ErrStorageConflict
	}
	err = withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		if e := storageProjectGate(ctx, tx, task.ProjectID, task.ExpectedStorageGeneration); e != nil {
			return e
		}
		res, e := tx.Resource.Query().Where(resource.IDEQ(snapshot.ResourceID), resource.ProjectIDEQ(task.ProjectID)).Only(ctx)
		if e != nil {
			return e
		}
		p, e := tx.Project.Get(ctx, task.ProjectID)
		if e != nil {
			return e
		}
		if e = s.storage.logicalAdmission(ctx, tx, p, w.ActualBytes); e != nil {
			return e
		}
		b, e := s.storage.publish(ctx, tx, w, blob.PurposeSnapshot, nil)
		if e != nil {
			return e
		}
		artifact, e := tx.ExportArtifact.Create().SetProjectID(task.ProjectID).SetResourceID(snapshot.ResourceID).SetSourceRevisionID(*snapshot.RevisionID).SetSnapshotBlobID(b.ID).SetRendererVersion(storageRendererVersion).SetSourceGeneration(snapshot.SourceGeneration).SetTranslationGeneration(snapshot.TranslationGeneration).SetOutputGeneration(snapshot.OutputGeneration).SetFilename(filepath.Base(res.Path)).Save(ctx)
		if e != nil {
			return e
		}
		n, e := tx.StorageTask.Update().Where(storagetask.IDEQ(task.ID), storagetask.ResultArtifactIDIsNil(), storagetask.StatusNEQ(storagetask.StatusCancelled)).SetResultArtifactID(artifact.ID).SetPhase("rendering").SetStatus(storagetask.StatusPending).Save(ctx)
		if e != nil {
			return e
		}
		if n != 1 {
			return ErrStorageConflict
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.client.StorageTask.Get(ctx, task.ID)
}

func (s *ResourceService) decodeExportSnapshot(f *StorageFile) (*resourceSnapshot, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > s.storage.cfg.Limits.MaxMetadataBytes {
		return nil, ErrStorageTooLarge
	}
	var snapshot resourceSnapshot
	d := json.NewDecoder(io.LimitReader(f, s.storage.cfg.Limits.MaxMetadataBytes+1))
	if err = d.Decode(&snapshot); err != nil {
		return nil, err
	}
	if err = d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, ErrRepairMismatch
	}
	return &snapshot, nil
}

func (s *ResourceService) finishExport(ctx context.Context, task *ent.StorageTask) error {
	var err error
	if task.ResultArtifactID == nil {
		task, err = s.prepareExportArtifact(ctx, task)
		if err != nil {
			return err
		}
	}
	artifact, err := s.client.ExportArtifact.Get(ctx, *task.ResultArtifactID)
	if err != nil {
		return err
	}
	if artifact.Status == exportartifact.StatusReady {
		return nil
	}
	if artifact.Status == exportartifact.StatusDeleted {
		return ErrStorageCancelled
	}
	valid, err := s.ExportRebuildable(ctx, artifact)
	if err != nil {
		return err
	}
	if !valid {
		return ErrRepairMismatch
	}
	// 提交响应丢失或重启后，复用已完全校验的输出。
	w, err := s.client.StorageWrite.Query().Where(storagewrite.TaskIDEQ(task.ID), storagewrite.PhaseEQ("prepared")).Only(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return err
	}
	if w == nil {
		f, e := s.storage.readBlob(ctx, *artifact.SnapshotBlobID)
		if e != nil {
			return e
		}
		snapshot, e := s.decodeExportSnapshot(f)
		closeErr := f.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
		output, e := s.renderSnapshot(ctx, snapshot)
		if e != nil {
			return e
		}
		defer output.Close()
		info, e := output.Stat()
		if e != nil {
			return e
		}
		stage, e := s.storage.Stage(ctx, task, output, info.Size())
		if e != nil {
			return e
		}
		defer stage.Close()
		w = stage.Write
	} else {
		f, e := s.storage.materialize(ctx, w.SpaceID, w.ObjectKey, w.ProviderVersion, w.ActualBytes, w.Sha256)
		if e != nil {
			return e
		}
		if e = f.Close(); e != nil {
			return e
		}
	}
	return withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		if e := storageProjectGate(ctx, tx, task.ProjectID, task.ExpectedStorageGeneration); e != nil {
			return e
		}
		p, e := tx.Project.Get(ctx, task.ProjectID)
		if e != nil {
			return e
		}
		if e = s.storage.logicalAdmission(ctx, tx, p, w.ActualBytes); e != nil {
			return e
		}
		b, e := s.storage.publish(ctx, tx, w, blob.PurposeExport, nil)
		if e != nil {
			return e
		}
		n, e := tx.ExportArtifact.Update().Where(exportartifact.IDEQ(artifact.ID), exportartifact.StatusEQ(exportartifact.StatusPending)).SetOutputBlobID(b.ID).SetStatus(exportartifact.StatusReady).Save(ctx)
		if e != nil {
			return e
		}
		if n != 1 {
			return ErrStorageConflict
		}
		return s.storage.finishTask(ctx, tx, task.ID)
	})
}

func (s *ResourceService) DownloadExport(ctx context.Context, actor, projectID, artifactID int) (*StorageFile, string, error) {
	if _, err := s.projects.requireProjectAccess(ctx, actor, projectID, false); err != nil {
		return nil, "", err
	}
	a, err := s.client.ExportArtifact.Query().Where(exportartifact.IDEQ(artifactID), exportartifact.ProjectIDEQ(projectID), exportartifact.StatusEQ(exportartifact.StatusReady)).Only(ctx)
	if err != nil {
		return nil, "", err
	}
	if a.OutputBlobID == nil {
		return nil, "", ErrRepairMismatch
	}
	f, err := s.storage.readBlob(ctx, *a.OutputBlobID)
	return f, a.Filename, err
}

func (s *ResourceService) DeleteExport(ctx context.Context, actor, projectID, artifactID int) error {
	if _, err := s.projects.requireProjectAccess(ctx, actor, projectID, true); err != nil {
		return err
	}
	return withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		a, e := tx.ExportArtifact.Query().Where(exportartifact.IDEQ(artifactID), exportartifact.ProjectIDEQ(projectID)).Only(ctx)
		if e != nil {
			return e
		}
		if a.DeletionTaskID != nil {
			return nil
		}
		p, e := tx.Project.Get(ctx, projectID)
		if e != nil {
			return e
		}
		if p.StorageState != "active" {
			return ErrStorageMaintenance
		}
		if e = storageProjectGate(ctx, tx, projectID, p.StorageGeneration); e != nil {
			return e
		}
		key := fmt.Sprintf("export-delete-%d", a.ID)
		deletionTask, e := tx.StorageTask.Create().SetOperationID(generateUniqueID()).SetIdempotencyKey(key).SetRequestHash(key).SetActorID(actor).SetProjectID(projectID).SetResourceID(a.ResourceID).SetKind("export_delete").SetStatus(storagetask.StatusCompleted).SetPhase("committed").SetResultArtifactID(a.ID).SetCleanupStatus(storagetask.CleanupStatusCleanupPending).Save(ctx)
		if e != nil {
			return e
		}
		if e = tx.ExportArtifact.UpdateOneID(a.ID).SetStatus(exportartifact.StatusDeleted).SetDeletionTaskID(deletionTask.ID).Exec(ctx); e != nil {
			return e
		}
		if _, e = tx.StorageTask.Update().Where(storagetask.ResultArtifactIDEQ(a.ID), storagetask.PhaseNEQ("committed")).SetStatus(storagetask.StatusCancelled).SetCleanupStatus(storagetask.CleanupStatusCleanupPending).Save(ctx); e != nil {
			return e
		}
		for _, id := range []*int{a.OutputBlobID, a.SnapshotBlobID} {
			if id == nil {
				continue
			}
			referenced, e := tx.ExportArtifact.Query().Where(exportartifact.StatusNEQ(exportartifact.StatusDeleted), exportartifact.Or(exportartifact.SnapshotBlobIDEQ(*id), exportartifact.OutputBlobIDEQ(*id))).Exist(ctx)
			if e != nil {
				return e
			}
			if referenced {
				continue
			}
			b, e := tx.Blob.Get(ctx, *id)
			if e != nil {
				return e
			}
			if e = tx.Blob.UpdateOneID(b.ID).SetStatus(blob.StatusDeletePending).ClearActiveLocationID().Exec(ctx); e != nil {
				return e
			}
			if b.ActiveLocationID != nil {
				if e = s.storage.retireLocation(ctx, tx, *b.ActiveLocationID, projectID); e != nil {
					return e
				}
				if e = tx.DeletionEntry.Update().Where(deletionentry.LocationIDEQ(*b.ActiveLocationID), deletionentry.DeletionTaskIDIsNil()).SetDeletionTaskID(deletionTask.ID).Exec(ctx); e != nil {
					return e
				}
			}
		}
		pending, e := tx.DeletionEntry.Query().Where(deletionentry.DeletionTaskIDEQ(deletionTask.ID), deletionentry.StatusNEQ(deletionentry.StatusDone)).Exist(ctx)
		if e != nil {
			return e
		}
		if !pending {
			return tx.StorageTask.UpdateOneID(deletionTask.ID).SetCleanupStatus(storagetask.CleanupStatusDone).SetInput(map[string]any{"cleanup_reason": "no_objects"}).Exec(ctx)
		}
		return nil
	})
}

func (s *ResourceService) RebuildExport(ctx context.Context, actor, projectID, artifactID int, key string) (*ent.StorageTask, error) {
	ctx, cancel := context.WithTimeout(ctx, s.storage.cfg.TransferTimeout)
	defer cancel()
	if _, err := s.projects.requireProjectAccess(ctx, actor, projectID, true); err != nil {
		return nil, err
	}
	replay, err := s.findExportReplay(ctx, actor, projectID, 0, artifactID, key)
	if err != nil {
		return nil, err
	}
	if replay != nil && replay.ResultArtifactID != nil {
		return replay, nil
	}
	old, err := s.client.ExportArtifact.Query().Where(exportartifact.IDEQ(artifactID), exportartifact.ProjectIDEQ(projectID), exportartifact.StatusNEQ(exportartifact.StatusDeleted)).Only(ctx)
	if err != nil {
		return nil, err
	}
	valid, err := s.ExportRebuildable(ctx, old)
	if err != nil {
		return nil, err
	}
	if !valid {
		return nil, ErrRepairMismatch
	}
	task := replay
	if task == nil {
		task, err = s.storage.Begin(ctx, actor, projectID, StorageIntent{Kind: "export", ArtifactID: old.ID, IdempotencyKey: key, ResourceID: old.ResourceID, SourceRevisionID: old.SourceRevisionID, SourceGeneration: old.SourceGeneration, TranslationGeneration: old.TranslationGeneration})
	}
	if err != nil {
		return nil, err
	}
	if task.ResultArtifactID != nil {
		return task, nil
	}
	release, err := s.storage.ClaimTaskExecution(ctx, task.ID)
	if err != nil {
		return task, err
	}
	defer release()
	if err = storageTerminalError(task); err != nil {
		return task, err
	}
	err = withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		if e := storageProjectGate(ctx, tx, projectID, task.ExpectedStorageGeneration); e != nil {
			return e
		}
		current, e := tx.ExportArtifact.Get(ctx, old.ID)
		if e != nil {
			return e
		}
		if current.Status == exportartifact.StatusDeleted || current.SnapshotBlobID == nil || *current.SnapshotBlobID != *old.SnapshotBlobID {
			return ErrStorageConflict
		}
		valid, e := exportRebuildable(ctx, tx, current)
		if e != nil {
			return e
		}
		if !valid {
			return ErrRepairMismatch
		}
		a, e := tx.ExportArtifact.Create().SetProjectID(projectID).SetResourceID(old.ResourceID).SetSourceRevisionID(old.SourceRevisionID).SetSnapshotBlobID(*old.SnapshotBlobID).SetRendererVersion(old.RendererVersion).SetFilename(old.Filename).SetSourceGeneration(old.SourceGeneration).SetTranslationGeneration(old.TranslationGeneration).SetOutputGeneration(old.OutputGeneration).Save(ctx)
		if e != nil {
			return e
		}
		n, e := tx.StorageTask.Update().Where(storagetask.IDEQ(task.ID), storagetask.ResultArtifactIDIsNil(), storagetask.StatusNEQ(storagetask.StatusCancelled)).SetResultArtifactID(a.ID).SetPhase("rendering").Save(ctx)
		if e != nil {
			return e
		}
		if n != 1 {
			return ErrStorageConflict
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.client.StorageTask.Get(ctx, task.ID)
}
