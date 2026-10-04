package service

import (
	"context"
	"encoding/json"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
)

func (s *ResourceService) findExportReplay(ctx context.Context, actor, projectID, resourceID, artifactID int, key string) (*ent.StorageTask, error) {
	if !StorageKeyValid(key) {
		return nil, ErrInvalidInput
	}
	task, err := s.client.StorageTask.Query().Where(storagetask.ActorIDEQ(actor), storagetask.ProjectIDEQ(projectID), storagetask.KindEQ("export"), storagetask.IdempotencyKeyEQ(key)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var intent StorageIntent
	data, err := json.Marshal(task.Input)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(data, &intent); err != nil {
		return nil, err
	}
	if intent.ArtifactID != artifactID || (artifactID == 0 && (task.ResourceID == nil || *task.ResourceID != resourceID)) {
		return nil, ErrStorageIdempotency
	}
	if err = storageTerminalError(task); err != nil {
		return task, err
	}
	return task, nil
}

func (s *ResourceService) freezeExportSnapshot(ctx context.Context, actor int, task *ent.StorageTask) (json.RawMessage, error) {
	if len(task.SourcePlan) > 0 {
		return task.SourcePlan, nil
	}
	if task.ResourceID == nil {
		return nil, ErrInvalidInput
	}
	snapshot, err := s.captureSnapshot(ctx, actor, task.ProjectID, *task.ResourceID)
	if err != nil {
		return nil, err
	}
	if snapshot.RevisionID == nil || task.SourceRevisionID == nil || *snapshot.RevisionID != *task.SourceRevisionID || snapshot.SourceGeneration != task.ExpectedSourceGeneration || snapshot.TranslationGeneration != task.ExpectedTranslationGeneration {
		return nil, ErrSourceRevisionConflict
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > s.storage.cfg.Limits.MaxMetadataBytes {
		return nil, ErrStorageTooLarge
	}
	err = withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		current, e := tx.StorageTask.Get(ctx, task.ID)
		if e != nil {
			return e
		}
		if e = storageTaskGate(ctx, tx, current); e != nil {
			return e
		}
		if len(current.SourcePlan) > 0 {
			data = current.SourcePlan
			return nil
		}
		return tx.StorageTask.UpdateOneID(task.ID).SetSourcePlan(data).Exec(ctx)
	})
	return data, err
}
