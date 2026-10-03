package service

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/backuppin"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/blob"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/bloblocation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/deletionentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagemigrationitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagereservation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagewrite"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

func (s *StorageService) retireLocation(ctx context.Context, tx *ent.Client, id, projectID int) error {
	l, err := tx.BlobLocation.Get(ctx, id)
	if err != nil {
		return err
	}
	if l.Status == bloblocation.StatusDeleted || l.Status == bloblocation.StatusDeleting {
		return nil
	}
	until := time.Now().UTC().Add(s.deleteGrace)
	if l.RetainUntil != nil && l.RetainUntil.After(until) {
		until = *l.RetainUntil
	}
	n, err := tx.BlobLocation.Update().Where(bloblocation.IDEQ(id), bloblocation.StatusEQ(l.Status)).SetStatus(bloblocation.StatusRetired).SetRetainUntil(until).Save(ctx)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrStorageConflict
	}
	exists, err := tx.DeletionEntry.Query().Where(deletionentry.LocationIDEQ(id)).Exist(ctx)
	if err != nil {
		return err
	}
	if !exists {
		entry := tx.DeletionEntry.Create().SetLocationID(id).SetProjectID(projectID).SetNotBefore(until)
		if l.BlobID != nil {
			b, e := tx.Blob.Get(ctx, *l.BlobID)
			if e != nil {
				return e
			}
			entry.SetOwnerKind(string(b.OwnerKind)).SetOwnerID(b.OwnerID)
		}
		if err = entry.Exec(ctx); err != nil {
			return err
		}
	}
	if l.Status == bloblocation.StatusLive {
		if err = tx.StorageSpace.UpdateOneID(l.SpaceID).AddLiveBytes(-l.Size).AddPendingDeleteBytes(l.Size).Exec(ctx); err != nil {
			return err
		}
		_, err = tx.StorageReservation.Update().Where(storagereservation.HasWriteWith(storagewrite.LocationIDEQ(id)), storagereservation.StateEQ(storagereservation.StateLive)).SetState(storagereservation.StatePendingDelete).Save(ctx)
	} else if l.Status == bloblocation.StatusCandidate {
		if err = tx.StorageSpace.UpdateOneID(l.SpaceID).AddCandidateBytes(-l.Size).AddPendingDeleteBytes(l.Size).Exec(ctx); err != nil {
			return err
		}
		_, err = tx.StorageReservation.Update().Where(storagereservation.HasWriteWith(storagewrite.LocationIDEQ(id)), storagereservation.StateEQ(storagereservation.StateCandidate)).SetState(storagereservation.StatePendingDelete).Save(ctx)
	}
	return err
}

func (s *StorageService) failWrite(ctx context.Context, id int, cause error) error {
	return withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		w, e := tx.StorageWrite.Get(ctx, id)
		if e != nil {
			return e
		}
		if w.Phase == "committed" || w.Phase == "cleaned" {
			return nil
		}
		if e = tx.StorageWrite.UpdateOneID(id).SetPhase("reconcile").Exec(ctx); e != nil {
			return e
		}
		t, e := tx.StorageTask.Get(ctx, w.TaskID)
		if e != nil {
			return e
		}
		if t.Phase == "committed" {
			return nil
		}
		u := tx.StorageTask.UpdateOneID(t.ID).SetCleanupStatus(storagetask.CleanupStatusCleanupPending).SetErrorCode(storageCode(cause))
		if t.Status != storagetask.StatusCancelled {
			u.SetStatus(storagetask.StatusNeedsAction)
		}
		return u.Exec(ctx)
	})
}

