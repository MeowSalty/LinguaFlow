package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageauthversion"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageretirement"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageretiremententry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagespace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storageauth"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storageguard"
)

const retirementRecoveryKind = "recover_authorization"

// recoveryDraft 是条目上记录的非秘密恢复草稿证据:已创建的受限密文代次。
// 重试复用同一代次,而不是每轮失败都新建一份密文。
type recoveryDraft struct {
	Generation int64  `json:"auth_generation,omitempty"`
	Outcome    string `json:"outcome,omitempty"`
}

// retirementRecoveryFingerprint 是幂等请求哈希的输入。它有意只包含非秘密事实:
// 相同幂等键下不同的凭据身份或有效期会冲突，持久请求摘要不含明文秘密。
type retirementRecoveryFingerprint struct {
	Connection     int    `json:"connection_id"`
	TargetIdentity string `json:"target_identity"`
	Generation     int64  `json:"expected_generation"`
	Management     int64  `json:"expected_management_generation"`
	AccessKeyID    string `json:"access_key_id"`
	SecretDigest   string `json:"secret_digest,omitempty"`
	ExpiresAt      string `json:"expires_at,omitempty"`
}

// retirementRecoverySecretSHA256 is a fixed-domain SHA-256 commitment. It is
// deliberately not claimed to be keyring-keyed: it prevents accidental secret
// persistence and distinguishes high-entropy same-length secrets, but it must
// not be relied on to protect low-entropy secrets. Raw secrets never enter the
// persisted fingerprint, rows, audit, or logs.
func retirementRecoverySecretSHA256(payload storageauth.S3Payload) string {
	input, _ := json.Marshal([]string{payload.AccessKeyID, payload.SecretAccessKey, payload.SessionToken})
	sum := sha256.Sum256(append([]byte("linguaflow:storage:retirement:recovery:v1:"), input...))
	return hex.EncodeToString(sum[:])
}

