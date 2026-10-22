package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/bloblocation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/deletionentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageauthversion"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagereservation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageretirement"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageretiremententry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagespace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagewrite"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storageauth"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storageguard"
)

// Finalize 完成第 18.6/18.7 节的最终封存:逐空间精确回收身份标记,然后提交 removed。
//
// 远端核验与回收只在 finalizing 阶段发生——那时空间标记的精确 I/O 由
// storageguard.Cleanup 许可按 retirement id 限定。进入 waiting_configuration 后
// 同一许可立即失效,因此等待部署配置期间不会发生任何远端 I/O。
func (s *StorageRetirementService) Finalize(ctx context.Context, id int) error {
	r, err := s.client.StorageRetirement.Get(ctx, id)
	if err != nil {
		return err
	}
	// 已终结的任务是幂等成功:重放只返回原结果,不重复远端核验。
	if r.Status == storageretirement.StatusCompleted || r.Status == storageretirement.StatusCancelled {
		return nil
	}
	if r.Phase != storageretirement.PhaseFinalizing && r.Phase != storageretirement.PhaseWaitingConfiguration {
		return ErrStorageRetirementConflict
	}
	if s.cfg.Maintenance {
		return ErrStorageMaintenance
	}
	target, err := retirementScope(r)
	if err != nil {
		return err
	}
	if r.Phase == storageretirement.PhaseFinalizing {
		for _, scoped := range target.Spaces {
			if err = s.reclaimSpaceMarker(ctx, r, scoped.SpaceID, scoped.SpaceIdentity); err != nil {
				return err
			}
		}
	}
	if target.ConfigurationSource == "deployment" {
		return s.completeDeployment(ctx, r, target)
	}
	return s.complete(ctx, id)
}

// completeDeployment 对站点 backend 先进入 waiting_configuration,期间保持路径占用
// 与退出屏障、零远端 I/O。只有服务实际加载到的配置中确实不存在原 backend_id 时
// 才提交 removed;提前撤下配置而清理未竟的情形已在标记回收处暴露为 needs_action。
func (s *StorageRetirementService) completeDeployment(ctx context.Context, r *ent.StorageRetirement, target RetirementTarget) error {
	if target.BackendID != nil && s.deploymentBackendLoaded(*target.BackendID) {
		if r.Phase != storageretirement.PhaseFinalizing {
			return nil
		}
		// 纯等待使用持久 next_check_at,不消耗失败重试预算。
		next := time.Now().UTC().Add(max(s.cfg.ReconcileInterval, time.Minute))
		return s.client.StorageRetirement.UpdateOneID(r.ID).
			SetPhase(storageretirement.PhaseWaitingConfiguration).
			SetStatus(storageretirement.StatusWaitingRetry).
			SetNextCheckAt(next).SetErrorCode("").Exec(ctx)
	}
	return s.complete(ctx, r.ID)
}

func (s *StorageRetirementService) deploymentBackendLoaded(id string) bool {
	for _, backend := range s.cfg.Backends {
		if backend.ID == id {
			return true
		}
	}
	return false
}

const storageMarkerKind = "marker"

func markerEntryKey(spaceID int) string { return fmt.Sprintf("marker:%d", spaceID) }

// markerEvidence 是标记条目的非秘密结果证据,用于重放与历史查询。
type markerEvidence struct {
	Outcome  string `json:"outcome"`
	Identity string `json:"space_identity"`
	Version  string `json:"provider_version,omitempty"`
}

func (s *StorageRetirementService) markerEntry(ctx context.Context, id, spaceID int) (*ent.StorageRetirementEntry, error) {
	e, err := s.client.StorageRetirementEntry.Query().
		Where(storageretiremententry.RetirementIDEQ(id), storageretiremententry.EntryKeyEQ(markerEntryKey(spaceID))).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return e, nil
}

