package service

import (
	"context"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/blob"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/bloblocation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/project"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagemigrationitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagereservation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagewrite"
)

func (s *StorageService) StartMigration(ctx context.Context, actor, projectID, target int, generation int64, key string) (*ent.StorageTask, error) {
	task, err := s.Begin(ctx, actor, projectID, StorageIntent{Kind: "migration", IdempotencyKey: key, TargetSpaceID: target, StorageGeneration: generation})
	if err != nil {
		return nil, err
	}
	if task.Phase != "accepted" {
		return task, nil
	}
	if err = storageTerminalError(task); err != nil {
		return nil, err
	}
	err = withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		p, e := tx.Project.Get(ctx, projectID)
		if e != nil {
			return e
		}
		size, e := storageMigrationSize(ctx, tx, projectID)
		if e != nil {
			return e
		}
		if e = s.validateStorageTarget(ctx, tx, p, target, size, false); e != nil {
			return e
		}
		n, e := tx.Project.Update().Where(project.IDEQ(projectID), project.StorageGenerationEQ(generation), project.StorageStateEQ("active")).SetStorageState("draining").SetStorageMigrationTaskID(task.ID).AddStorageGeneration(1).Save(ctx)
		if e != nil {
			return e
		}
		if n != 1 {
			return ErrStorageConflict
		}
		if e = storageTaskGate(ctx, tx, task); e != nil {
			return e
		}
		rows, e := tx.Blob.Query().Where(blob.ProjectIDEQ(projectID), blob.StatusEQ(blob.StatusReady)).All(ctx)
		if e != nil {
			return e
		}
		for _, b := range rows {
			if b.ActiveLocationID == nil || b.Size == nil || b.Sha256 == nil {
				return ErrRepairMismatch
			}
			if e = tx.StorageMigrationItem.Create().SetTaskID(task.ID).SetBlobID(b.ID).SetSourceLocationID(*b.ActiveLocationID).SetExpectedLocationGeneration(b.LocationGeneration).Exec(ctx); e != nil {
				return e
			}
		}
		return tx.StorageTask.UpdateOneID(task.ID).SetExpectedStorageGeneration(generation + 1).SetPhase("draining").Exec(ctx)
	})
	if err != nil {
		return nil, err
	}
	return s.client.StorageTask.Get(ctx, task.ID)
}

