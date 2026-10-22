package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageretiremententry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagespace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

// 预检快照的有效期。过期或事实变化都必须重新预览,旧页面不得静默扩展范围。
const retirementPreviewLifetime = 5 * time.Minute

// retirementPreviewToken 是快照身份,不是授权凭证:它只绑定发起人、事实指纹与到期时间。
type retirementPreviewToken struct {
	Version     int    `json:"version"`
	Actor       int    `json:"actor"`
	Fingerprint string `json:"fingerprint"`
	ExpiresAt   int64  `json:"expires_at"`
}

func (s *StorageRetirementService) issuePreviewRevision(actor int, fingerprint string, expiresAt time.Time) (string, error) {
	payload, err := json.Marshal(retirementPreviewToken{Version: 1, Actor: actor, Fingerprint: fingerprint, ExpiresAt: expiresAt.UTC().Unix()})
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, s.previewKey[:])
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// decodePreviewRevision 校验 HMAC 后才解析载荷,任何失败都按失效处理。
func (s *StorageRetirementService) decodePreviewRevision(revision string) (retirementPreviewToken, error) {
	var token retirementPreviewToken
	parts := strings.Split(revision, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return token, ErrStorageRetirementPreviewStale
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return token, ErrStorageRetirementPreviewStale
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return token, ErrStorageRetirementPreviewStale
	}
	mac := hmac.New(sha256.New, s.previewKey[:])
	mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return token, ErrStorageRetirementPreviewStale
	}
	if err = json.Unmarshal(payload, &token); err != nil {
		return token, ErrStorageRetirementPreviewStale
	}
	if token.Version != 1 || token.Fingerprint == "" || token.ExpiresAt <= 0 {
		return token, ErrStorageRetirementPreviewStale
	}
	return token, nil
}

// verifyPreview 供确认事务使用:只比较签名、发起人、有效期与完整事实指纹。
// 它绝不授予权限,接受时仍会重查全部事实。
func (s *StorageRetirementService) verifyPreview(revision string, actor int, fingerprint string) error {
	token, err := s.decodePreviewRevision(revision)
	if err != nil {
		return err
	}
	if token.Actor != actor || token.Fingerprint != fingerprint {
		return ErrStorageRetirementPreviewStale
	}
	if !time.Unix(token.ExpiresAt, 0).After(time.Now()) {
		return ErrStorageRetirementPreviewStale
	}
	return nil
}

