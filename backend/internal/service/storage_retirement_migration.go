package service

import (
	"context"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/blob"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/project"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storageguard"
)

// prepareMigration accepts the existing project migration protocol inside the
// same transaction that associates it with its retirement parent.
func (s *StorageService) prepareMigration(ctx context.Context, tx *ent.Client, p *ent.Project, task *ent.StorageTask, target int, generation int64) error {
	if err := storageguard.Gate(ctx, tx, target, storageguard.NewUse); err != nil {
		return err
	}
	size, err := storageMigrationSize(ctx, tx, p.ID)
	if err != nil {
		return err
	}
	if err = s.validateStorageTarget(ctx, tx, p, target, size, false); err != nil {
		return err
	}
	n, err := tx.Project.Update().Where(project.IDEQ(p.ID), project.StorageGenerationEQ(generation), project.StorageStateEQ("active")).SetStorageState("draining").SetStorageMigrationTaskID(task.ID).AddStorageGeneration(1).Save(ctx)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrStorageConflict
	}
	if err = storageTaskGate(ctx, tx, task); err != nil {
		return err
	}
	rows, err := tx.Blob.Query().Where(blob.ProjectIDEQ(p.ID), blob.StatusEQ(blob.StatusReady)).All(ctx)
	if err != nil {
		return err
	}
	for _, b := range rows {
		if b.ActiveLocationID == nil || b.Size == nil || b.Sha256 == nil {
			return ErrRepairMismatch
		}
		if err = tx.StorageMigrationItem.Create().SetTaskID(task.ID).SetBlobID(b.ID).SetSourceLocationID(*b.ActiveLocationID).SetExpectedLocationGeneration(b.LocationGeneration).Exec(ctx); err != nil {
			return err
		}
	}
	return tx.StorageTask.UpdateOneID(task.ID).SetExpectedStorageGeneration(generation + 1).SetPhase("draining").Exec(ctx)
}