func (s *StorageService) ContinueMigration(ctx context.Context, id int) error {
	task, err := s.client.StorageTask.Get(ctx, id)
	if err != nil {
		return err
	}
	if task.Kind != "migration" {
		return ErrInvalidInput
	}
	if task.Phase == "committed" {
		return nil
	}
	if task.Status == storagetask.StatusCancelled {
		return s.cancelMigration(ctx, task)
	}
	if task.Phase == "draining" {
		writes, e := s.client.StorageWrite.Query().Where(storagewrite.HasTaskWith(storagetask.ProjectIDEQ(task.ProjectID), storagetask.IDNEQ(task.ID), storagetask.Not(storagetask.And(storagetask.KindEQ("repair"), storagetask.ExpectedStorageGenerationEQ(task.ExpectedStorageGeneration)))), storagewrite.PhaseNotIn("committed", "cleaned")).All(ctx)
		if e != nil {
			return e
		}
		for _, w := range writes {
			if _, e = s.client.StorageTask.Update().Where(storagetask.IDEQ(w.TaskID), storagetask.PhaseNEQ("committed"), storagetask.StatusNEQ(storagetask.StatusCompleted)).SetStatus(storagetask.StatusCancelled).Save(ctx); e != nil {
				return e
			}
		}
		if len(writes) > 0 {
			return ErrStorageMaintenance
		}
		err = withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
			if e := storageProjectGate(ctx, tx, task.ProjectID, task.ExpectedStorageGeneration); e != nil {
				return e
			}
			if e := storageTaskGate(ctx, tx, task); e != nil {
				return e
			}
			rows, e := tx.Blob.Query().Where(blob.ProjectIDEQ(task.ProjectID), blob.StatusEQ(blob.StatusReady)).All(ctx)
			if e != nil {
				return e
			}
			for _, b := range rows {
				if b.ActiveLocationID == nil || b.Size == nil || b.Sha256 == nil {
					return ErrRepairMismatch
				}
				exists, e := tx.StorageMigrationItem.Query().Where(storagemigrationitem.TaskIDEQ(id), storagemigrationitem.BlobIDEQ(b.ID)).Exist(ctx)
				if e != nil {
					return e
				}
				if exists {
					continue
				}
				if _, e = tx.StorageMigrationItem.Create().SetTaskID(id).SetBlobID(b.ID).SetSourceLocationID(*b.ActiveLocationID).SetExpectedLocationGeneration(b.LocationGeneration).Save(ctx); e != nil {
					return e
				}
			}
			if e = tx.Project.UpdateOneID(task.ProjectID).SetStorageState("migrating").Exec(ctx); e != nil {
				return e
			}
			return tx.StorageTask.UpdateOneID(id).SetPhase("copy").SetStatus(storagetask.StatusRunning).Exec(ctx)
		})
		if err != nil {
			return err
		}
		task.Phase = "copy"
		task.Status = storagetask.StatusRunning
	}
	if task.Phase == "copy" || task.Phase == "verify" {
		items, e := s.client.StorageMigrationItem.Query().Where(storagemigrationitem.TaskIDEQ(id), storagemigrationitem.StatusEQ(storagemigrationitem.StatusPending)).Order(ent.Asc(storagemigrationitem.FieldID)).Limit(s.cfg.ReconcileBatchSize).All(ctx)
		if e != nil {
			return e
		}
		for _, item := range items {
			current, e := s.client.StorageTask.Get(ctx, id)
			if e != nil {
				return e
			}
			if current.Status == storagetask.StatusCancelled {
				return s.cancelMigration(ctx, current)
			}
			b, e := s.client.Blob.Get(ctx, item.BlobID)
			if e != nil {
				return e
			}
			if b.LocationGeneration != item.ExpectedLocationGeneration {
				return ErrStorageConflict
			}
			w, e := s.client.StorageWrite.Query().Where(storagewrite.TaskIDEQ(id), storagewrite.PhaseEQ("prepared"), storagewrite.LocationIDIsNil(), storagewrite.Sha256EQ(*b.Sha256), storagewrite.ActualBytesEQ(*b.Size)).Order(ent.Asc(storagewrite.FieldID)).First(ctx)
			var staged *StagedObject
			if ent.IsNotFound(e) {
				f, readErr := s.readBlob(ctx, b.ID)
				if readErr != nil {
					return readErr
				}
				staged, e = s.Stage(ctx, current, f, *b.Size)
				closeErr := f.Close()
				if e != nil {
					return e
				}
				if closeErr != nil {
					_ = staged.Close()
					return closeErr
				}
				w = staged.Write
			} else if e != nil {
				return e
			}
			if w.Sha256 != *b.Sha256 {
				if staged != nil {
					_ = staged.Close()
				}
				return ErrRepairMismatch
			}
			e = withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
				if e := storageProjectGate(ctx, tx, task.ProjectID, task.ExpectedStorageGeneration); e != nil {
					return e
				}
				t, e := tx.StorageTask.Get(ctx, id)
				if e != nil {
					return e
				}
				if t.Status == storagetask.StatusCancelled {
					return ErrStorageCancelled
				}
				if e = storageTaskGate(ctx, tx, t); e != nil {
					return e
				}
				if t.Phase != "copy" && t.Phase != "verify" {
					return ErrStorageConflict
				}
				currentItem, e := tx.StorageMigrationItem.Get(ctx, item.ID)
				if e != nil {
					return e
				}
				if currentItem.Status != storagemigrationitem.StatusPending {
					return ErrStorageConflict
				}
				n, e := tx.StorageWrite.Update().Where(storagewrite.IDEQ(w.ID), storagewrite.PhaseEQ("prepared"), storagewrite.LocationIDIsNil()).SetPhase("prepared").Save(ctx)
				if e != nil {
					return e
				}
				if n != 1 {
					return ErrStorageConflict
				}
				l, e := tx.BlobLocation.Create().SetBlobID(b.ID).SetSpaceID(*task.TargetSpaceID).SetObjectKey(w.ObjectKey).SetProviderVersion(w.ProviderVersion).SetSize(*b.Size).SetIntegrity(bloblocation.IntegrityAvailable).Save(ctx)
				if e != nil {
					return e
				}
				if e = tx.StorageWrite.UpdateOneID(w.ID).SetLocationID(l.ID).Exec(ctx); e != nil {
					return e
				}
				return tx.StorageMigrationItem.UpdateOneID(item.ID).SetTargetLocationID(l.ID).SetStatus(storagemigrationitem.StatusVerified).Exec(ctx)
			})
			if staged != nil {
				_ = staged.Close()
			}
			if e != nil {
				return e
			}
		}
		ready := false
		err = withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
			if e := storageProjectGate(ctx, tx, task.ProjectID, task.ExpectedStorageGeneration); e != nil {
				return e
			}
			t, e := tx.StorageTask.Get(ctx, id)
			if e != nil {
				return e
			}
			if e = storageTaskGate(ctx, tx, t); e != nil {
				return e
			}
			if t.Phase != "copy" && t.Phase != "verify" {
				return ErrStorageConflict
			}
			pending, e := tx.StorageMigrationItem.Query().Where(storagemigrationitem.TaskIDEQ(id), storagemigrationitem.StatusNotIn(storagemigrationitem.StatusVerified, storagemigrationitem.StatusCommitted)).Exist(ctx)
			if e != nil || pending {
				return e
			}
			if e = tx.StorageTask.UpdateOneID(id).SetPhase("cutover").ClearDeadline().Exec(ctx); e != nil {
				return e
			}
			ready = true
			return nil
		})
		if err != nil {
			return err
		}
		if !ready {
			return nil
		}
		task.Phase = "cutover"
	}
	if task.Phase == "cutover" {
		items, e := s.client.StorageMigrationItem.Query().Where(storagemigrationitem.TaskIDEQ(id), storagemigrationitem.StatusNEQ(storagemigrationitem.StatusCommitted)).Order(ent.Asc(storagemigrationitem.FieldID)).Limit(s.cfg.ReconcileBatchSize).All(ctx)
		if e != nil {
			return e
		}
		for _, item := range items {
			if item.Status != storagemigrationitem.StatusVerified || item.TargetLocationID == nil {
				return ErrStorageConflict
			}
			e = withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
				if e := storageProjectGate(ctx, tx, task.ProjectID, task.ExpectedStorageGeneration); e != nil {
					return e
				}
				t, e := tx.StorageTask.Get(ctx, id)
				if e != nil {
					return e
				}
				if t.Phase != "cutover" {
					return ErrStorageConflict
				}
				if e = storageTaskGate(ctx, tx, t); e != nil {
					return e
				}
				b, e := tx.Blob.Get(ctx, item.BlobID)
				if e != nil {
					return e
				}
				l, e := tx.BlobLocation.Get(ctx, *item.TargetLocationID)
				if e != nil {
					return e
				}
				if b.LocationGeneration != item.ExpectedLocationGeneration {
					return ErrStorageConflict
				}
				if l.Status != bloblocation.StatusCandidate {
					return ErrStorageConflict
				}
				w, e := tx.StorageWrite.Query().Where(storagewrite.LocationIDEQ(l.ID)).Only(ctx)
				if e != nil {
					return e
				}
				if e = s.validateWriteTarget(ctx, tx, w); e != nil {
					return e
				}
				claimed, e := tx.StorageWrite.Update().Where(storagewrite.IDEQ(w.ID), storagewrite.PhaseEQ("prepared")).SetPhase("publishing").Save(ctx)
				if e != nil {
					return e
				}
				if claimed != 1 {
					return ErrStorageConflict
				}
				n, e := tx.Blob.Update().Where(blob.IDEQ(b.ID), blob.LocationGenerationEQ(item.ExpectedLocationGeneration)).SetActiveLocationID(l.ID).AddLocationGeneration(1).Save(ctx)
				if e != nil {
					return e
				}
				if n != 1 {
					return ErrStorageConflict
				}
				if e = tx.BlobLocation.UpdateOneID(l.ID).SetStatus(bloblocation.StatusLive).Exec(ctx); e != nil {
					return e
				}
				if e = s.retireLocation(ctx, tx, item.SourceLocationID, task.ProjectID); e != nil {
					return e
				}
				if e = tx.StorageWrite.UpdateOneID(w.ID).SetPhase("committed").Exec(ctx); e != nil {
					return e
				}
				if _, e = tx.StorageReservation.Update().Where(storagereservation.WriteIDEQ(w.ID)).SetState(storagereservation.StateLive).Save(ctx); e != nil {
					return e
				}
				if e = tx.StorageSpace.UpdateOneID(l.SpaceID).AddCandidateBytes(-l.Size).AddLiveBytes(l.Size).Exec(ctx); e != nil {
					return e
				}
				return tx.StorageMigrationItem.UpdateOneID(item.ID).SetStatus(storagemigrationitem.StatusCommitted).Exec(ctx)
			})
			if e != nil {
				return e
			}
		}
		return withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
			if e := storageProjectGate(ctx, tx, task.ProjectID, task.ExpectedStorageGeneration); e != nil {
				return e
			}
			pending, e := tx.StorageMigrationItem.Query().Where(storagemigrationitem.TaskIDEQ(id), storagemigrationitem.StatusNEQ(storagemigrationitem.StatusCommitted)).Exist(ctx)
			if e != nil {
				return e
			}
			if pending {
				return nil
			}
			p, e := tx.Project.Get(ctx, task.ProjectID)
			if e != nil {
				return e
			}
			if e = s.validateStorageTarget(ctx, tx, p, *task.TargetSpaceID, 0, false); e != nil {
				return e
			}
			if e = tx.Project.UpdateOneID(task.ProjectID).SetStorageSpaceID(*task.TargetSpaceID).SetStorageState("active").ClearStorageMigrationTaskID().AddStorageGeneration(1).Exec(ctx); e != nil {
				return e
			}
			if e = tx.StorageTask.UpdateOneID(id).SetCleanupStatus(storagetask.CleanupStatusCleanupPending).Exec(ctx); e != nil {
				return e
			}
			return s.finishTask(ctx, tx, id)
		})
	}
	return nil
}

