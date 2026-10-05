package service

import (
	"context"
	"errors"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagespace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagewrite"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

// AdmitLocalSpace 在写入被受理之前，把本地根目录与其持久化标记绑定。
// 它绝不探测远程存储，也不使用授权密钥环。
func (s *StorageConnectionService) AdmitLocalSpace(ctx context.Context, spaceID int, driver storage.Driver) error {
	if s.cfg.Maintenance {
		return ErrStorageMaintenance
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.TransferTimeout)
	defer cancel()
	sp, err := s.client.StorageSpace.Get(ctx, spaceID)
	if err != nil {
		return err
	}
	c, err := s.client.StorageConnection.Get(ctx, sp.ConnectionID)
	if err != nil {
		return err
	}
	if c.Driver != storageconnection.DriverLocal || c.OwnerKind != storageconnection.OwnerKindSite || c.BackendID == "legacy" {
		return storage.ErrUnsupported
	}
	if c.Status != storageconnection.StatusEnabled || sp.Status == storagespace.StatusDisabled {
		return storage.ErrPermission
	}
	auth := &ent.StorageAuthVersion{}
	// 已验证的空间必须保留其原始标记，即使其目录曾被删除又在同一路径重建。
	if sp.Verified {
		_, err = s.verifySpace(ctx, 0, c, sp, auth, driver, false)
		return err
	}
	if sp.Status != storagespace.StatusActive {
		return storage.ErrPermission
	}
	// 只处理该根目录已登记的 setup 尝试。这里刻意不做通用对账——
	// 通用对账可能会操作远程空间。
	writes, err := s.client.StorageWrite.Query().Where(storagewrite.SpaceIDEQ(sp.ID), storagewrite.PhaseNotIn("committed", "cleaned"), storagewrite.HasTaskWith(storagetask.KindIn("storage_marker", "storage_probe"))).Limit(s.cfg.ReconcileBatchSize).All(ctx)
	if err != nil {
		return err
	}
	for _, w := range writes {
		if w.ObjectKey == storageMarkerKey {
			obj, statErr := driver.Stat(ctx, storage.Object{Key: w.ObjectKey})
			if statErr == nil {
				if err = readStorageProbe(ctx, driver, obj, storageMarkerBytes(sp)); err != nil {
					return err
				}
				if err = s.adoptMarker(ctx, c, sp, auth, obj); err != nil {
					return err
				}
				continue
			}
			if !errors.Is(statErr, storage.ErrNotFound) {
				return statErr
			}
		}
		if err = s.cleanupProbe(ctx, w, driver); err != nil {
			return err
		}
	}
	if _, err = s.verifySpace(ctx, 0, c, sp, auth, driver, true); err != nil {
		return err
	}
	return withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		n, err := tx.StorageConnection.Update().Where(storageconnection.IDEQ(c.ID), storageconnection.ManagementGenerationEQ(c.ManagementGeneration), storageconnection.StatusEQ(storageconnection.StatusEnabled)).SetHealth("available").SetCheckedAt(time.Now().UTC()).Save(ctx)
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrStorageConflict
		}
		n, err = tx.StorageSpace.Update().Where(storagespace.IDEQ(sp.ID), storagespace.ManagementGenerationEQ(sp.ManagementGeneration), storagespace.StatusEQ(storagespace.StatusActive)).SetVerified(true).Save(ctx)
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrStorageConflict
		}
		return nil
	})
}