// Reconcile 只触碰已持久化的确切 key；绝不通过列举存储桶来发现垃圾数据。
func (s *StorageService) Reconcile(ctx context.Context) error {
	if s.maintenance {
		return nil
	}
	if err := s.expireSourceRevisions(ctx); err != nil {
		return err
	}
	now := time.Now().UTC()
	rows, err := s.client.StorageWrite.Query().Where(storagewrite.PhaseNotIn("committed", "cleaned"), storagewrite.Or(storagewrite.PhaseNEQ("prepared"), storagewrite.HasTaskWith(storagetask.StatusIn(storagetask.StatusCancelled, storagetask.StatusFailed), storagetask.PhaseNotIn("cutover", "cleanup")), storagewrite.And(storagewrite.ExpiresAtLTE(now), storagewrite.HasTaskWith(storagetask.KindNEQ("migration"))))).Order(ent.Asc(storagewrite.FieldUpdatedAt)).Limit(s.cfg.ReconcileBatchSize).All(ctx)
	if err != nil {
		return err
	}
	for _, w := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		claimed, e := s.claimCleanup(ctx, w.ID)
		if e != nil {
			return e
		}
		if claimed == nil {
			continue
		}
		w = claimed
		e = s.cleanWrite(ctx, w)
		s.mu.Lock()
		delete(s.inFlight, w.ID)
		s.mu.Unlock()
		if e != nil {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.cfg.MetadataTimeout)
			_ = s.failWrite(cleanupCtx, w.ID, e)
			cancel()
		}
	}
	if err = s.collect(ctx); err != nil {
		return err
	}
	return s.refreshTaskCleanup(ctx)
}

// claimCleanup 在发起任何提供方请求之前先持久化排斥标记。发布方同样必须
// 把同一条 write 从 prepared 状态改走，因此重启后只有一方能够获胜。
func (s *StorageService) claimCleanup(ctx context.Context, id int) (*ent.StorageWrite, error) {
	s.transferMu.Lock()
	defer s.transferMu.Unlock()
	s.mu.Lock()
	busy := s.inFlight[id]
	s.mu.Unlock()
	if busy {
		return nil, nil
	}
	var claimed *ent.StorageWrite
	err := withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		w, e := tx.StorageWrite.Get(ctx, id)
		if e != nil {
			return e
		}
		if w.Phase == "committed" || w.Phase == "cleaned" {
			return nil
		}
		t, e := tx.StorageTask.Get(ctx, w.TaskID)
		if e != nil {
			return e
		}
		if t.Kind == "storage_probe" || t.Kind == "storage_marker" {
			return nil
		}
		// 不可逆切换期间，即使超过截止期限，迁移目的地仍然必要。
		// 普通的 prepared 工作是持久化的重试输入。
		terminal := t.Status == storagetask.StatusCancelled || t.Status == storagetask.StatusFailed
		expired := t.Deadline != nil && !t.Deadline.After(time.Now())
		if t.Kind == "migration" && w.Phase == "prepared" && (t.Phase == "cutover" || t.Phase == "cleanup") {
			return nil
		}
		if w.Phase == "prepared" && !terminal && (!expired || t.Kind == "migration") {
			return nil
		}
		n, e := tx.StorageTask.Update().Where(storagetask.IDEQ(t.ID), storagetask.PhaseEQ(t.Phase), storagetask.StatusEQ(t.Status)).SetPhase(t.Phase).Save(ctx)
		if e != nil {
			return e
		}
		if n != 1 {
			return nil
		}
		if w.LocationID != nil {
			l, e := tx.BlobLocation.Get(ctx, *w.LocationID)
			if e != nil {
				return e
			}
			if l.Status == bloblocation.StatusLive {
				return nil
			}
			active, e := tx.Blob.Query().Where(blob.ActiveLocationIDEQ(l.ID)).Exist(ctx)
			if e != nil {
				return e
			}
			if active {
				return nil
			}
		}
		n, e = tx.StorageWrite.Update().Where(storagewrite.IDEQ(id), storagewrite.PhaseEQ(w.Phase)).SetPhase("reconciling").Save(ctx)
		if e != nil {
			return e
		}
		if n == 1 {
			w.Phase = "reconciling"
			claimed = w
		}
		return nil
	})
	if err == nil && claimed != nil {
		s.mu.Lock()
		s.inFlight[id] = true
		s.mu.Unlock()
	}
	return claimed, err
}