func (s *StorageService) validateWriteTarget(ctx context.Context, tx *ent.Client, w *ent.StorageWrite) error {
	sp, err := tx.StorageSpace.Get(ctx, w.SpaceID)
	if err != nil {
		return err
	}
	c, err := tx.StorageConnection.Get(ctx, sp.ConnectionID)
	if err != nil {
		return err
	}
	if sp.ManagementGeneration != w.SpaceGeneration || c.ManagementGeneration != w.ConnectionGeneration || string(sp.Status) != "active" || string(c.Status) != "enabled" {
		return ErrStorageConflict
	}
	return nil
}

func (s *StorageService) cancelMigration(ctx context.Context, task *ent.StorageTask) error {
	return withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		p, e := tx.Project.Get(ctx, task.ProjectID)
		if e != nil {
			return e
		}
		if e = storageProjectGate(ctx, tx, p.ID, p.StorageGeneration); e != nil {
			return e
		}
		t, e := tx.StorageTask.Get(ctx, task.ID)
		if e != nil {
			return e
		}
		if t.Phase == "cutover" || t.Phase == "cleanup" || t.Phase == "committed" {
			return ErrStorageConflict
		}
		n, e := tx.StorageTask.Update().Where(storagetask.IDEQ(t.ID), storagetask.PhaseEQ(t.Phase)).SetStatus(storagetask.StatusCancelled).SetCleanupStatus(storagetask.CleanupStatusCleanupPending).Save(ctx)
		if e != nil {
			return e
		}
		if n != 1 {
			return ErrStorageConflict
		}
		if _, e := tx.StorageWrite.Update().Where(storagewrite.TaskIDEQ(task.ID), storagewrite.PhaseNotIn("committed", "cleaned", "reconciling")).SetPhase("reconcile").Save(ctx); e != nil {
			return e
		}
		if _, e := tx.Project.Update().Where(project.IDEQ(task.ProjectID), project.StorageMigrationTaskIDEQ(task.ID)).SetStorageState("active").ClearStorageMigrationTaskID().AddStorageGeneration(1).Save(ctx); e != nil {
			return e
		}
		return nil
	})
}