// RecoverAuthorization 是 finalizing 阶段的受限凭据恢复(第 18.6/18.8 节)。
//
// 它沿用既有授权版本的加密存储,但新密文 purpose=retirement 且关联同一退出任务:
// 永不激活为普通业务授权、不重建标记、不写入测试对象。恢复先排空旧执行器并推进
// 执行代次,重启或重试后仍只授予原退出范围。expires 为可选的上限期限。
func (s *StorageRetirementService) RecoverAuthorization(ctx context.Context, actor, id int, key string, expectedGeneration, expectedManagementGeneration int64, payload storageauth.S3Payload, expires ...*time.Time) (*ent.StorageRetirement, error) {
	if err := ValidateStorageIdempotencyKey(key); err != nil {
		return nil, err
	}
	if err := payload.Validate(); err != nil {
		return nil, ErrInvalidInput
	}
	if len(expires) > 1 {
		return nil, ErrInvalidInput
	}
	var limit *time.Time
	if len(expires) == 1 && expires[0] != nil {
		value := expires[0].UTC()
		limit = &value
	}
	if s.local {
		return nil, storage.ErrUnsupported
	}
	var recovered *ent.StorageAuthVersion
	// 受限恢复与其它执行者互斥:先排空已发出的远端调用,再推进执行代次。
	err := s.storage.ioGuard.Exclusive(func() error {
		return withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
			r, err := s.authorized(ctx, tx, actor, id)
			if err != nil {
				return err
			}
			target, err := retirementScope(r)
			if err != nil {
				return err
			}
			connID, err := retirementScopeConnection(target)
			if err != nil {
				return err
			}
			// 连接管理权限单独检查:退出编排不隐含连接管理以外的权限。
			c, err := s.connections.authorized(ctx, tx, actor, connID)
			if err != nil {
				return err
			}
			// 先算完整请求指纹并解析同键条目:已完成或部分完成的丢响应重放不能被
			// maintenance、代次/阶段、生命周期或候选过期变成另一个错误。
			fingerprint := retirementRecoveryFingerprint{
				Connection: c.ID, TargetIdentity: target.TargetIdentity,
				Generation: expectedGeneration, Management: expectedManagementGeneration,
				AccessKeyID: payload.AccessKeyID, SecretDigest: retirementRecoverySecretSHA256(payload),
			}
			if limit != nil {
				fingerprint.ExpiresAt = limit.Format(time.RFC3339Nano)
			}
			requestHash := retirementHash(fingerprint)
			entry, err := tx.StorageRetirementEntry.Query().Where(
				storageretiremententry.RetirementIDEQ(id),
				storageretiremententry.KindEQ(retirementRecoveryKind),
				storageretiremententry.IdempotencyKeyEQ(key),
			).Only(ctx)
			switch {
			case err == nil:
				if entry.RequestHash == nil || *entry.RequestHash != requestHash {
					return ErrStorageIdempotency
				}
				if entry.Status == storageretiremententry.StatusCompleted {
					return nil
				}
			case ent.IsNotFound(err):
			default:
				return err
			}
			if limit != nil && !limit.After(time.Now()) {
				return ErrInvalidInput
			}
			if s.connections.keys == nil {
				return ErrStorageCrypto
			}
			if err = storageguard.LockConnection(ctx, tx, connID); err != nil {
				return err
			}
			c, err = s.connections.authorized(ctx, tx, actor, connID)
			if err != nil {
				return err
			}
			if entry == nil && c.ManagementGeneration != expectedManagementGeneration {
				return ErrStorageConflict
			}
			if s.cfg.Maintenance {
				return ErrStorageMaintenance
			}
			if r.Generation != expectedGeneration || r.CancelRequested {
				return ErrStorageRetirementConflict
			}
			// waiting_configuration 起不再接受凭据恢复,也不再有任何远端 I/O。
			if r.Phase != storageretirement.PhaseFinalizing {
				return ErrStorageRetirementConflict
			}
			if target.ConfigurationSource == "deployment" {
				// 站点凭据由部署维护者在原 backend 配置中更新,不接受上传凭据。
				return ErrStorageRetirementConfigurationPending
			}
			if c.Lifecycle == storageconnection.LifecycleRemoved {
				return ErrStorageRemoved
			}
			if c.AuthSource != storageconnection.AuthSourceStored || c.Driver != storageconnection.DriverS3 {
				return ErrStorageRetirementConfigurationPending
			}
			// 显式禁用与全局维护的限制继续生效:需要相应管理者先恢复许可。
			if c.Status != storageconnection.StatusEnabled {
				return storage.ErrPermission
			}
			spaces, err := s.retirementRecoverySpaces(ctx, tx, r, c, target)
			if err != nil {
				return err
			}
			ids := make([]int, 0, len(spaces))
			for _, sp := range spaces {
				if sp.Status == storagespace.StatusDisabled {
					return storage.ErrPermission
				}
				ids = append(ids, sp.ID)
			}
			if s.storage.ioGuard.BusyLocked(ids) {
				return storageguard.ErrBusy
			}
			var priorGeneration int64
			if entry != nil {
				var draft recoveryDraft
				if len(entry.Result) > 0 {
					if e := json.Unmarshal(entry.Result, &draft); e != nil {
						return ErrStorageRetirementConflict
					}
				}
				priorGeneration = draft.Generation
			}
			candidate, err := s.retirementDraft(ctx, tx, r, c, priorGeneration, limit)
			if err != nil {
				return err
			}
			if candidate == nil {
				next, err := s.nextAuthGeneration(ctx, tx, c.ID)
				if err != nil {
					return err
				}
				value, err := storageauth.EncryptS3(s.connections.keys, storageAuthIdentity(c, next), payload)
				if err != nil {
					return ErrStorageCrypto
				}
				candidate, err = tx.StorageAuthVersion.Create().SetConnectionID(c.ID).SetGeneration(next).
					SetPayloadVersion(storageauth.PayloadVersion).SetAadVersion(storageauth.AADVersion).
					SetKeyID(value.KeyID).SetNonce(value.Nonce).SetCiphertext(value.Data).
					SetStatus(storageauthversion.StatusCandidate).SetPurpose(storageauthversion.PurposeRetirement).
					SetRetirementID(id).SetNillableExpiresAt(limit).Save(ctx)
				if err != nil {
					return err
				}
				// 立即记录草稿代次:重试复用同一份密文,不新建第二份。
				data, err := json.Marshal(recoveryDraft{Generation: candidate.Generation})
				if err != nil {
					return err
				}
				if entry == nil {
					if _, err = tx.StorageRetirementEntry.Create().SetRetirementID(id).
						SetEntryKey("recover:" + key).SetKind(retirementRecoveryKind).
						SetIdempotencyKey(key).SetRequestHash(requestHash).
						SetStatus(storageretiremententry.StatusRunning).SetResult(data).Save(ctx); err != nil {
						return err
					}
				} else if err = tx.StorageRetirementEntry.UpdateOneID(entry.ID).
					SetStatus(storageretiremententry.StatusRunning).SetErrorCode("").SetResult(data).Exec(ctx); err != nil {
					return err
				}
			}
			// 推进执行代次:此前绑定的执行器立即失去该身份的 I/O 许可。
			// 单空间退出不推进父连接代次,否则兄弟空间的旧 driver 会被误杀。
			if target.TargetKind == retirementKindConnection {
				if err = tx.StorageConnection.UpdateOneID(c.ID).AddManagementGeneration(1).AddExecutionGeneration(1).Exec(ctx); err != nil {
					return err
				}
			}
			for _, sp := range spaces {
				if err = tx.StorageSpace.UpdateOneID(sp.ID).AddExecutionGeneration(1).Exec(ctx); err != nil {
					return err
				}
			}
			recovered = candidate
			return recordAuditEvent(ctx, tx, AuditEvent{ActorUserID: actor, Action: "storage.retirement.recover_authorization", ResourceType: "storage_retirement", ResourceID: id})
		})
	})
	if err != nil {
		return nil, err
	}
	if recovered == nil {
		// 已完成的幂等重放:返回原任务,不重复核验或写入。
		return s.client.StorageRetirement.Get(ctx, id)
	}
	if err = s.verifyRecoveredCredentials(ctx, id, recovered); err != nil {
		// 未通过核验的候选立即回收,只留下非秘密的失败原因;条目保留以便重试。
		_ = s.failRetirementRecovery(ctx, id, key, err)
		_ = s.revokeRetirementCredential(ctx, recovered.ID)
		return nil, s.connections.sanitize(err)
	}
	if err = s.completeRetirementRecovery(ctx, id, key, recovered); err != nil {
		return nil, err
	}
	return s.client.StorageRetirement.Get(ctx, id)
}

