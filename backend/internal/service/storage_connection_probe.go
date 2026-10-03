package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/bloblocation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/deletionentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageauthversion"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagereservation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagespace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagewrite"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storageauth"
)

const storageMarkerKey = ".linguaflow/space.json"

func storageMarkerBytes(sp *ent.StorageSpace) []byte {
	b, _ := json.Marshal(struct {
		Version  int    `json:"version"`
		Identity string `json:"identity"`
		Nonce    string `json:"nonce"`
	}{1, sp.Identity, sp.MarkerNonce})
	return b
}

// VerifyStorageSpaceMarker 在不写入、不探测、不改动数据库的前提下校验物理身份，
// 离线备份与恢复校验同样适用。
func VerifyStorageSpaceMarker(ctx context.Context, driver storage.Driver, space *ent.StorageSpace) error {
	if driver == nil || space == nil {
		return ErrInvalidInput
	}
	marker, err := driver.Stat(ctx, storage.Object{Key: storageMarkerKey})
	if err != nil {
		return err
	}
	return readStorageProbe(ctx, driver, marker, storageMarkerBytes(space))
}

func readStorageProbe(ctx context.Context, d storage.Driver, o storage.Object, want []byte) error {
	r, err := d.Open(ctx, o)
	if err != nil {
		return err
	}
	got, err := io.ReadAll(io.LimitReader(&storageContextReader{ctx: ctx, r: r}, int64(len(want))+1))
	closeErr := r.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if !bytes.Equal(got, want) {
		return storage.ErrCorrupt
	}
	return nil
}

func (s *StorageConnectionService) verifySpace(ctx context.Context, actor int, c *ent.StorageConnection, sp *ent.StorageSpace, a *ent.StorageAuthVersion, d storage.Driver, write bool) (storage.Capabilities, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.TransferTimeout)
	defer cancel()
	inspector, ok := d.(storage.CapabilityInspector)
	if !ok {
		return storage.Capabilities{}, storage.ErrUnsupported
	}
	caps, err := inspector.Capabilities(ctx)
	if err != nil {
		return caps, err
	}
	if write && (!caps.ConditionalCreate || (caps.Versioned && !caps.ExactVersions)) {
		return caps, storage.ErrUnsupported
	}
	marker := storage.Object{Key: storageMarkerKey}
	marker, err = d.Stat(ctx, marker)
	if errors.Is(err, storage.ErrNotFound) && write && !sp.Verified {
		_, err = s.writeProbe(ctx, actor, c, sp, a, d, storageMarkerKey, storageMarkerBytes(sp), true, caps)
	} else if err == nil {
		err = readStorageProbe(ctx, d, marker, storageMarkerBytes(sp))
		if err == nil {
			err = s.adoptMarker(ctx, c, sp, a, marker)
		}
	}
	if err != nil {
		return caps, err
	}
	if write {
		key := ".linguaflow/probes/" + generateUniqueID()
		_, err = s.writeProbe(ctx, actor, c, sp, a, d, key, []byte(generateUniqueID()), false, caps)
	}
	return caps, err
}