// ensureMarkerEntry 在核验之前先持久登记该空间的标记条目。条目是重放依据:
// 崩溃或丢响应之后重新核验同一条目,而不是重建标记假装退出未曾发生。
func (s *StorageRetirementService) ensureMarkerEntry(ctx context.Context, id, spaceID int) (*ent.StorageRetirementEntry, error) {
	e, err := s.markerEntry(ctx, id, spaceID)
	if err != nil {
		return nil, err
	}
	if e != nil {
		if e.Kind != storageMarkerKind || e.SpaceID == nil || *e.SpaceID != spaceID {
			return nil, ErrStorageRetirementConflict
		}
		return e, nil
	}
	return s.client.StorageRetirementEntry.Create().
		SetRetirementID(id).SetEntryKey(markerEntryKey(spaceID)).SetKind(storageMarkerKind).
		SetSpaceID(spaceID).SetObjectKey(storageMarkerKey).SetStatus(storageretiremententry.StatusRunning).Save(ctx)
}

// registeredMarkerAbsent 只承认已登记的持久事实:创建期的 never 断言会在任何远端
// 准入之前被改写为 attempted,因此 never(且未验证)才是"从未发生远端尝试"的证据。
func registeredMarkerAbsent(sp *ent.StorageSpace, _ *ent.StorageConnection) bool {
	return !sp.Verified &&
		sp.RemoteUseState == storagespace.RemoteUseStateNever
}

// reclaimSpaceMarker 逐 space 回收持久身份标记(kind=marker, entry_key=marker:<spaceID>)。
//
// 固定顺序:先持久登记条目,再取得该空间在本次退出下的精确执行许可,之后核对
// identity+nonce 并只删除已登记的 key/version。身份不符、外部覆盖、陌生版本或删除
// 结果不明一律阻塞,绝不删除观察到的陌生对象。整段可重放。
func (s *StorageRetirementService) reclaimSpaceMarker(ctx context.Context, r *ent.StorageRetirement, spaceID int, expectedIdentity string) error {
	entry, err := s.ensureMarkerEntry(ctx, r.ID, spaceID)
	if err != nil {
		return err
	}
	if entry.Status == storageretiremententry.StatusCompleted {
		return nil
	}
	sp, err := s.client.StorageSpace.Get(ctx, spaceID)
	if err != nil {
		return err
	}
	conn, err := s.client.StorageConnection.Get(ctx, sp.ConnectionID)
	if err != nil {
		return err
	}
	if expectedIdentity != "" && sp.Identity != expectedIdentity {
		return fmt.Errorf("%w: space identity changed after confirmation", ErrStorageRetirementConflict)
	}
	if sp.Lifecycle == storagespace.LifecycleRemoved || conn.Lifecycle == storageconnection.LifecycleRemoved {
		// 同一任务已经终结该身份:持久移除事实本身就是证据,不再访问远端。
		return s.settleMarkerEntry(ctx, entry, markerEvidence{Outcome: "already_removed", Identity: sp.Identity}, "", nil)
	}
	registered, err := s.client.StorageWrite.Query().
		Where(storagewrite.SpaceIDEQ(spaceID), storagewrite.ObjectKeyEQ(storageMarkerKey)).Only(ctx)
	if ent.IsNotFound(err) {
		if registeredMarkerAbsent(sp, conn) {
			// 误建条目:已登记事实证明从未发生远端尝试,可无远端访问直接释放。
			return s.settleMarkerEntry(ctx, entry, markerEvidence{Outcome: "no_remote_attempt", Identity: sp.Identity}, "", nil)
		}
		return fmt.Errorf("%w: registered marker evidence missing", ErrStorageRetirementDependenciesRemaining)
	}
	if err != nil {
		return err
	}
	if err = s.client.StorageRetirementEntry.UpdateOneID(entry.ID).SetObjectKey(registered.ObjectKey).SetProviderVersion(registered.ProviderVersion).Exec(ctx); err != nil {
		return err
	}
	release, err := s.storage.ioGuard.Acquire(ctx, s.client, spaceID, storageguard.Cleanup, r.ID, nil)
	if err != nil {
		return err
	}
	observed, err := func() (string, error) {
		defer release()
		d, derr := s.retirementDriver(ctx, r, conn, sp)
		if derr != nil {
			return "", derr
		}
		return s.reclaimMarkerObject(ctx, d, sp, registered)
	}()
	if err != nil {
		return err
	}
	return s.settleMarkerEntry(ctx, entry, markerEvidence{Outcome: "reclaimed", Identity: sp.Identity, Version: observed}, observed, &registered.ID)
}