func (s *StorageService) cleanWrite(ctx context.Context, w *ent.StorageWrite) error {
	d, err := s.driver(ctx, w.SpaceID, false)
	if err != nil {
		return err
	}
	if err = deleteWriteObjects(ctx, d, w); err != nil {
		return err
	}
	return withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		current, e := tx.StorageWrite.Get(ctx, w.ID)
		if e != nil {
			return e
		}
		if current.Phase == "committed" || current.Phase == "cleaned" {
			return nil
		}
		if current.Phase != "reconciling" {
			return ErrStorageConflict
		}
		res, e := tx.StorageReservation.Query().Where(storagereservation.WriteIDEQ(w.ID)).Only(ctx)
		if e != nil {
			return e
		}
		update := tx.StorageSpace.UpdateOneID(w.SpaceID)
		switch res.State {
		case storagereservation.StateReserved:
			update.AddReservedBytes(-res.Bytes)
		case storagereservation.StateCandidate:
			update.AddCandidateBytes(-res.Bytes)
		case storagereservation.StatePendingDelete:
			update.AddPendingDeleteBytes(-res.Bytes)
		case storagereservation.StateFreed:
			return nil
		default:
			return ErrStorageConflict
		}
		if e = update.Exec(ctx); e != nil {
			return e
		}
		if e = tx.StorageReservation.UpdateOneID(res.ID).SetState(storagereservation.StateFreed).Exec(ctx); e != nil {
			return e
		}
		if e = tx.StorageWrite.UpdateOneID(w.ID).SetPhase("cleaned").SetOutcomeUnknown(false).Exec(ctx); e != nil {
			return e
		}
		if w.LocationID != nil {
			if e = tx.BlobLocation.UpdateOneID(*w.LocationID).SetStatus(bloblocation.StatusDeleted).Exec(ctx); e != nil {
				return e
			}
			if _, e = tx.DeletionEntry.Update().Where(deletionentry.LocationIDEQ(*w.LocationID)).SetStatus(deletionentry.StatusDone).SetErrorCode("").Save(ctx); e != nil {
				return e
			}
		}
		t, e := tx.StorageTask.Get(ctx, w.TaskID)
		if e != nil {
			return e
		}
		remaining, e := tx.StorageWrite.Query().Where(storagewrite.TaskIDEQ(t.ID), storagewrite.PhaseNotIn("committed", "cleaned")).Exist(ctx)
		if e != nil {
			return e
		}
		u := tx.StorageTask.UpdateOneID(t.ID)
		if !remaining {
			u.SetCleanupStatus(storagetask.CleanupStatusDone)
		}
		if t.Kind != "migration" && t.Phase != "committed" && t.Status != storagetask.StatusCancelled {
			u.SetStatus(storagetask.StatusNeedsAction).SetErrorCode("storage_transfer_interrupted")
			if t.Kind == "upload" || t.Kind == "repair" || t.Kind == "source_update" {
				u.SetPhase("cleaned")
			}
		}
		return u.Exec(ctx)
	})
}

func deleteExactObject(ctx context.Context, d storage.Driver, obj storage.Object) error {
	if err := d.Delete(ctx, obj); err != nil && !errors.Is(err, storage.ErrNotFound) {
		return err
	}
	_, err := d.Stat(ctx, obj)
	if errors.Is(err, storage.ErrNotFound) {
		return nil
	}
	if err == nil {
		return storage.ErrUnavailable
	}
	return err
}