// 续期的授权可以在原响应丢失后，完成先前 marker 的已验证写入。
// 只有在当前管理授权仍然有效时才会更新准入代数，且绝不创建新的物理标记 key。
func (s *StorageConnectionService) adoptMarker(ctx context.Context, c *ent.StorageConnection, sp *ent.StorageSpace, a *ent.StorageAuthVersion, o storage.Object) error {
	w, err := s.client.StorageWrite.Query().Where(storagewrite.SpaceIDEQ(sp.ID), storagewrite.ObjectKeyEQ(storageMarkerKey)).Only(ctx)
	if ent.IsNotFound(err) {
		return ErrStorageConflict
	}
	if err != nil {
		return err
	}
	if w.Phase == "committed" {
		return nil
	}
	if w.Phase == "cleaned" {
		return ErrStorageConflict
	}
	err = withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		conn, e := tx.StorageConnection.Get(ctx, c.ID)
		if e != nil {
			return e
		}
		space, e := tx.StorageSpace.Get(ctx, sp.ID)
		if e != nil {
			return e
		}
		if conn.Status != storageconnection.StatusEnabled || conn.ManagementGeneration != c.ManagementGeneration || space.ManagementGeneration != sp.ManagementGeneration || space.Status == storagespace.StatusDisabled {
			return ErrStorageConflict
		}
		return tx.StorageWrite.UpdateOneID(w.ID).SetConnectionGeneration(c.ManagementGeneration).SetSpaceGeneration(sp.ManagementGeneration).SetAuthGeneration(a.Generation).SetProviderVersion(o.Version).Exec(ctx)
	})
	if err != nil {
		return err
	}
	w.ConnectionGeneration = c.ManagementGeneration
	w.SpaceGeneration = sp.ManagementGeneration
	w.AuthGeneration = a.Generation
	w.ProviderVersion = o.Version
	return s.finishMarker(ctx, w)
}

func (s *StorageConnectionService) registerProbe(ctx context.Context, actor int, c *ent.StorageConnection, sp *ent.StorageSpace, a *ent.StorageAuthVersion, key string, data []byte, marker bool) (*ent.StorageWrite, error) {
	var w *ent.StorageWrite
	err := withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		current, err := tx.StorageConnection.Get(ctx, c.ID)
		if err != nil {
			return err
		}
		space, err := tx.StorageSpace.Get(ctx, sp.ID)
		if err != nil {
			return err
		}
		if current.ManagementGeneration != c.ManagementGeneration || current.Status != storageconnection.StatusEnabled || space.ManagementGeneration != sp.ManagementGeneration || space.Status != storagespace.StatusActive {
			return ErrStorageConflict
		}
		size := int64(len(data))
		used := space.LiveBytes + space.CandidateBytes + space.PendingDeleteBytes + space.ReservedBytes
		if used < 0 || size > space.CapacityBytes-used {
			return storage.ErrLimit
		}
		kind := "storage_probe"
		if marker {
			kind = "storage_marker"
		}
		// 发送前被中断的 marker 可以在其确切持久化的 key 上继续。
		old, err := tx.StorageWrite.Query().Where(storagewrite.SpaceIDEQ(sp.ID), storagewrite.ObjectKeyEQ(key)).Only(ctx)
		if err == nil {
			if !marker || old.Phase != "cleaned" {
				return ErrStorageConflict
			}
			if err = tx.StorageReservation.Update().Where(storagereservation.WriteIDEQ(old.ID), storagereservation.StateEQ(storagereservation.StateFreed)).SetState(storagereservation.StateReserved).Exec(ctx); err != nil {
				return err
			}
			if err = tx.StorageWrite.UpdateOneID(old.ID).SetPhase("accepted").SetOutcomeUnknown(false).SetAuthGeneration(a.Generation).SetConnectionGeneration(c.ManagementGeneration).SetSpaceGeneration(sp.ManagementGeneration).SetExpiresAt(time.Now().UTC().Add(s.cfg.IntentTTL)).Exec(ctx); err != nil {
				return err
			}
			if old.LocationID != nil {
				if err = tx.BlobLocation.UpdateOneID(*old.LocationID).SetStatus(bloblocation.StatusCandidate).SetProviderVersion("").Exec(ctx); err != nil {
					return err
				}
				if err = tx.DeletionEntry.Update().Where(deletionentry.LocationIDEQ(*old.LocationID)).SetStatus(deletionentry.StatusPending).Exec(ctx); err != nil {
					return err
				}
			}
			if err = tx.StorageTask.UpdateOneID(old.TaskID).SetStatus(storagetask.StatusRunning).SetPhase("accepted").SetCleanupStatus(storagetask.CleanupStatusCleanupPending).Exec(ctx); err != nil {
				return err
			}
			if err = tx.StorageSpace.UpdateOneID(space.ID).AddReservedBytes(size).Exec(ctx); err != nil {
				return err
			}
			w, err = tx.StorageWrite.Get(ctx, old.ID)
			return err
		}
		if !ent.IsNotFound(err) {
			return err
		}
		sum := sha256.Sum256(data)
		digest := hex.EncodeToString(sum[:])
		operation := generateUniqueID()
		task, err := tx.StorageTask.Create().SetOperationID(operation).SetIdempotencyKey(operation).SetRequestHash(digest).SetActorID(actor).SetProjectID(0).SetKind(kind).SetTargetSpaceID(sp.ID).SetStatus(storagetask.StatusRunning).SetCleanupStatus(storagetask.CleanupStatusCleanupPending).SetDeadline(time.Now().UTC().Add(s.cfg.IntentTTL)).Save(ctx)
		if err != nil {
			return err
		}
		location, err := tx.BlobLocation.Create().SetSpaceID(sp.ID).SetObjectKey(key).SetSize(size).Save(ctx)
		if err != nil {
			return err
		}
		if err = tx.DeletionEntry.Create().SetLocationID(location.ID).SetOwnerKind(string(sp.OwnerKind)).SetOwnerID(sp.OwnerID).SetProjectID(0).SetNotBefore(time.Now().UTC().Add(s.cfg.DeletionGrace)).Exec(ctx); err != nil {
			return err
		}
		w, err = tx.StorageWrite.Create().SetTaskID(task.ID).SetSpaceID(sp.ID).SetLocationID(location.ID).SetAttemptID(generateUniqueID()).SetObjectKey(key).SetMaxBytes(size).SetActualBytes(size).SetSha256(digest).SetConnectionGeneration(c.ManagementGeneration).SetSpaceGeneration(sp.ManagementGeneration).SetAuthGeneration(a.Generation).SetExpiresAt(time.Now().UTC().Add(s.cfg.IntentTTL)).Save(ctx)
		if err != nil {
			return err
		}
		if err = tx.StorageReservation.Create().SetWriteID(w.ID).SetSpaceID(sp.ID).SetBytes(size).Exec(ctx); err != nil {
			return err
		}
		n, err := tx.StorageSpace.Update().Where(storagespace.IDEQ(space.ID), storagespace.ReservedBytesEQ(space.ReservedBytes), storagespace.CandidateBytesEQ(space.CandidateBytes), storagespace.LiveBytesEQ(space.LiveBytes), storagespace.PendingDeleteBytesEQ(space.PendingDeleteBytes)).AddReservedBytes(size).Save(ctx)
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrStorageConflict
		}
		return nil
	})
	return w, err
}