// reclaimMarkerObject 只按精确位置操作标记 key:枚举到的每个非删除标记都必须与本空间
// identity+nonce 逐字节一致,否则阻塞;删除之后必须观测到确实不存在。
func (s *StorageRetirementService) reclaimMarkerObject(ctx context.Context, d storage.Driver, sp *ent.StorageSpace, w *ent.StorageWrite) (string, error) {
	want := storageMarkerBytes(sp)
	objects := []storage.Object{{Key: storageMarkerKey, Version: w.ProviderVersion}}
	enumerated := false
	if sp.Versioned && w.ProviderVersion == "" {
		return "", ErrStorageRetirementDependenciesRemaining
	}
	if lister, ok := d.(storage.VersionLister); ok {
		versions, err := lister.Versions(ctx, storageMarkerKey)
		if err == nil {
			objects, enumerated = versions, true
		} else if sp.Versioned || !errors.Is(err, storage.ErrUnsupported) {
			return "", err
		}
	} else if sp.Versioned {
		return "", storage.ErrUnsupported
	}
	// 先完整核对再删除:任何一个陌生版本都能在删除发生之前阻塞整次回收。
	targets := make([]storage.Object, 0, len(objects))
	observed := ""
	for _, o := range objects {
		if o.Key != storageMarkerKey {
			return "", storage.ErrInvalidKey
		}
		if enumerated && (o.Version == "" || o.Version != w.ProviderVersion || o.DeleteMarker) {
			// 已登记写入之外的陌生位置必须阻塞,绝不按猜测删除。
			return "", fmt.Errorf("%w: unregistered marker version", ErrStorageRetirementDependenciesRemaining)
		}
		if !o.DeleteMarker {
			switch err := readStorageProbe(ctx, d, o, want); {
			case err == nil:
			case errors.Is(err, storage.ErrNotFound):
				continue
			case errors.Is(err, storage.ErrCorrupt):
				return "", fmt.Errorf("%w: marker identity mismatch", ErrStorageRetirementDependenciesRemaining)
			default:
				return "", err
			}
			if o.Version != "" {
				observed = o.Version
			}
		}
		targets = append(targets, o)
	}
	for _, o := range targets {
		if err := d.Delete(ctx, o); err != nil && !errors.Is(err, storage.ErrNotFound) {
			return "", err
		}
	}
	// 删除结果必须被观测确认;缺失也按驱动规则证实(Versions 必须为空)。
	if _, err := d.Stat(ctx, storage.Object{Key: storageMarkerKey}); !errors.Is(err, storage.ErrNotFound) {
		if err == nil {
			return "", fmt.Errorf("%w: marker survived deletion", ErrStorageRetirementDependenciesRemaining)
		}
		return "", err
	}
	if lister, ok := d.(storage.VersionLister); ok {
		remaining, err := lister.Versions(ctx, storageMarkerKey)
		if err == nil && len(remaining) > 0 {
			return "", fmt.Errorf("%w: marker versions survived deletion", ErrStorageRetirementDependenciesRemaining)
		}
		if err != nil && (sp.Versioned || !errors.Is(err, storage.ErrUnsupported)) {
			return "", err
		}
	}
	return observed, nil
}

