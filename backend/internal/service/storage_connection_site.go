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
	"github.com/MeowSalty/LinguaFlow/backend/internal/storageguard"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storagenet"
)

// SetupSiteBackends 只对数据库中的部署身份做对账。启动期间绝不探测远程存储桶。
// 新安装的 S3 空间必须先由管理员显式执行 Check，才有资格被项目绑定。
func (s *StorageConnectionService) SetupSiteBackends(ctx context.Context) (int, error) {
	var defaultID int
	err := withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error { var err error; defaultID, err = s.setupSiteBackends(ctx, tx); return err })
	return defaultID, err
}

func (s *StorageConnectionService) setupSiteBackends(ctx context.Context, tx *ent.Client) (int, error) {
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
		err = func() error {
			c, err := tx.StorageConnection.Query().Where(storageconnection.OwnerKindEQ(storageconnection.OwnerKindSite), storageconnection.BackendIDEQ(backend.ID)).Only(ctx)
			if ent.IsNotFound(err) {
				if !s.cfg.Enabled || s.cfg.Maintenance {
					return nil
				}
				c, err = tx.StorageConnection.Create().SetRemoteUseState(storageconnection.RemoteUseStateNever).SetName(backend.ID).SetBackendID(backend.ID).SetDriver(storageconnection.DriverS3).SetOwnerKind(storageconnection.OwnerKindSite).SetOwnerID(0).SetAuthSource(storageconnection.AuthSourceDeployment).SetEndpoint(endpoint).SetRegion(backend.Region).SetPathStyle(backend.PathStyle).Save(ctx)
			}
			if err != nil {
				return err
			}
			// 启动登记必须查询持久退出身份:removed 与旧 backend_id 留下防复活记录,
			// 意外恢复旧配置时报告配置冲突,不重新登记也不访问旧位置。
			if c.Lifecycle == storageconnection.LifecycleRemoved {
				return ErrStorageRetirementConflict
			}
			// 旧 backend_id 在移除后回流不是新身份:必须显式冲突,不能当作首次登记。
			if c.BackendID != "" && c.BackendID != backend.ID {
				return ErrStorageRetirementConflict
			}
			// retiring 不重新启用、不创建替代空间,也不成为部署默认项。
			if c.Lifecycle == storageconnection.LifecycleRetiring {
				return nil
			}
			if c.Driver != storageconnection.DriverS3 || c.AuthSource != storageconnection.AuthSourceDeployment || c.Endpoint != endpoint || c.Region != backend.Region || c.PathStyle != backend.PathStyle {
				return ErrStorageConflict
			}
			sp, err = tx.StorageSpace.Query().Where(storagespace.ConnectionIDEQ(c.ID)).Only(ctx)
			if ent.IsNotFound(err) {
				if !s.cfg.Enabled || s.cfg.Maintenance {
					return nil
				}
				// 命名空间分配与最终移除共用同一把锁,避免并发登记同一 bucket。
				if e := storageguard.LockNamespace(ctx, tx, backend.Bucket); e != nil {
					return e
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
				policy, policyErr := storagePolicy(ctx, tx)
				if policyErr != nil {
					return policyErr
				}
				sp, err = tx.StorageSpace.Create().SetRemoteUseState(storagespace.RemoteUseStateNever).SetConnectionID(c.ID).SetName(backend.ID).SetOwnerKind(storagespace.OwnerKindSite).SetOwnerID(0).SetIdentity(generateUniqueID()).SetMarkerNonce(generateUniqueID()).SetBucket(backend.Bucket).SetPrefix(strings.TrimSuffix(backend.Prefix, "/")).SetNillableCapacityBytes(policy.DefaultSpaceCapacityBytes).Save(ctx)
			}
			if err != nil {
				return err
			}
			if sp == nil {
				return nil
			}
			// 已退出或退出中的空间绝不能被重新登记为可用身份。
			if sp.Lifecycle != storagespace.LifecycleAvailable {
				return ErrStorageRetirementConflict
			}
			// 活跃路径占用与历史行分离:创建空间与最终移除共用同一占用规则。
			if err = ensureStoragePathClaim(ctx, tx, sp); err != nil {
				return err
			}
			if sp.Bucket != backend.Bucket || sp.Prefix != strings.TrimSuffix(backend.Prefix, "/") {
				return ErrStorageConflict
			}
			return nil
		}()
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
	payload, err := s.deploymentCredentials(c, sp)
	if err != nil {
		return nil, err
	}
	if s.factory == nil {
		return nil, storage.ErrUnavailable
	}
	return s.factory(ctx, c, sp, payload)
}

// deploymentCredentials reads only parsed deployment configuration. Capability
// discovery uses the same identity checks without constructing a provider client.
func (s *StorageConnectionService) deploymentCredentials(c *ent.StorageConnection, sp *ent.StorageSpace) (storageauth.S3Payload, error) {
	if c.OwnerKind != storageconnection.OwnerKindSite || c.AuthSource != storageconnection.AuthSourceDeployment || c.Driver != storageconnection.DriverS3 {
		return storageauth.S3Payload{}, storage.ErrAuthRequired
	}
	for _, backend := range s.cfg.Backends {
		if backend.ID != c.BackendID {
			continue
		}
		endpoint, err := storagenet.NormalizeEndpoint(backend.Endpoint)
		if err != nil || endpoint != c.Endpoint || backend.Driver != "s3" || backend.Region != c.Region || backend.PathStyle != c.PathStyle || backend.Bucket != sp.Bucket || strings.TrimSuffix(backend.Prefix, "/") != sp.Prefix {
			return storageauth.S3Payload{}, ErrStorageConflict
		}
		payload := storageauth.S3Payload{Version: storageauth.PayloadVersion, AccessKeyID: backend.AccessKeyID, SecretAccessKey: backend.SecretAccessKey, SessionToken: backend.SessionToken}
		if err = payload.Validate(); err != nil {
			return storageauth.S3Payload{}, storage.ErrAuthRequired
		}
		return payload, nil
	}
	return storageauth.S3Payload{}, storage.ErrAuthRequired
}

// Check 校验当前部署或已生效的存储授权；只读检查绝不发送写入，
// 也不会改变空间的管理访问模式。
func (s *StorageConnectionService) check(ctx context.Context, actor, id int, write bool, expectedGeneration int64) (*StorageConnectionRecord, error) {
	c, err := s.authorized(ctx, s.client, actor, id)
	if err != nil {
		return nil, err
	}
	if c.ManagementGeneration != expectedGeneration {
		return nil, ErrStorageConflict
	}
	op := storageOpCheckRead
	if write {
		op = storageOpCheckWrite
	}
	if err = s.admitManagement(ctx, s.client, c, op); err != nil {
		return nil, err
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
		// 真实 IO 计数:检查期间的每次提供方调用都必须经过共享执行许可。
		d, e := bindStorageGuard(ctx, s.client, s.ioGuard, sp.ID, &storageGuardedDriver{service: s, spaceID: sp.ID})
		if e != nil {
			return nil, s.sanitize(e)
		}
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
		// 与 finalizing 竞争时由事务内重读的当前事实判定,而不是检查期间的陈旧快照。
		if err := storageguard.LockConnection(ctx, tx, c.ID); err != nil {
			return err
		}
		current, err := tx.StorageConnection.Get(ctx, c.ID)
		if err != nil {
			return err
		}
		if current.ManagementGeneration != c.ManagementGeneration || current.ActiveAuthGeneration != c.ActiveAuthGeneration {
			return ErrStorageConflict
		}
		if err := s.admitManagement(ctx, tx, current, op); err != nil {
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
