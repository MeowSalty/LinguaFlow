package service

import (
	"context"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagespace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storageauth"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storagenet"
)

// SetupSiteBackends 只对数据库中的部署身份做对账。启动期间绝不探测远程存储桶。
// 新安装的 S3 空间必须先由管理员显式执行 Check，才有资格被项目绑定。
func (s *StorageConnectionService) SetupSiteBackends(ctx context.Context) (int, error) {
	defaultID := 0
	for _, backend := range s.cfg.Backends {
		if backend.Driver != "s3" {
			continue
		}
		endpoint, err := storagenet.NormalizeEndpoint(backend.Endpoint)
		if err != nil {
			return 0, ErrInvalidInput
		}
		var sp *ent.StorageSpace
		err = withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
			c, err := tx.StorageConnection.Query().Where(storageconnection.OwnerKindEQ(storageconnection.OwnerKindSite), storageconnection.BackendIDEQ(backend.ID)).Only(ctx)
			if ent.IsNotFound(err) {
				if !s.cfg.Enabled || s.cfg.Maintenance {
					return nil
				}
				c, err = tx.StorageConnection.Create().SetName(backend.ID).SetBackendID(backend.ID).SetDriver(storageconnection.DriverS3).SetOwnerKind(storageconnection.OwnerKindSite).SetOwnerID(0).SetAuthSource(storageconnection.AuthSourceDeployment).SetEndpoint(endpoint).SetRegion(backend.Region).SetPathStyle(backend.PathStyle).Save(ctx)
			}
			if err != nil {
				return err
			}
			if c.Driver != storageconnection.DriverS3 || c.AuthSource != storageconnection.AuthSourceDeployment || c.Endpoint != endpoint || c.Region != backend.Region || c.PathStyle != backend.PathStyle {
				return ErrStorageConflict
			}
			sp, err = tx.StorageSpace.Query().Where(storagespace.ConnectionIDEQ(c.ID)).Only(ctx)
			if ent.IsNotFound(err) {
				if !s.cfg.Enabled || s.cfg.Maintenance {
					return nil
				}
				others, e := tx.StorageSpace.Query().Where(storagespace.BucketEQ(backend.Bucket)).All(ctx)
				if e != nil {
					return e
				}
				for _, other := range others {
					if storagePrefixesOverlap(other.Prefix, strings.TrimSuffix(backend.Prefix, "/")) {
						return ErrStorageConflict
					}
				}
				sp, err = tx.StorageSpace.Create().SetConnectionID(c.ID).SetName(backend.ID).SetOwnerKind(storagespace.OwnerKindSite).SetOwnerID(0).SetIdentity(generateUniqueID()).SetMarkerNonce(generateUniqueID()).SetBucket(backend.Bucket).SetPrefix(strings.TrimSuffix(backend.Prefix, "/")).SetCapacityBytes(s.cfg.Limits.CapacityBytes).Save(ctx)
			}
			if err != nil {
				return err
			}
			if sp == nil {
				return nil
			}
			if sp.Bucket != backend.Bucket || sp.Prefix != strings.TrimSuffix(backend.Prefix, "/") {
				return ErrStorageConflict
			}
			return nil
		})
		if err != nil {
			return 0, err
		}
		if sp != nil && backend.ID == s.cfg.DefaultSiteSpace {
			defaultID = sp.ID
		}
	}
	return defaultID, nil
}