// settleMarkerEntry 只在远端事实确认之后提交本地状态:条目完成、标记写入日志终结、
// 预留/活跃字节按其真实状态释放。已完成条目直接返回,因此整段可重放。
func (s *StorageRetirementService) settleMarkerEntry(ctx context.Context, entry *ent.StorageRetirementEntry, evidence markerEvidence, providerVersion string, writeID *int) error {
	data, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	return withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		current, err := tx.StorageRetirementEntry.Get(ctx, entry.ID)
		if err != nil {
			return err
		}
		if current.Status != storageretiremententry.StatusCompleted {
			update := tx.StorageRetirementEntry.UpdateOneID(entry.ID).
				SetStatus(storageretiremententry.StatusCompleted).SetResult(data).SetErrorCode("")
			if providerVersion != "" {
				update.SetProviderVersion(providerVersion).SetObjectKey(storageMarkerKey)
			}
			if err = update.Exec(ctx); err != nil {
				return err
			}
		}
		if writeID == nil {
			return nil
		}
		return s.releaseMarkerWrite(ctx, tx, *writeID, providerVersion)
	})
}

// releaseMarkerWrite 结算已登记的标记写入:对象删除已经确认,因此释放它占用的字节与
// 清理引用,并把标记写入日志推进到终态。已终结的写入不再重复结算。
func (s *StorageRetirementService) releaseMarkerWrite(ctx context.Context, tx *ent.Client, writeID int, version string) error {
	w, err := tx.StorageWrite.Get(ctx, writeID)
	if err != nil {
		return err
	}
	if w.Phase == "cleaned" {
		return nil
	}
	res, err := tx.StorageReservation.Query().Where(storagereservation.WriteIDEQ(writeID)).Only(ctx)
	if err != nil {
		return err
	}
	var release *ent.StorageSpaceUpdateOne
	switch res.State {
	case storagereservation.StateReserved:
		release = tx.StorageSpace.UpdateOneID(w.SpaceID).AddReservedBytes(-res.Bytes)
	case storagereservation.StateCandidate:
		release = tx.StorageSpace.UpdateOneID(w.SpaceID).AddCandidateBytes(-res.Bytes)
	case storagereservation.StateLive:
		release = tx.StorageSpace.UpdateOneID(w.SpaceID).AddLiveBytes(-res.Bytes)
	case storagereservation.StatePendingDelete:
		release = tx.StorageSpace.UpdateOneID(w.SpaceID).AddPendingDeleteBytes(-res.Bytes)
	case storagereservation.StateFreed:
	default:
		return ErrStorageConflict
	}
	if release != nil {
		if err = release.Exec(ctx); err != nil {
			return err
		}
		if err = tx.StorageReservation.UpdateOneID(res.ID).SetState(storagereservation.StateFreed).Exec(ctx); err != nil {
			return err
		}
	}
	if w.LocationID != nil {
		if err = tx.BlobLocation.UpdateOneID(*w.LocationID).SetStatus(bloblocation.StatusDeleted).Exec(ctx); err != nil {
			return err
		}
		if _, err = tx.DeletionEntry.Update().Where(deletionentry.LocationIDEQ(*w.LocationID)).
			SetStatus(deletionentry.StatusDone).SetErrorCode("").Save(ctx); err != nil {
			return err
		}
	}
	update := tx.StorageWrite.UpdateOneID(writeID).SetPhase("cleaned").SetOutcomeUnknown(false)
	if version != "" {
		update.SetProviderVersion(version)
	}
	if err = update.Exec(ctx); err != nil {
		return err
	}
	return tx.StorageTask.UpdateOneID(w.TaskID).
		SetPhase("cleaned").SetStatus(storagetask.StatusCompleted).
		SetCleanupStatus(storagetask.CleanupStatusDone).SetErrorCode("").Exec(ctx)
}

// retirementDriver 只解析该退出任务被许可使用的精确驱动:部署来源使用已加载配置,
// stored 来源优先使用本任务的受限恢复密文,否则沿用仍然有效的当前业务密文。
// 它绝不激活业务授权,也不写入任何探针或标记。
func (s *StorageRetirementService) retirementDriver(ctx context.Context, r *ent.StorageRetirement, conn *ent.StorageConnection, sp *ent.StorageSpace) (storage.Driver, error) {
	if s.connections.factory == nil {
		return nil, storage.ErrUnavailable
	}
	if conn.Driver != storageconnection.DriverS3 {
		return nil, storage.ErrUnsupported
	}
	payload, err := s.retirementPayload(ctx, r, conn, sp)
	if err != nil {
		return nil, err
	}
	return s.connections.factory(ctx, conn, sp, payload)
}