func deleteWriteObjects(ctx context.Context, d storage.Driver, w *ent.StorageWrite) error {
	if versions, ok := d.(storage.VersionLister); ok {
		objects, err := versions.Versions(ctx, w.ObjectKey)
		if err != nil && !errors.Is(err, storage.ErrUnsupported) {
			return err
		}
		if err == nil {
			for _, obj := range objects {
				if obj.Key != w.ObjectKey || obj.Version == "" {
					return storage.ErrInvalidKey
				}
				if err = deleteExactObject(ctx, d, obj); err != nil {
					return err
				}
			}
			remaining, err := versions.Versions(ctx, w.ObjectKey)
			if err != nil {
				return err
			}
			if len(remaining) != 0 {
				return storage.ErrUnavailable
			}
			return nil
		}
	}
	return deleteExactObject(ctx, d, storage.Object{Key: w.ObjectKey, Version: w.ProviderVersion, Size: w.MaxBytes})
}

func (s *StorageService) collect(ctx context.Context) error {
	if s.maintenance {
		return nil
	}
	now := time.Now().UTC()
	entries, err := s.client.DeletionEntry.Query().Where(deletionentry.ProjectIDGT(0), deletionentry.StatusNEQ(deletionentry.StatusDone), deletionentry.NotBeforeLTE(now), deletionentry.Or(deletionentry.NextRetryAtIsNil(), deletionentry.NextRetryAtLTE(now))).Order(ent.Asc(deletionentry.FieldUpdatedAt)).Limit(s.cfg.ReconcileBatchSize).All(ctx)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		s.mu.Lock()
		if s.readers[entry.LocationID] > 0 {
			s.mu.Unlock()
			continue
		}
		var location *ent.BlobLocation
		e := withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
			// 检查备份固定（pin）之前先锁定 location，与清单创建流程保持一致。
			n, e := tx.BlobLocation.Update().Where(bloblocation.IDEQ(entry.LocationID), bloblocation.StatusIn(bloblocation.StatusRetired, bloblocation.StatusDeleting)).SetStatus(bloblocation.StatusDeleting).Save(ctx)
			if e != nil {
				return e
			}
			if n != 1 {
				return ErrStorageConflict
			}
			pinned, e := tx.BackupPin.Query().Where(backuppin.LocationIDEQ(entry.LocationID), backuppin.ExpiresAtGT(time.Now().UTC())).Exist(ctx)
			if e != nil {
				return e
			}
			if pinned {
				return ErrStorageConflict
			}
			// 未发布的 location 归其 write 对账方所有，因此两条清理路径
			// 不可能重复删除或释放同一个预留。
			owned, e := tx.StorageWrite.Query().Where(storagewrite.LocationIDEQ(entry.LocationID), storagewrite.PhaseNotIn("committed", "cleaned")).Exist(ctx)
			if e != nil {
				return e
			}
			if owned {
				return ErrStorageConflict
			}
			location, e = tx.BlobLocation.Get(ctx, entry.LocationID)
			if e != nil {
				return e
			}
			if location.Status != bloblocation.StatusRetired && location.Status != bloblocation.StatusDeleting {
				return ErrStorageConflict
			}
			if location.RetainUntil != nil && location.RetainUntil.After(time.Now()) {
				return ErrStorageConflict
			}
			if location.BlobID != nil {
				b, e := tx.Blob.Get(ctx, *location.BlobID)
				if e != nil {
					return e
				}
				if b.ActiveLocationID != nil && *b.ActiveLocationID == location.ID {
					return ErrStorageConflict
				}
			}
			n, e = tx.DeletionEntry.Update().Where(deletionentry.IDEQ(entry.ID), deletionentry.StatusNEQ(deletionentry.StatusDone), deletionentry.Or(deletionentry.NextRetryAtIsNil(), deletionentry.NextRetryAtLTE(time.Now().UTC()))).SetStatus(deletionentry.StatusRunning).SetNextRetryAt(time.Now().UTC().Add(s.cfg.TransferTimeout)).AddAttempts(1).Save(ctx)
			if e != nil {
				return e
			}
			if n != 1 {
				return ErrStorageConflict
			}
			return nil
		})
		s.mu.Unlock()
		if e != nil {
			continue
		}
		d, e := s.driver(ctx, location.SpaceID, false)
		if e == nil {
			e = deleteExactObject(ctx, d, storage.Object{Key: location.ObjectKey, Version: location.ProviderVersion, DeleteMarker: location.DeleteMarker})
		}
		if e != nil && !errors.Is(e, storage.ErrNotFound) {
			_ = s.client.DeletionEntry.UpdateOneID(entry.ID).SetStatus(deletionentry.StatusBlocked).SetErrorCode(storageCode(e)).SetNextRetryAt(time.Now().UTC().Add(time.Minute)).Exec(ctx)
			continue
		}
		if e = withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
			current, e := tx.DeletionEntry.Get(ctx, entry.ID)
			if e != nil {
				return e
			}
			if current.Status == deletionentry.StatusDone {
				return nil
			}
			l, e := tx.BlobLocation.Get(ctx, location.ID)
			if e != nil {
				return e
			}
			if l.Status == bloblocation.StatusDeleted {
				return tx.DeletionEntry.UpdateOneID(entry.ID).SetStatus(deletionentry.StatusDone).Exec(ctx)
			}
			n, e := tx.BlobLocation.Update().Where(bloblocation.IDEQ(location.ID), bloblocation.StatusEQ(bloblocation.StatusDeleting)).SetStatus(bloblocation.StatusDeleted).Save(ctx)
			if e != nil {
				return e
			}
			if n != 1 {
				return ErrStorageConflict
			}
			if e = tx.StorageSpace.UpdateOneID(location.SpaceID).AddPendingDeleteBytes(-location.Size).Exec(ctx); e != nil {
				return e
			}
			if _, e = tx.StorageReservation.Update().Where(storagereservation.HasWriteWith(storagewrite.LocationIDEQ(location.ID))).SetState(storagereservation.StateFreed).Save(ctx); e != nil {
				return e
			}
			return tx.DeletionEntry.UpdateOneID(entry.ID).SetStatus(deletionentry.StatusDone).SetErrorCode("").Exec(ctx)
		}); e != nil {
			return e
		}
	}
	return nil
}