func (s *StorageConnectionService) writeProbe(ctx context.Context, actor int, c *ent.StorageConnection, sp *ent.StorageSpace, a *ent.StorageAuthVersion, d storage.Driver, key string, data []byte, marker bool, caps storage.Capabilities) (result storage.Object, err error) {
	s.mu.Lock()
	w, err := s.registerProbe(ctx, actor, c, sp, a, key, data, marker)
	if err == nil {
		s.activeWrites[w.ID] = true
	}
	s.mu.Unlock()
	if err != nil {
		return result, err
	}
	defer func() {
		s.mu.Lock()
		delete(s.activeWrites, w.ID)
		s.mu.Unlock()
		if err != nil {
			s.recordProbeFailure(ctx, w, err)
		}
	}()
	if err = s.client.StorageWrite.UpdateOneID(w.ID).SetPhase("sending").SetOutcomeUnknown(true).Exec(ctx); err != nil {
		return result, err
	}
	result, err = d.PutNew(ctx, key, bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return result, err
	}
	if err = s.client.StorageWrite.UpdateOneID(w.ID).SetProviderVersion(result.Version).SetPhase("verifying").Exec(ctx); err != nil {
		return result, err
	}
	if err = s.client.BlobLocation.UpdateOneID(*w.LocationID).SetProviderVersion(result.Version).Exec(ctx); err != nil {
		return result, err
	}
	w.ProviderVersion = result.Version
	if err = readStorageProbe(ctx, d, result, data); err != nil {
		return result, err
	}
	if marker {
		return result, s.finishMarker(ctx, w)
	}
	// 在这条已登记的确切 key 上验证条件创建；前置条件被忽略即视为准入失败。
	// 产生的所有版本都保留在这条 write 的日志中。
	if _, err = d.PutNew(ctx, key, bytes.NewReader(data), int64(len(data))); !errors.Is(err, storage.ErrExists) {
		if err == nil {
			err = storage.ErrUnsupported
		}
		return result, err
	}
	err = nil
	if caps.Versioned {
		versions, listErr := d.(storage.VersionLister).Versions(ctx, key)
		if listErr != nil {
			return result, listErr
		}
		if len(versions) == 0 {
			return result, storage.ErrUnsupported
		}
	}
	if err = s.cleanupProbe(ctx, w, d); err != nil {
		return result, err
	}
	return result, nil
}