func (s *StorageService) reconcileMigrationRepair(ctx context.Context, tx *ent.Client, p *ent.Project, b *ent.Blob, spaceID int) error {
	if p.StorageMigrationTaskID == nil || p.StorageState == "active" {
		return nil
	}
	task, err := tx.StorageTask.Get(ctx, *p.StorageMigrationTaskID)
	if err != nil {
		return err
	}
	if task.TargetSpaceID == nil || *task.TargetSpaceID != spaceID {
		return ErrStoragePolicy
	}
	if b.ActiveLocationID == nil {
		return ErrStorageConflict
	}
	item, err := tx.StorageMigrationItem.Query().Where(storagemigrationitem.TaskIDEQ(task.ID), storagemigrationitem.BlobIDEQ(b.ID)).Only(ctx)
	if err != nil {
		return err
	}
	if item.TargetLocationID != nil && *item.TargetLocationID != *b.ActiveLocationID {
		if _, err = tx.StorageWrite.Update().Where(storagewrite.LocationIDEQ(*item.TargetLocationID), storagewrite.PhaseEQ("prepared")).SetPhase("reconcile").Save(ctx); err != nil {
			return err
		}
	}
	return tx.StorageMigrationItem.UpdateOneID(item.ID).SetTargetLocationID(*b.ActiveLocationID).SetExpectedLocationGeneration(b.LocationGeneration).SetStatus(storagemigrationitem.StatusCommitted).Exec(ctx)
}