func (s *StorageRetirementService) retirementPayload(ctx context.Context, r *ent.StorageRetirement, conn *ent.StorageConnection, sp *ent.StorageSpace) (storageauth.S3Payload, error) {
	if conn.AuthSource == storageconnection.AuthSourceDeployment {
		if !s.deploymentBackendLoaded(conn.BackendID) {
			// 提前撤下部署配置使精确清理无法继续:需要部署维护者恢复原身份配置。
			return storageauth.S3Payload{}, ErrStorageRetirementConfigurationPending
		}
		return s.connections.deploymentCredentials(conn, sp)
	}
	if conn.AuthSource != storageconnection.AuthSourceStored {
		return storageauth.S3Payload{}, storage.ErrAuthRequired
	}
	// 同一任务的最新受限恢复密文优先;它只在本任务范围内有效。
	a, err := s.client.StorageAuthVersion.Query().Where(
		storageauthversion.ConnectionIDEQ(conn.ID),
		storageauthversion.RetirementIDEQ(r.ID),
		storageauthversion.PurposeEQ(storageauthversion.PurposeRetirement),
		storageauthversion.PurgedAtIsNil(),
		storageauthversion.StatusEQ(storageauthversion.StatusActive),
	).Order(ent.Desc(storageauthversion.FieldGeneration)).First(ctx)
	if err == nil {
		if a.ExpiresAt != nil && !a.ExpiresAt.After(time.Now()) {
			return storageauth.S3Payload{}, storage.ErrAuthRequired
		}
		if a.AadVersion != storageauth.AADVersion || a.PayloadVersion != storageauth.PayloadVersion {
			return storageauth.S3Payload{}, ErrStorageCrypto
		}
		payload, derr := storageauth.DecryptS3(s.connections.keys, storageAuthIdentity(conn, a.Generation), storageCiphertext(a))
		if derr != nil {
			return storageauth.S3Payload{}, ErrStorageCrypto
		}
		return payload, nil
	}
	if !ent.IsNotFound(err) {
		return storageauth.S3Payload{}, err
	}
	if conn.ActiveAuthGeneration == 0 {
		return storageauth.S3Payload{}, storage.ErrAuthRequired
	}
	_, payload, err := s.connections.loadAuth(ctx, conn, conn.ActiveAuthGeneration, storageauthversion.StatusActive)
	return payload, err
}

// purgeRetirementCredentials 在移除提交之后回收秘密材料。它保留非秘密的代次审计,
// 不删除共享 keyring、不触碰其它连接或 LLM 凭据,也不要求先成功解密历史密文。
func (s *StorageRetirementService) purgeRetirementCredentials(ctx context.Context, tx *ent.Client, r *ent.StorageRetirement) error {
	if r == nil {
		return ErrInvalidInput
	}
	target, err := retirementScope(r)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if target.TargetKind == "connection" {
		// 整个连接退出:该身份不再服务任何空间,因此回收所有代次的在线密文与 nonce,
		// 只保留 generation 等非秘密审计事实。active_auth_generation 由移除提交清除。
		if _, err = tx.StorageAuthVersion.Update().Where(
			storageauthversion.ConnectionIDEQ(target.TargetID),
			storageauthversion.PurgedAtIsNil(),
		).SetPurgedAt(now).SetStatus(storageauthversion.StatusRetired).
			ClearNonce().ClearCiphertext().Save(ctx); err != nil {
			return err
		}
	}
	// 本任务的受限专用密文总是回收,包括只退出部分空间的连接。
	_, err = tx.StorageAuthVersion.Update().Where(
		storageauthversion.RetirementIDEQ(r.ID),
		storageauthversion.PurgedAtIsNil(),
	).SetPurgedAt(now).SetStatus(storageauthversion.StatusRetired).
		ClearNonce().ClearCiphertext().Save(ctx)
	return err
}