func (s *StorageConnectionService) recordProbeFailure(ctx context.Context, w *ent.StorageWrite, cause error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		current, err := tx.StorageWrite.Get(ctx, w.ID)
		if err != nil {
			return err
		}
		if current.Phase == "committed" || current.Phase == "cleaned" {
			return nil
		}
		if err = tx.StorageWrite.UpdateOneID(w.ID).SetPhase("reconcile").Exec(ctx); err != nil {
			return err
		}
		return tx.StorageTask.UpdateOneID(w.TaskID).SetStatus(storagetask.StatusNeedsAction).SetErrorCode(storageCode(s.sanitize(cause))).SetCleanupStatus(storagetask.CleanupStatusCleanupPending).Exec(ctx)
	})
}

func (s *StorageConnectionService) finishMarker(ctx context.Context, w *ent.StorageWrite) error {
	return withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		sp, err := tx.StorageSpace.Get(ctx, w.SpaceID)
		if err != nil {
			return err
		}
		c, err := tx.StorageConnection.Get(ctx, sp.ConnectionID)
		if err != nil {
			return err
		}
		if c.Status != storageconnection.StatusEnabled || c.ManagementGeneration != w.ConnectionGeneration || sp.Status == storagespace.StatusDisabled || sp.ManagementGeneration != w.SpaceGeneration {
			return ErrStorageConflict
		}
		current, err := tx.StorageWrite.Get(ctx, w.ID)
		if err != nil {
			return err
		}
		if current.Phase == "committed" {
			return nil
		}
		res, err := tx.StorageReservation.Query().Where(storagereservation.WriteIDEQ(w.ID)).Only(ctx)
		if err != nil {
			return err
		}
		if res.State != storagereservation.StateReserved {
			return ErrStorageConflict
		}
		if err = tx.StorageSpace.UpdateOneID(w.SpaceID).AddReservedBytes(-res.Bytes).AddLiveBytes(res.Bytes).Exec(ctx); err != nil {
			return err
		}
		if err = tx.StorageReservation.UpdateOneID(res.ID).SetState(storagereservation.StateLive).Exec(ctx); err != nil {
			return err
		}
		if err = tx.BlobLocation.UpdateOneID(*w.LocationID).SetProviderVersion(w.ProviderVersion).SetStatus(bloblocation.StatusLive).SetIntegrity(bloblocation.IntegrityAvailable).SetVerifiedAt(time.Now().UTC()).Exec(ctx); err != nil {
			return err
		}
		if err = tx.DeletionEntry.Update().Where(deletionentry.LocationIDEQ(*w.LocationID)).SetStatus(deletionentry.StatusDone).Exec(ctx); err != nil {
			return err
		}
		if err = tx.StorageWrite.UpdateOneID(w.ID).SetPhase("committed").SetOutcomeUnknown(false).Exec(ctx); err != nil {
			return err
		}
		return tx.StorageTask.UpdateOneID(w.TaskID).SetPhase("committed").SetStatus(storagetask.StatusCompleted).SetCleanupStatus(storagetask.CleanupStatusDone).SetErrorCode("").Exec(ctx)
	})
}