// retirementDraft 找到先前尝试留下的受限草稿;已被回收或过期时返回 nil,
// 由调用方在推进之后的代次上重新签发。
func (s *StorageRetirementService) retirementDraft(ctx context.Context, tx *ent.Client, r *ent.StorageRetirement, c *ent.StorageConnection, generation int64, limit *time.Time) (*ent.StorageAuthVersion, error) {
	if generation <= 0 {
		return nil, nil
	}
	a, err := tx.StorageAuthVersion.Query().Where(
		storageauthversion.ConnectionIDEQ(c.ID),
		storageauthversion.GenerationEQ(generation),
		storageauthversion.PurposeEQ(storageauthversion.PurposeRetirement),
		storageauthversion.RetirementIDEQ(r.ID),
		storageauthversion.PurgedAtIsNil(),
		storageauthversion.StatusEQ(storageauthversion.StatusCandidate),
	).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if a.ExpiresAt != nil && !a.ExpiresAt.After(time.Now()) {
		return nil, nil
	}
	return a, nil
}

// retirementScopeConnection 从冻结的确认范围解析唯一连接身份;范围不允许跨连接。
func retirementScopeConnection(target RetirementTarget) (int, error) {
	if target.TargetKind == "connection" {
		return target.TargetID, nil
	}
	id := 0
	for _, sp := range target.Spaces {
		if id == 0 {
			id = sp.ConnectionID
			continue
		}
		if sp.ConnectionID != id {
			return 0, ErrStorageRetirementConflict
		}
	}
	if id == 0 {
		return 0, ErrStorageRetirementConflict
	}
	return id, nil
}