func (s *StorageService) refreshTaskCleanup(ctx context.Context) error {
	tasks, err := s.client.StorageTask.Query().Where(storagetask.ProjectIDGT(0), storagetask.StatusIn(storagetask.StatusCancelled, storagetask.StatusCompleted), storagetask.CleanupStatusEQ(storagetask.CleanupStatusCleanupPending)).Order(ent.Asc(storagetask.FieldUpdatedAt)).Limit(s.cfg.ReconcileBatchSize).All(ctx)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		pending, e := s.client.StorageWrite.Query().Where(storagewrite.TaskIDEQ(task.ID), storagewrite.PhaseNotIn("committed", "cleaned")).Exist(ctx)
		if e != nil {
			return e
		}
		if pending {
			continue
		}
		if task.Kind == "migration" && task.Status == storagetask.StatusCompleted {
			pending, e = s.client.StorageMigrationItem.Query().Where(storagemigrationitem.TaskIDEQ(task.ID), storagemigrationitem.HasSourceLocationWith(bloblocation.StatusNEQ(bloblocation.StatusDeleted))).Exist(ctx)
			if e != nil {
				return e
			}
			if pending {
				continue
			}
		}
		if _, e = s.client.StorageTask.Update().Where(storagetask.IDEQ(task.ID), storagetask.StatusEQ(task.Status)).SetCleanupStatus(storagetask.CleanupStatusDone).Save(ctx); e != nil {
			return e
		}
	}
	return nil
}

func (s *StorageService) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	nextReconcile := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = s.ProcessTasks(ctx)
			if !time.Now().Before(nextReconcile) {
				_ = s.Reconcile(ctx)
				nextReconcile = time.Now().Add(s.cfg.ReconcileInterval)
			}
		}
	}
}

type protectedReader struct {
	io.ReadCloser
	release func()
	once    bool
}

func (r *protectedReader) Close() error {
	err := r.ReadCloser.Close()
	if !r.once {
		r.once = true
		r.release()
	}
	return err
}