func (s *StorageConnectionService) cleanupProbe(ctx context.Context, w *ent.StorageWrite, d storage.Driver) error {
	// 删除前先解析出所有可能的版本，包括响应丢失与提供方前置条件失败的情况；
	// 不支持版本化的提供方会报告 Unsupported。
	objects := []storage.Object{{Key: w.ObjectKey, Version: w.ProviderVersion}}
	if lister, ok := d.(storage.VersionLister); ok {
		versions, err := lister.Versions(ctx, w.ObjectKey)
		if err == nil {
			objects = versions
		} else if !errors.Is(err, storage.ErrUnsupported) {
			return err
		}
	}
	for _, o := range objects {
		if o.Key != w.ObjectKey {
			return storage.ErrInvalidKey
		}
		if err := d.Delete(ctx, o); err != nil && !errors.Is(err, storage.ErrNotFound) {
			return err
		}
	}
	if _, err := d.Stat(ctx, storage.Object{Key: w.ObjectKey}); !errors.Is(err, storage.ErrNotFound) {
		if err == nil {
			return storage.ErrUnavailable
		}
		return err
	}
	if lister, ok := d.(storage.VersionLister); ok {
		versions, err := lister.Versions(ctx, w.ObjectKey)
		if err == nil && len(versions) > 0 {
			return storage.ErrUnavailable
		}
		if err != nil && !errors.Is(err, storage.ErrUnsupported) {
			return err
		}
	}
	return withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		current, err := tx.StorageWrite.Get(ctx, w.ID)
		if err != nil {
			return err
		}
		if current.Phase == "cleaned" {
			return nil
		}
		if current.Phase == "committed" {
			return ErrStorageConflict
		}
		res, err := tx.StorageReservation.Query().Where(storagereservation.WriteIDEQ(w.ID)).Only(ctx)
		if err != nil {
			return err
		}
		if res.State != storagereservation.StateReserved {
			return ErrStorageConflict
		}
		if err = tx.StorageSpace.UpdateOneID(w.SpaceID).AddReservedBytes(-res.Bytes).Exec(ctx); err != nil {
			return err
		}
		if err = tx.StorageReservation.UpdateOneID(res.ID).SetState(storagereservation.StateFreed).Exec(ctx); err != nil {
			return err
		}
		if err = tx.BlobLocation.UpdateOneID(*w.LocationID).SetStatus(bloblocation.StatusDeleted).Exec(ctx); err != nil {
			return err
		}
		if err = tx.DeletionEntry.Update().Where(deletionentry.LocationIDEQ(*w.LocationID)).SetStatus(deletionentry.StatusDone).Exec(ctx); err != nil {
			return err
		}
		if err = tx.StorageWrite.UpdateOneID(w.ID).SetPhase("cleaned").SetOutcomeUnknown(false).Exec(ctx); err != nil {
			return err
		}
		return tx.StorageTask.UpdateOneID(w.TaskID).SetPhase("cleaned").SetStatus(storagetask.StatusCompleted).SetCleanupStatus(storagetask.CleanupStatusDone).SetErrorCode("").Exec(ctx)
	})
}

func (s *StorageConnectionService) recoveryDriver(ctx context.Context, w *ent.StorageWrite) (storage.Driver, error) {
	check := func(ctx context.Context, write bool) (storage.Driver, error) { return s.recoveryAttempt(ctx, w, write) }
	if _, err := check(ctx, false); err != nil {
		return nil, err
	}
	return &storageGuardedDriver{check: check, deleteCheck: func(ctx context.Context) (storage.Driver, error) {
		if s.cfg.Maintenance {
			return nil, ErrStorageMaintenance
		}
		return check(ctx, false)
	}}, nil
}