// Preview 是 18.3 的只读预检:单次一致事务内规范化目标、完整扫描依赖并逐项目独立鉴权。
// 它不探测远端、不产生探针、不改变任何状态,分页只作用于授权投影。
func (s *StorageRetirementService) Preview(ctx context.Context, actor int, in RetirementPreviewInput) (*RetirementPreview, error) {
	if s.local {
		return nil, storage.ErrUnsupported
	}
	kind := strings.TrimSpace(in.TargetKind)
	if (kind != retirementKindConnection && kind != retirementKindSpace) || in.TargetID < 1 {
		return nil, ErrInvalidInput
	}
	cursor, limit, err := retirementPageBounds(in.Cursor, in.Limit)
	if err != nil {
		return nil, err
	}
	if cursor > 0 && in.PreviewRevision == "" {
		// 续页必须携带同一快照,否则授权投影可能与前页拼出错误结论。
		return nil, ErrInvalidInput
	}
	plan := make([]RetirementProjectPlan, 0, len(in.Plan))
	plan = append(plan, in.Plan...)
	seen := map[int]bool{}
	for _, item := range plan {
		if item.ProjectID < 1 || item.TargetSpaceID < 1 || seen[item.ProjectID] {
			return nil, ErrInvalidInput
		}
		seen[item.ProjectID] = true
	}
	var out *RetirementPreview
	err = withRetirementPreviewTransaction(ctx, s, func(tx *ent.Client) error {
		target, err := s.normalizeTarget(ctx, tx, actor, kind, in.TargetID)
		if err != nil {
			return err
		}
		inv, err := s.inventory(ctx, tx, target)
		if err != nil {
			return err
		}
		scopedProjects := map[int]bool{}
		for _, row := range inv.Projects {
			scopedProjects[row.ID] = true
		}
		if err = retirementPreviewPlanCheck(target, scopedProjects, plan); err != nil {
			return err
		}
		fingerprint := retirementPreviewFingerprint(inv.Fingerprint, plan)
		revision := ""
		// 到期时间以秒为单位写入快照,续页必须返回完全相同的 expires_at。
		expiresAt := time.Now().UTC().Add(retirementPreviewLifetime).Truncate(time.Second)
		if cursor > 0 {
			if err = s.verifyPreview(in.PreviewRevision, actor, fingerprint); err != nil {
				return err
			}
			token, err := s.decodePreviewRevision(in.PreviewRevision)
			if err != nil {
				return err
			}
			revision = in.PreviewRevision
			expiresAt = time.Unix(token.ExpiresAt, 0).UTC()
		} else {
			revision, err = s.issuePreviewRevision(actor, fingerprint, expiresAt)
			if err != nil {
				return err
			}
		}
		availability, err := s.retirementTargetAvailability(ctx, tx, target)
		if err != nil {
			return err
		}
		projects, visible, hidden, err := s.retirementPreviewProjects(ctx, tx, actor, target, inv, plan)
		if err != nil {
			return err
		}
		page, err := retirementDependencyProjection(inv.Dependencies, visible, cursor, limit)
		if err != nil {
			return err
		}
		capability := "in_use_retirement"
		if !inv.HasUsage {
			capability = "unused_removal"
		}
		out = &RetirementPreview{
			ContractVersion:    StorageRetirementContractVersion,
			Target:             target,
			PreviewRevision:    revision,
			ExpiresAt:          expiresAt,
			RequiredCapability: capability,
			Availability:       availability,
			Projects:           projects,
			Dependencies:       page,
			Impact: RetirementImpact{
				SpaceCount:                  len(target.Spaces),
				ProjectCount:                len(inv.Projects),
				HiddenProjectCount:          hidden,
				ConfigurationActionRequired: target.ConfigurationSource == string(storageconnection.AuthSourceDeployment),
			},
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// withRetirementPreviewTransaction 使用只读事务建立一致快照:PostgreSQL 上以
// RepeatableRead ReadOnly 保证依赖扫描多次查询读到同一快照,SQLite 保持默认事务。
func withRetirementPreviewTransaction(ctx context.Context, s *StorageRetirementService, run func(tx *ent.Client) error) error {
	var tx *ent.Tx
	var err error
	if s.storage != nil && s.storage.dialect == "postgres" {
		tx, err = s.client.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	} else {
		tx, err = s.client.Tx(ctx)
	}
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = run(tx.Client()); err != nil {
		return err
	}
	return tx.Commit()
}

// retirementAssessment 只表达"当前判断是否允许",原因码保持稳定且已脱敏。
func retirementAssessment(allowed bool) StorageActionAvailability {
	if allowed {
		return StorageActionAvailability{Allowed: true, ReasonCodes: []string{}}
	}
	return storageAvailability([]string{"forbidden"})
}

// retirementTargetAvailability 描述该目标当前能否进入退出流程;预检本身不是远端能力保证。
func (s *StorageRetirementService) retirementTargetAvailability(ctx context.Context, client *ent.Client, target RetirementTarget) (StorageActionAvailability, error) {
	reasons := []string{}
	if s.cfg.Maintenance {
		reasons = append(reasons, "storage_maintenance")
	}
	// 空间目标的 TargetID 是空间 ID,连接必须经范围内空间的归一化结果解析,不能按目标 ID 直接取。
	connID := target.TargetID
	if target.TargetKind == retirementKindSpace {
		if len(target.Spaces) == 0 {
			return storageAvailability([]string{"storage_removed"}), nil
		}
		connID = target.Spaces[0].ConnectionID
	}
	conn, err := client.StorageConnection.Get(ctx, connID)
	if err != nil {
		if ent.IsNotFound(err) {
			return storageAvailability([]string{"storage_removed"}), nil
		}
		return StorageActionAvailability{}, err
	}
	switch conn.Lifecycle {
	case storageconnection.LifecycleRemoved:
		reasons = append(reasons, "storage_removed")
	case storageconnection.LifecycleRetiring:
		reasons = append(reasons, "storage_retirement_conflict")
	}
	spaces := make([]int, 0, len(target.Spaces))
	for _, sp := range target.Spaces {
		spaces = append(spaces, sp.SpaceID)
		row, err := client.StorageSpace.Get(ctx, sp.SpaceID)
		if err != nil {
			if ent.IsNotFound(err) {
				reasons = append(reasons, "storage_removed")
				continue
			}
			return StorageActionAvailability{}, err
		}
		switch row.Lifecycle {
		case storagespace.LifecycleRemoved:
			reasons = append(reasons, "storage_removed")
		case storagespace.LifecycleRetiring:
			reasons = append(reasons, "storage_retirement_conflict")
		}
	}
	// 已进入 cutover/cleanup 的迁入目标与协调器使用同一事实:退出必须先等待或取消迁移。
	if len(spaces) > 0 {
		incoming, err := client.StorageTask.Query().
			Where(storagetask.KindEQ("migration"), storagetask.TargetSpaceIDIn(spaces...), storagetask.PhaseIn("cutover", "cleanup"), storagetask.StatusNEQ(storagetask.StatusCompleted)).
			Exist(ctx)
		if err != nil {
			return StorageActionAvailability{}, err
		}
		if incoming {
			reasons = append(reasons, "storage_operation_in_progress")
		}
	}
	return storageAvailability(reasons), nil
}

// retirementProjectEntry 只读取已登记的计划事实;没有已接受范围时返回 nil。
func (s *StorageRetirementService) retirementProjectEntry(ctx context.Context, client *ent.Client, target RetirementTarget, projectID int) (*ent.StorageRetirementEntry, error) {
	if projectID < 1 {
		return nil, nil
	}
	ids := []int{}
	if target.TargetKind == retirementKindSpace && len(target.Spaces) == 1 {
		row, err := client.StorageSpace.Get(ctx, target.Spaces[0].SpaceID)
		if err != nil {
			if !ent.IsNotFound(err) {
				return nil, err
			}
		} else if row.RetirementID != nil {
			ids = append(ids, *row.RetirementID)
		}
	} else {
		conn, err := client.StorageConnection.Get(ctx, target.TargetID)

		if err != nil {
			if !ent.IsNotFound(err) {
				return nil, err
			}
		} else if conn.RetirementID != nil {
			ids = append(ids, *conn.RetirementID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	row, err := client.StorageRetirementEntry.Query().Where(storageretiremententry.RetirementIDIn(ids...), storageretiremententry.KindEQ("project"), storageretiremententry.ProjectIDEQ(projectID)).First(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return row, nil
}

// retirementPreviewProjects 只读评估调用方有权查看的项目;无权项目只贡献脱敏计数。
func (s *StorageRetirementService) retirementPreviewProjects(ctx context.Context, client *ent.Client, actor int, target RetirementTarget, inv *retirementInventory, plan []RetirementProjectPlan) ([]RetirementProjectAssessment, map[int]bool, int, error) {
	plans := map[int]RetirementProjectPlan{}
	for _, item := range plan {
		plans[item.ProjectID] = item
	}
	// 授权判定必须复用本次一致事务的客户端,否则会在单连接数据库上自锁死。
	access := &ProjectService{client: client, users: s.storage.projects.users, storage: s.storage}
	projects := make([]RetirementProjectAssessment, 0, len(inv.Projects))
	visible := map[int]bool{}
	sources := retirementSpaceSet(target)
	hidden := 0
	for _, row := range inv.Projects {
		entry, err := s.retirementProjectEntry(ctx, client, target, row.ID)
		if err != nil {
			return nil, nil, 0, err
		}
		accepted := entry != nil && entry.AcceptedAt != nil
		item := RetirementProjectPlan{ProjectID: row.ID}
		registered := false
		requested := false
		if entry != nil && entry.TargetSpaceID != nil {
			item = RetirementProjectPlan{ProjectID: row.ID, TargetSpaceID: *entry.TargetSpaceID, ExpectedStorageGeneration: entry.ExpectedStorageGeneration, ExpectedTargetManagementGeneration: entry.ExpectedTargetManagementGeneration, ExpectedTargetExecutionGeneration: entry.ExpectedTargetExecutionGeneration}
			registered = true
		} else if candidate, ok := plans[row.ID]; ok {
			item = candidate
			requested = true
		}
		if _, err := access.requireProjectAccess(ctx, actor, row.ID, false); err != nil {
			// 无权项目仅出现在按 kind 脱敏汇总的依赖里,绝不泄露身份或字节。
			hidden++
			continue
		}
		visible[row.ID] = true
		_, migrateErr := access.requireProjectAccess(ctx, actor, row.ID, true)
		assessment := RetirementProjectAssessment{
			ProjectID:   row.ID,
			Permissions: RetirementProjectPermissions{View: retirementAssessment(true), Migrate: retirementAssessment(migrateErr == nil), UpdatePlan: retirementAssessment(migrateErr == nil && !accepted)},
			TargetCheck: retirementPlanAssessment(ctx, client, row, item, requested, sources, accepted),
			Accepted:    accepted,
		}
		if registered {
			value := item
			assessment.Plan = &value
		}
		if entry != nil && entry.MigrationTaskID != nil {
			id := *entry.MigrationTaskID
			assessment.MigrationTaskID = &id
		}
		projects = append(projects, assessment)
	}
	return projects, visible, hidden, nil
}

// retirementPlanAssessment 只读检查目标是否符合项目归属、政策、容量、可写与代次要求。
func retirementPlanAssessment(ctx context.Context, client *ent.Client, row *ent.Project, item RetirementProjectPlan, requested bool, sources map[int]bool, accepted bool) StorageActionAvailability {
	if accepted {
		// 已接受子任务输入不可改写,当前事实变化由子任务自身报告。
		return retirementAssessment(true)
	}
	if item.TargetSpaceID < 1 {
		return storageAvailability([]string{retirementSelectionRequiredReason})
	}
	if sources[item.TargetSpaceID] {
		// 目标不能属于本次退出范围,也不能成为扩大范围的理由。
		return storageAvailability([]string{"storage_policy_violation"})
	}
	sp, err := client.StorageSpace.Get(ctx, item.TargetSpaceID)
	if err != nil {
		if ent.IsNotFound(err) {
			return storageAvailability([]string{"space_unverified"})
		}
		return storageAvailability([]string{"storage_unavailable"})
	}
	reasons := []string{}
	conn, err := client.StorageConnection.Get(ctx, sp.ConnectionID)
	if err != nil {
		reasons = append(reasons, "storage_unavailable")
	} else {
		if sp.OwnerKind != storagespace.OwnerKindSite {
			kind, owner := storageProjectOwner(row)
			if string(sp.OwnerKind) != kind || sp.OwnerID != owner {
				reasons = append(reasons, "policy_disallowed")
			}
		}
		switch conn.Lifecycle {
		case storageconnection.LifecycleRemoved:
			reasons = append(reasons, "storage_removed")
		case storageconnection.LifecycleRetiring:
			reasons = append(reasons, "storage_retiring")
		}
		if conn.Status != storageconnection.StatusEnabled {
			reasons = append(reasons, "connection_disabled")
		}
	}
	if !sp.Verified {
		reasons = append(reasons, "space_unverified")
	}
	switch sp.Status {
	case storagespace.StatusReadOnly:
		reasons = append(reasons, "space_read_only")
	case storagespace.StatusActive:
	default:
		reasons = append(reasons, "space_disabled")
	}
	if sp.Lifecycle == storagespace.LifecycleRemoved {
		reasons = append(reasons, "storage_removed")
	}
	if sp.Lifecycle == storagespace.LifecycleRetiring {
		reasons = append(reasons, "storage_retiring")
	}
	policy, policyErr := storagePolicy(ctx, client)
	if policyErr != nil {
		reasons = append(reasons, "storage_policy_violation")
	} else if !storagePolicyAllows(policy, row, sp) {
		reasons = append(reasons, "policy_disallowed")
	}
	size, sizeErr := storageMigrationSize(ctx, client, row.ID)
	if sizeErr != nil {
		reasons = append(reasons, "storage_unavailable")
	} else if available, quotaErr := storageAvailableBytes(sp); quotaErr != nil {
		reasons = append(reasons, "storage_quota_exceeded")
	} else if available != nil && ((size < 0 && *available == 0) || size > *available) {
		reasons = append(reasons, "storage_quota_exceeded")
	}
	if requested && (item.ExpectedTargetManagementGeneration != sp.ManagementGeneration || item.ExpectedTargetExecutionGeneration != sp.ExecutionGeneration) {
		reasons = append(reasons, "storage_generation_conflict")
	}
	if row.StorageState != "active" {
		reasons = append(reasons, "storage_operation_in_progress")
	}
	return storageAvailability(reasons)
}

// retirementPreviewPlanCheck 只读校验计划形状:计划只能描述范围内的项目。
func retirementPreviewPlanCheck(target RetirementTarget, projectIDs map[int]bool, plan []RetirementProjectPlan) error {
	sources := retirementSpaceSet(target)
	for _, item := range plan {
		if item.ProjectID < 1 || item.TargetSpaceID < 1 || !projectIDs[item.ProjectID] {
			return ErrInvalidInput
		}
		if sources[item.TargetSpaceID] {
			return ErrStoragePolicy
		}
	}
	return nil
}

// retirementDependencyProjection 把权威依赖投影为稳定游标分页;不可见项目按 kind 脱敏汇总。
// 空页、脱敏细节或分页未完成都不构成"可以安全移除"。
func retirementDependencyProjection(dependencies []RetirementDependency, visible map[int]bool, cursor, limit int) (RetirementDependencyPage, error) {
	projected := make([]RetirementDependency, 0, len(dependencies))
	aggregated := map[string]*RetirementDependency{}
	for _, dep := range dependencies {
		if dep.ProjectID > 0 && !visible[dep.ProjectID] {
			key := dep.Kind + "|" + dep.Status
			row, ok := aggregated[key]
			if !ok {
				row = &RetirementDependency{Kind: dep.Kind, Status: dep.Status, Redacted: true, ReasonCodes: []string{}}
				aggregated[key] = row
			}
			count := dep.Count
			if count < 1 {
				count = 1
			}
			row.Count += count
			for _, reason := range dep.ReasonCodes {
				row.ReasonCodes = appendReason(row.ReasonCodes, reason)
			}
			continue
		}
		projected = append(projected, dep)
	}
	keys := make([]string, 0, len(aggregated))
	for key := range aggregated {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		row := aggregated[key]
		if row.Count < 1 {
			row.Count = 1
		}
		sort.Strings(row.ReasonCodes)
		projected = append(projected, *row)
	}
	page := RetirementDependencyPage{Items: []RetirementDependency{}}
	if cursor > len(projected) {
		return page, ErrInvalidInput
	}
	end := cursor + limit
	if end > len(projected) {
		end = len(projected)
	}
	page.Items = append(page.Items, projected[cursor:end]...)
	page.HasMore = end < len(projected)
	if page.HasMore {
		next := end
		page.NextCursor = &next
	}
	return page, nil
}