func (s *StorageConnectionService) deploymentDriver(ctx context.Context, c *ent.StorageConnection, sp *ent.StorageSpace) (storage.Driver, error) {
	if c.OwnerKind != storageconnection.OwnerKindSite || c.AuthSource != storageconnection.AuthSourceDeployment || c.Driver != storageconnection.DriverS3 {
		return nil, storage.ErrAuthRequired
	}
	for _, backend := range s.cfg.Backends {
		if backend.ID != c.BackendID {
			continue
		}
		endpoint, err := storagenet.NormalizeEndpoint(backend.Endpoint)
		if err != nil || endpoint != c.Endpoint || backend.Driver != "s3" || backend.Region != c.Region || backend.PathStyle != c.PathStyle || backend.Bucket != sp.Bucket || strings.TrimSuffix(backend.Prefix, "/") != sp.Prefix {
			return nil, ErrStorageConflict
		}
		payload := storageauth.S3Payload{Version: storageauth.PayloadVersion, AccessKeyID: backend.AccessKeyID, SecretAccessKey: backend.SecretAccessKey, SessionToken: backend.SessionToken}
		if err = payload.Validate(); err != nil {
			return nil, storage.ErrAuthRequired
		}
		if s.factory == nil {
			return nil, storage.ErrUnavailable
		}
		return s.factory(ctx, c, sp, payload)
	}
	return nil, storage.ErrAuthRequired
}

// Check 校验当前部署或已生效的存储授权；只读检查绝不发送写入，
// 也不会改变空间的管理访问模式。
func (s *StorageConnectionService) check(ctx context.Context, actor, id int, write bool, expectedGeneration int64) (*StorageConnectionRecord, error) {
	if write && (!s.cfg.Enabled || s.cfg.Maintenance) {
		return nil, ErrStorageMaintenance
	}
	c, err := s.authorized(ctx, s.client, actor, id)
	if err != nil {
		return nil, err
	}
	if c.ManagementGeneration != expectedGeneration {
		return nil, ErrStorageConflict
	}
	spaces, err := s.client.StorageSpace.Query().Where(storagespace.ConnectionIDEQ(c.ID)).Limit(101).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(spaces) == 0 || len(spaces) > 100 {
		return nil, ErrInvalidInput
	}
	checked := map[int]storage.Capabilities{}
	for _, sp := range spaces {
		if sp.Status == storagespace.StatusDisabled {
			continue
		}
		d := &storageGuardedDriver{service: s, spaceID: sp.ID}
		caps, e := s.verifySpace(ctx, actor, c, sp, &ent.StorageAuthVersion{Generation: c.ActiveAuthGeneration}, d, write)
		if e != nil {
			return nil, s.sanitize(e)
		}
		checked[sp.ID] = caps
	}
	if len(checked) == 0 {
		return nil, storage.ErrPermission
	}
	err = withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		if err := s.requireOwner(ctx, tx, actor, string(c.OwnerKind), c.OwnerID); err != nil {
			return err
		}
		n, err := tx.StorageConnection.Update().Where(storageconnection.IDEQ(c.ID), storageconnection.ManagementGenerationEQ(c.ManagementGeneration), storageconnection.ActiveAuthGenerationEQ(c.ActiveAuthGeneration), storageconnection.StatusEQ(storageconnection.StatusEnabled)).SetHealth("available").SetCheckedAt(time.Now().UTC()).Save(ctx)
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrStorageConflict
		}
		for _, sp := range spaces {
			caps, ok := checked[sp.ID]
			if !ok {
				continue
			}
			n, err = tx.StorageSpace.Update().Where(storagespace.IDEQ(sp.ID), storagespace.ManagementGenerationEQ(sp.ManagementGeneration)).SetVerified(true).SetVersioned(caps.Versioned).Save(ctx)
			if err != nil {
				return err
			}
			if n != 1 {
				return ErrStorageConflict
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, actor, id)
}

// Delete 保留持久身份与清理引用。禁用的连接可以重新启用以进行恢复；
// 业务删除绝不会销毁这些证据。
func (s *StorageConnectionService) Delete(ctx context.Context, actor, id int, generation int64) error {
	_, err := s.SetStatus(ctx, actor, id, "disabled", generation)
	return err
}
func (s *StorageConnectionService) UpdateState(ctx context.Context, actor, id int, status string, generation int64) (*StorageConnectionRecord, error) {
	return s.SetStatus(ctx, actor, id, status, generation)
}