func (s *StorageConnectionService) recoveryAttempt(ctx context.Context, w *ent.StorageWrite, write bool) (storage.Driver, error) {
	sp, err := s.client.StorageSpace.Get(ctx, w.SpaceID)
	if err != nil {
		return nil, err
	}
	c, err := s.client.StorageConnection.Get(ctx, sp.ConnectionID)
	if err != nil {
		return nil, err
	}
	if c.AuthSource == storageconnection.AuthSourceDeployment || c.ActiveAuthGeneration > 0 {
		d, _, err := s.activeDriver(ctx, sp.ID, write)
		return d, err
	}
	// 只要管理授权仍然有效，失败的候选就可以清理自身有界的尝试。
	// 吊销与过期则永远是终态。
	if c.Status != storageconnection.StatusEnabled || sp.Status == storagespace.StatusDisabled || c.ManagementGeneration != w.ConnectionGeneration || sp.ManagementGeneration != w.SpaceGeneration || w.ExpiresAt == nil || !w.ExpiresAt.After(time.Now()) {
		return nil, storage.ErrPermission
	}
	a, err := s.client.StorageAuthVersion.Query().Where(storageauthversion.ConnectionIDEQ(c.ID), storageauthversion.GenerationEQ(w.AuthGeneration), storageauthversion.StatusNEQ(storageauthversion.StatusRevoked)).Only(ctx)
	if err != nil {
		return nil, storage.ErrAuthRequired
	}
	if a.ExpiresAt != nil && !a.ExpiresAt.After(time.Now()) {
		return nil, storage.ErrAuthRequired
	}
	payload, err := storageauth.DecryptS3(s.keys, storageAuthIdentity(c, a.Generation), storageCiphertext(a))
	if err != nil {
		return nil, ErrStorageCrypto
	}
	if s.factory == nil {
		return nil, storage.ErrUnavailable
	}
	return s.factory(ctx, c, sp, payload)
}

// Reconcile 恢复有界且已登记的探测操作。正常启动绝不列举存储桶或扫描未知对象；
// 维护模式会挂起所有远程操作。
func (s *StorageConnectionService) Reconcile(ctx context.Context) error {
	if s.cfg.Maintenance {
		return nil
	}
	rows, err := s.client.StorageWrite.Query().Where(storagewrite.PhaseNotIn("committed", "cleaned"), storagewrite.HasTaskWith(storagetask.KindIn("storage_probe", "storage_marker"))).Order(ent.Asc(storagewrite.FieldUpdatedAt)).Limit(s.cfg.ReconcileBatchSize).All(ctx)
	if err != nil {
		return err
	}
	for _, w := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.mu.Lock()
		busy := s.activeWrites[w.ID]
		if !busy {
			s.activeWrites[w.ID] = true
		}
		s.mu.Unlock()
		if busy {
			continue
		}
		func() {
			defer func() { s.mu.Lock(); delete(s.activeWrites, w.ID); s.mu.Unlock() }()
			opctx, cancel := context.WithTimeout(ctx, s.cfg.TransferTimeout)
			defer cancel()
			d, e := s.recoveryDriver(opctx, w)
			if e != nil {
				s.recordProbeFailure(opctx, w, e)
				return
			}
			task, e := s.client.StorageTask.Get(opctx, w.TaskID)
			if e != nil {
				return
			}
			if task.Kind == "storage_marker" {
				sp, e := s.client.StorageSpace.Get(opctx, w.SpaceID)
				if e != nil {
					return
				}
				o, e := d.Stat(opctx, storage.Object{Key: w.ObjectKey})
				if e == nil {
					e = readStorageProbe(opctx, d, o, storageMarkerBytes(sp))
					if e == nil {
						w.ProviderVersion = o.Version
						e = s.finishMarker(opctx, w)
					}
					if e != nil {
						s.recordProbeFailure(opctx, w, e)
					}
					return
				}
				if !errors.Is(e, storage.ErrNotFound) {
					s.recordProbeFailure(opctx, w, e)
					return
				}
			}
			if e = s.cleanupProbe(opctx, w, d); e != nil {
				s.recordProbeFailure(opctx, w, e)
			}
		}()
	}
	return nil
}