// retirementRecoverySpaces 复核持久空间身份与退出代次:恢复只覆盖原确认范围内、
// 仍由同一任务持有的空间,不扩展到后来出现的身份。
func (s *StorageRetirementService) retirementRecoverySpaces(ctx context.Context, tx *ent.Client, r *ent.StorageRetirement, c *ent.StorageConnection, target RetirementTarget) ([]*ent.StorageSpace, error) {
	// Recovery is scoped to the frozen confirmation facts, never to a fresh
	// connection-wide query. A single-space retirement therefore cannot gain
	// permission for sibling spaces, and sibling spaces cannot block recovery.
	if len(target.Spaces) == 0 || len(target.Spaces) > 100 {
		return nil, ErrStorageRetirementConflict
	}
	ids := make([]int, 0, len(target.Spaces))
	scoped := make(map[int]string, len(target.Spaces))
	for _, scope := range target.Spaces {
		if _, ok := scoped[scope.SpaceID]; ok {
			return nil, ErrStorageRetirementConflict
		}
		ids = append(ids, scope.SpaceID)
		scoped[scope.SpaceID] = scope.SpaceIdentity
	}
	spaces, err := tx.StorageSpace.Query().
		Where(storagespace.IDIn(ids...)).
		Order(ent.Asc(storagespace.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	if len(spaces) != len(ids) {
		return nil, ErrStorageRetirementConflict
	}
	for _, sp := range spaces {
		identity, ok := scoped[sp.ID]
		if !ok {
			return nil, ErrStorageRetirementConflict
		}
		if identity != "" && identity != sp.Identity {
			return nil, ErrStorageRetirementConflict
		}
		if sp.ConnectionID != c.ID || sp.RetirementID == nil || *sp.RetirementID != r.ID {
			return nil, ErrStorageRetirementConflict
		}
		if sp.Lifecycle != storagespace.LifecycleRetiring && sp.Lifecycle != storagespace.LifecycleRemoved {
			return nil, ErrStorageRetirementConflict
		}
	}
	return spaces, nil
}

func (s *StorageRetirementService) nextAuthGeneration(ctx context.Context, tx *ent.Client, connectionID int) (int64, error) {
	last, err := tx.StorageAuthVersion.Query().Where(storageauthversion.ConnectionIDEQ(connectionID)).
		Order(ent.Desc(storageauthversion.FieldGeneration)).First(ctx)
	if ent.IsNotFound(err) {
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	if !storageCanIncrement(last.Generation) {
		return 0, ErrInvalidInput
	}
	return last.Generation + 1, nil
}

// verifyRecoveredCredentials 只做只读核验:标记已按本任务确认删除的空间直接使用其
// 持久删除证据,标记仍在时只核对内容;绝不重建标记、不写入测试对象。
func (s *StorageRetirementService) verifyRecoveredCredentials(ctx context.Context, id int, recovered *ent.StorageAuthVersion) error {
	if recovered == nil {
		return ErrInvalidInput
	}
	r, err := s.client.StorageRetirement.Get(ctx, id)
	if err != nil {
		return err
	}
	target, err := retirementScope(r)
	if err != nil {
		return err
	}
	ids := make([]int, 0, len(target.Spaces))
	for _, scope := range target.Spaces {
		ids = append(ids, scope.SpaceID)
	}
	spaces, err := s.client.StorageSpace.Query().
		Where(storagespace.IDIn(ids...)).
		Order(ent.Asc(storagespace.FieldID)).All(ctx)
	if err != nil {
		return err
	}
	if len(spaces) != len(ids) {
		return ErrStorageRetirementConflict
	}
	for _, sp := range spaces {
		entry, err := s.markerEntry(ctx, id, sp.ID)
		if err != nil {
			return err
		}
		if err = s.verifyRecoveredMarker(ctx, r, sp, entry, recovered); err != nil {
			return err
		}
	}
	return nil
}

func (s *StorageRetirementService) verifyRecoveredMarker(ctx context.Context, r *ent.StorageRetirement, sp *ent.StorageSpace, entry *ent.StorageRetirementEntry, recovered *ent.StorageAuthVersion) error {
	if entry != nil && entry.Status == storageretiremententry.StatusCompleted {
		var evidence markerEvidence
		if err := json.Unmarshal(entry.Result, &evidence); err != nil || evidence.Identity != sp.Identity {
			return ErrStorageRetirementDependenciesRemaining
		}
		return nil
	}
	if !sp.Verified && sp.RemoteUseState == storagespace.RemoteUseStateNever {
		return nil
	}
	release, err := s.storage.ioGuard.Acquire(ctx, s.client, sp.ID, storageguard.Cleanup, r.ID, nil)
	if err != nil {
		return err
	}
	defer release()
	conn, err := s.client.StorageConnection.Get(ctx, sp.ConnectionID)
	if err != nil {
		return err
	}
	a, err := s.client.StorageAuthVersion.Get(ctx, recovered.ID)
	if err != nil {
		return err
	}
	if a.ConnectionID != conn.ID || a.RetirementID == nil || *a.RetirementID != r.ID || a.Purpose != storageauthversion.PurposeRetirement || a.Status != storageauthversion.StatusCandidate || a.PurgedAt != nil || (a.ExpiresAt != nil && !a.ExpiresAt.After(time.Now())) {
		return storage.ErrAuthRequired
	}
	payload, err := storageauth.DecryptS3(s.connections.keys, storageAuthIdentity(conn, a.Generation), storageCiphertext(a))
	if err != nil {
		return ErrStorageCrypto
	}
	if s.connections.factory == nil {
		return storage.ErrUnavailable
	}
	d, err := s.connections.factory(ctx, conn, sp, payload)
	if err != nil {
		return err
	}
	observed, err := d.Stat(ctx, storage.Object{Key: storageMarkerKey})
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return ErrStorageRetirementDependenciesRemaining
	case err != nil:
		return err
	}
	return readStorageProbe(ctx, d, observed, storageMarkerBytes(sp))
}

func (s *StorageRetirementService) failRetirementRecovery(ctx context.Context, id int, key string, cause error) error {
	code := StorageErrorCode(cause)
	data, err := json.Marshal(recoveryDraft{Outcome: "failed"})
	if err != nil {
		return err
	}
	_, err = s.client.StorageRetirementEntry.Update().Where(
		storageretiremententry.RetirementIDEQ(id),
		storageretiremententry.KindEQ(retirementRecoveryKind),
		storageretiremententry.IdempotencyKeyEQ(key),
	).SetStatus(storageretiremententry.StatusNeedsAction).SetErrorCode(code).SetResult(data).Save(ctx)
	return err
}

func (s *StorageRetirementService) completeRetirementRecovery(ctx context.Context, id int, key string, recovered *ent.StorageAuthVersion) error {
	data, err := json.Marshal(recoveryDraft{Generation: recovered.Generation, Outcome: "verified"})
	if err != nil {
		return err
	}
	return withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		n, err := tx.StorageAuthVersion.Update().Where(storageauthversion.IDEQ(recovered.ID), storageauthversion.StatusEQ(storageauthversion.StatusCandidate), storageauthversion.PurgedAtIsNil()).SetStatus(storageauthversion.StatusActive).Save(ctx)
		if err != nil {
			return err
		}
		if n != 1 {
			return storage.ErrAuthRequired
		}
		_, err = tx.StorageRetirementEntry.Update().Where(storageretiremententry.RetirementIDEQ(id), storageretiremententry.KindEQ(retirementRecoveryKind), storageretiremententry.IdempotencyKeyEQ(key)).SetStatus(storageretiremententry.StatusCompleted).SetErrorCode("").SetResult(data).Save(ctx)
		return err
	})
}

// revokeRetirementCredential 回收未被接受的受限密文:清空 nonce/ciphertext 并留下
// 非秘密的吊销审计,绝不留下可用的在线密文。
func (s *StorageRetirementService) revokeRetirementCredential(ctx context.Context, id int) error {
	err := s.client.StorageAuthVersion.UpdateOneID(id).
		SetStatus(storageauthversion.StatusRevoked).SetPurgedAt(time.Now().UTC()).
		ClearNonce().ClearCiphertext().Exec(ctx)
	return err
}
