package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/backuppin"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/blob"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/bloblocation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/deletionentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/project"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagereservation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageretiremententry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagespace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagewrite"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

// 退出目标种类。站点/部署连接的空间入口会规范化到整个连接。
const (
	retirementKindConnection = "connection"
	retirementKindSpace      = "space"
)

// 依赖分类。marker 与 configuration 单列且不阻止 finalizing，其余普通依赖计入 Blocking。
const (
	retirementDependencyProjectBinding = "project_binding"
	retirementDependencyActiveLocation = "active_location"
	retirementDependencyCandidate      = "candidate"
	retirementDependencyProbe          = "probe"
	retirementDependencyReservation    = "reservation"
	retirementDependencyUnknownWrite   = "unknown_write"
	retirementDependencyDeletion       = "deletion"
	retirementDependencyExecution      = "execution_reference"
	retirementDependencyBackupPin      = "backup_pin"
	retirementDependencyDefaultPolicy  = "default_policy"
	retirementDependencyConfiguration  = "configuration"
	retirementDependencyMarker         = "marker"
)

const (
	retirementDependencyPendingReason       = "storage_retirement_dependencies_remaining"
	retirementDependencyConfigurationReason = "storage_retirement_configuration_pending"
	retirementSelectionRequiredReason       = "selection_required"
)

// RetirementTarget 是规范化后的完整确认范围，永不截断；调用方必须原样回显。
type RetirementTarget struct {
	TargetKind           string                 `json:"target_kind"`
	TargetID             int                    `json:"target_id"`
	TargetIdentity       string                 `json:"target_identity"`
	Scope                string                 `json:"scope"`
	OwnerID              int                    `json:"owner_id"`
	ConfigurationSource  string                 `json:"configuration_source"`
	BackendID            *string                `json:"backend_id"`
	ManagementGeneration int64                  `json:"management_generation"`
	ExecutionGeneration  int64                  `json:"execution_generation"`
	Spaces               []RetirementSpaceScope `json:"spaces"`
}

// RetirementSpaceScope 是范围内单个空间的身份与代次。
type RetirementSpaceScope struct {
	SpaceID              int    `json:"space_id"`
	SpaceIdentity        string `json:"space_identity"`
	ConnectionID         int    `json:"connection_id"`
	ManagementGeneration int64  `json:"management_generation"`
	ExecutionGeneration  int64  `json:"execution_generation"`
}

// RetirementProjectPlan 为单个项目指定唯一目标空间。
type RetirementProjectPlan struct {
	ProjectID                          int   `json:"project_id"`
	TargetSpaceID                      int   `json:"target_space_id"`
	ExpectedStorageGeneration          int64 `json:"expected_storage_generation"`
	ExpectedTargetManagementGeneration int64 `json:"expected_target_management_generation"`
	ExpectedTargetExecutionGeneration  int64 `json:"expected_target_execution_generation"`
}

// RetirementPreviewInput 是只读预检请求；cursor 存在时必须带 preview_revision。
type RetirementPreviewInput struct {
	TargetKind      string                  `json:"target_kind"`
	TargetID        int                     `json:"target_id"`
	Plan            []RetirementProjectPlan `json:"plan"`
	Cursor          int                     `json:"cursor"`
	Limit           int                     `json:"limit"`
	PreviewRevision string                  `json:"preview_revision"`
}

// RetirementPreview 是只读元数据快照，绝不是远端能力保证。
type RetirementPreview struct {
	ContractVersion    int                           `json:"contract_version"`
	Target             RetirementTarget              `json:"target"`
	PreviewRevision    string                        `json:"preview_revision"`
	ExpiresAt          time.Time                     `json:"expires_at"`
	RequiredCapability string                        `json:"required_capability"`
	Availability       StorageActionAvailability     `json:"availability"`
	Projects           []RetirementProjectAssessment `json:"projects"`
	Dependencies       RetirementDependencyPage      `json:"dependencies"`
	Impact             RetirementImpact              `json:"impact"`
}

// RetirementProjectAssessment 仅对调用方有权查看的项目返回。
type RetirementProjectAssessment struct {
	ProjectID       int                          `json:"project_id"`
	Plan            *RetirementProjectPlan       `json:"plan,omitempty"`
	Permissions     RetirementProjectPermissions `json:"permissions"`
	TargetCheck     StorageActionAvailability    `json:"target_check"`
	Accepted        bool                         `json:"accepted"`
	MigrationTaskID *int                         `json:"migration_task_id,omitempty"`
}

// RetirementProjectPermissions 分开表达查看、迁移与计划更新权限。
type RetirementProjectPermissions struct {
	View       StorageActionAvailability `json:"view"`
	Migrate    StorageActionAvailability `json:"migrate"`
	UpdatePlan StorageActionAvailability `json:"update_plan"`
}

// RetirementImpact 是影响摘要；project_count 为权威范围大小，隐藏项目只贡献计数。
type RetirementImpact struct {
	SpaceCount                  int  `json:"space_count"`
	ProjectCount                int  `json:"project_count"`
	HiddenProjectCount          int  `json:"hidden_project_count"`
	ConfigurationActionRequired bool `json:"configuration_action_required"`
}

// RetirementDependency 是元数据投影，绝不包含凭据、密文、对象键或提供方版本。
// query.go 的 Detail 直接序列化本类型，因此 kind/count/project_id 键名不可更改。
type RetirementDependency struct {
	Kind            string     `json:"kind"`
	Status          string     `json:"status"`
	Redacted        bool       `json:"redacted"`
	Count           int        `json:"count"`
	ProjectID       int        `json:"project_id,omitempty"`
	SpaceID         int        `json:"space_id,omitempty"`
	MigrationTaskID int        `json:"migration_task_id,omitempty"`
	ProtectedBytes  *int64     `json:"protected_bytes,omitempty"`
	NextCheckAt     *time.Time `json:"next_check_at,omitempty"`
	ReasonCodes     []string   `json:"reason_codes"`
}

// RetirementDependencyPage 是授权投影的稳定游标分页；空页永远不能证明可以安全移除。
type RetirementDependencyPage struct {
	Items      []RetirementDependency `json:"items"`
	NextCursor *int                   `json:"next_cursor"`
	HasMore    bool                   `json:"has_more"`
}

// retirementInventory 是权威依赖快照：分页只作用于授权投影，安全判定始终使用完整扫描。
type retirementInventory struct {
	Projects     []*ent.Project
	Dependencies []RetirementDependency
	Fingerprint  string
	Blocking     bool
	NextCheckAt  *time.Time
	HasUsage     bool
	identities   []string
}

// retirementDependencySet 按 (kind, 项目, 空间, 任务, 状态) 聚合依赖，保持确定性顺序。
type retirementDependencySet struct {
	items      map[string]*RetirementDependency
	identities map[string]bool
}

func newRetirementDependencySet() *retirementDependencySet {
	return &retirementDependencySet{items: map[string]*RetirementDependency{}, identities: map[string]bool{}}
}

func (s *retirementDependencySet) add(kind, status string, projectID, spaceID, migrationTaskID int, protectedBytes int64, nextCheckAt *time.Time, identity string, reasons ...string) {
	if identity != "" {
		s.identities[kind+"|"+identity] = true
	}
	key := fmt.Sprintf("%s|%d|%d|%d|%s", kind, projectID, spaceID, migrationTaskID, status)
	row, ok := s.items[key]
	if !ok {
		row = &RetirementDependency{Kind: kind, Status: status, ProjectID: projectID, SpaceID: spaceID, MigrationTaskID: migrationTaskID, ReasonCodes: []string{}}
		s.items[key] = row
	}
	row.Count++
	if protectedBytes > 0 {
		total := int64(0)
		if row.ProtectedBytes != nil {
			total = *row.ProtectedBytes
		}
		if protectedBytes > MaxStorageInteger-total {
			total = MaxStorageInteger
		} else {
			total += protectedBytes
		}
		row.ProtectedBytes = &total
	}
	if nextCheckAt != nil {
		value := nextCheckAt.UTC()
		if row.NextCheckAt == nil || value.Before(*row.NextCheckAt) {
			row.NextCheckAt = &value
		}
	}
	for _, reason := range reasons {
		if reason != "" {
			row.ReasonCodes = appendReason(row.ReasonCodes, reason)
		}
	}
}

// identityKeys 返回聚合内对象身份的确定性列表:指纹用它区分同数量换对象。
func (s *retirementDependencySet) identityKeys() []string {
	keys := make([]string, 0, len(s.identities))
	for key := range s.identities {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (s *retirementDependencySet) sorted() []RetirementDependency {
	rows := make([]RetirementDependency, 0, len(s.items))
	for _, row := range s.items {
		sort.Strings(row.ReasonCodes)
		rows = append(rows, *row)
	}
	sort.Slice(rows, func(i, j int) bool {
		return retirementDependencyOrderKey(rows[i]) < retirementDependencyOrderKey(rows[j])
	})
	return rows
}

func retirementDependencyOrderKey(d RetirementDependency) string {
	return fmt.Sprintf("%s|%09d|%09d|%09d|%s", d.Kind, d.ProjectID, d.SpaceID, d.MigrationTaskID, d.Status)
}

// retirementDependencyBlocks 只有普通依赖阻止封存；marker 与部署配置单列。
func retirementDependencyBlocks(dep RetirementDependency) bool {
	switch dep.Kind {
	case retirementDependencyMarker, retirementDependencyConfiguration:
		return false
	}
	return true
}

// retirementDependencyProvesUsage 区分"使用事实"与"配置事实"。
func retirementDependencyProvesUsage(dep RetirementDependency) bool {
	switch dep.Kind {
	case retirementDependencyConfiguration, retirementDependencyDefaultPolicy:
		return false
	}
	return true
}

func retirementSpaceSet(target RetirementTarget) map[int]bool {
	scope := map[int]bool{}
	for _, sp := range target.Spaces {
		scope[sp.SpaceID] = true
	}
	return scope
}

// normalizeTarget 只读规范化确认范围：站点空间入口展开为整个 backend 连接，
// BYOS 空间只覆盖自身，连接覆盖全部未移除子空间。
func (s *StorageRetirementService) normalizeTarget(ctx context.Context, client *ent.Client, actor int, kind string, id int) (RetirementTarget, error) {
	var target RetirementTarget
	if s.local || client == nil {
		return target, storage.ErrUnsupported
	}
	if id < 1 || (kind != retirementKindConnection && kind != retirementKindSpace) {
		return target, ErrInvalidInput
	}
	var conn *ent.StorageConnection
	var scopeSpace *ent.StorageSpace
	switch kind {
	case retirementKindConnection:
		row, err := s.connections.authorized(ctx, client, actor, id)
		if err != nil {
			return target, err
		}
		conn = row
	case retirementKindSpace:
		row, err := client.StorageSpace.Get(ctx, id)
		if ent.IsNotFound(err) {
			return target, storage.ErrUnavailable
		}
		if err != nil {
			return target, err
		}
		if row.Lifecycle == storagespace.LifecycleRemoved {
			return target, ErrStorageRemoved
		}
		owner, err := s.connections.authorized(ctx, client, actor, row.ConnectionID)
		if err != nil {
			return target, err
		}
		conn, scopeSpace = owner, row
	}
	if conn.Lifecycle == storageconnection.LifecycleRemoved {
		return target, ErrStorageRemoved
	}
	if conn.Driver != storageconnection.DriverS3 {
		// 本地文件系统根、启动默认空间与本地模式不随本轮开放退出。
		return target, storage.ErrUnsupported
	}
	deployment := conn.AuthSource == storageconnection.AuthSourceDeployment
	spaces := []*ent.StorageSpace{}
	if deployment {
		// 站点 S3 以 backend 为完整范围：任何子空间入口都返回同一个连接目标。
		rows, err := client.StorageSpace.Query().Where(storagespace.ConnectionIDEQ(conn.ID), storagespace.LifecycleNEQ(storagespace.LifecycleRemoved)).Order(ent.Asc(storagespace.FieldID)).All(ctx)
		if err != nil {
			return target, err
		}
		if len(rows) > 1 {
			// 与"一个 backend 一个有效子空间"的配置模型不符，先对账而不是任选其一。
			return target, ErrStorageRetirementConflict
		}
		spaces = rows
	} else if kind == retirementKindSpace {
		spaces = []*ent.StorageSpace{scopeSpace}
	} else {
		rows, err := client.StorageSpace.Query().Where(storagespace.ConnectionIDEQ(conn.ID), storagespace.LifecycleNEQ(storagespace.LifecycleRemoved)).Order(ent.Asc(storagespace.FieldID)).All(ctx)
		if err != nil {
			return target, err
		}
		spaces = rows
	}
	target = RetirementTarget{
		TargetKind:           kind,
		TargetID:             id,
		Scope:                string(conn.OwnerKind),
		OwnerID:              conn.OwnerID,
		ConfigurationSource:  string(conn.AuthSource),
		ManagementGeneration: conn.ManagementGeneration,
		ExecutionGeneration:  conn.ExecutionGeneration,
		Spaces:               []RetirementSpaceScope{},
	}
	if deployment {
		target.TargetKind = retirementKindConnection
		target.TargetID = conn.ID
		target.TargetIdentity = retirementConnectionIdentity(conn.ID)
		if strings.TrimSpace(conn.BackendID) == "" {
			// 部署身份必须显式携带 backend_id,否则无法在确认时核验配置撤下。
			return target, ErrStorageRetirementConfigurationPending
		}
		backend := conn.BackendID
		target.BackendID = &backend
	} else if kind == retirementKindConnection {
		target.TargetIdentity = retirementConnectionIdentity(conn.ID)
	} else {
		target.TargetIdentity = scopeSpace.Identity
	}
	for _, sp := range spaces {
		target.Spaces = append(target.Spaces, RetirementSpaceScope{
			SpaceID:              sp.ID,
			SpaceIdentity:        sp.Identity,
			ConnectionID:         sp.ConnectionID,
			ManagementGeneration: sp.ManagementGeneration,
			ExecutionGeneration:  sp.ExecutionGeneration,
		})
	}
	sort.Slice(target.Spaces, func(i, j int) bool { return target.Spaces[i].SpaceID < target.Spaces[j].SpaceID })
	return target, nil
}

// retirementConnectionIdentity 是不透明且不可变的连接身份，绝不是提供方路径。
func retirementConnectionIdentity(id int) string {
	return "connection:" + strconv.Itoa(id)
}

// retirementPreviewFingerprint 把确认目标与权威事实指纹绑定为同一个快照身份。
// 计划顺序不影响指纹，缺失计划与空计划等价。
func retirementPreviewFingerprint(fingerprint string, plan []RetirementProjectPlan) string {
	normalized := make([]RetirementProjectPlan, 0, len(plan))
	normalized = append(normalized, plan...)
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].ProjectID != normalized[j].ProjectID {
			return normalized[i].ProjectID < normalized[j].ProjectID
		}
		return normalized[i].TargetSpaceID < normalized[j].TargetSpaceID
	})
	return retirementHash(struct {
		Inventory string                  `json:"inventory"`
		Plan      []RetirementProjectPlan `json:"plan"`
	}{fingerprint, normalized})
}

// retirementScopeSpaces 是权威阻断范围:规范化目标已经包含完整范围(站点连接入口会展开为整个
// backend 的全部有效子空间),因此这里只做确定性排序,绝不按当前 storage_space_id 重新推断范围,
// 也绝不把 BYOS 单空间退出扩大到同连接的兄弟空间。
func (s *StorageRetirementService) retirementScopeSpaces(target RetirementTarget) []int {
	return sortedIntKeys(retirementSpaceSet(target))
}

func sortedIntKeys(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Ints(out)
	return out
}

// retirementKeyedProjects 按 BlobLocation / DeletionEntry 找出真实仍绑定该范围的项目。
func (s *StorageRetirementService) retirementKeyedProjects(ctx context.Context, client *ent.Client, spaces []int) (map[int]bool, error) {
	found := map[int]bool{}
	if len(spaces) == 0 {
		return found, nil
	}
	locations, err := client.BlobLocation.Query().Where(bloblocation.SpaceIDIn(spaces...), bloblocation.StatusNotIn(bloblocation.StatusDeleted)).Select(bloblocation.FieldBlobID).All(ctx)
	if err != nil {
		return nil, err
	}
	blobIDs := make([]int, 0, len(locations))
	for _, location := range locations {
		if location.BlobID != nil {
			blobIDs = append(blobIDs, *location.BlobID)
		}
	}
	if len(blobIDs) > 0 {
		blobs, err := client.Blob.Query().Where(blob.IDIn(blobIDs...)).Select(blob.FieldID, blob.FieldProjectID).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range blobs {
			if row.ProjectID > 0 {
				found[row.ProjectID] = true
			}
		}
	}
	entries, err := client.DeletionEntry.Query().Where(deletionentry.ProjectIDGT(0), deletionentry.StatusNEQ(deletionentry.StatusDone), deletionentry.HasLocationWith(bloblocation.SpaceIDIn(spaces...))).Select(deletionentry.FieldProjectID).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		found[entry.ProjectID] = true
	}
	return found, nil
}

// retirementScopeProjects 收集权威影响范围:当前绑定、真实对象位置、清理记录与在途任务。
func (s *StorageRetirementService) retirementScopeProjects(ctx context.Context, client *ent.Client, conn *ent.StorageConnection, spaceRows []*ent.StorageSpace, spaces []int) (map[int]bool, error) {
	projects := map[int]bool{}
	if len(spaces) == 0 {
		return projects, nil
	}
	keyed, err := s.retirementKeyedProjects(ctx, client, spaces)
	if err != nil {
		return nil, err
	}
	for id := range keyed {
		projects[id] = true
	}
	bound, err := client.Project.Query().Where(project.StorageSpaceIDIn(spaces...)).Select(project.FieldID).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range bound {
		projects[row.ID] = true
	}
	// 仍在向该范围写入或在途的迁移任务同样构成影响,即使项目已换绑。
	tasks, err := client.StorageTask.Query().Where(storagetask.TargetSpaceIDIn(spaces...), storagetask.ProjectIDGT(0), storagetask.StatusNotIn(storagetask.StatusCompleted, storagetask.StatusCancelled, storagetask.StatusFailed)).Select(storagetask.FieldProjectID).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, task := range tasks {
		projects[task.ProjectID] = true
	}
	// 已登记的退出条目是权威范围事实,不能因为当前绑定已改变而遗漏。
	retirements := make([]int, 0, len(spaces))
	for _, row := range spaceRows {
		if row.RetirementID != nil {
			retirements = append(retirements, *row.RetirementID)
		}
	}
	if conn != nil && conn.RetirementID != nil {
		retirements = append(retirements, *conn.RetirementID)
	}
	if len(retirements) > 0 {
		entries, err := client.StorageRetirementEntry.Query().Where(storageretiremententry.RetirementIDIn(retirements...), storageretiremententry.KindEQ("project"), storageretiremententry.ProjectIDGT(0)).Select(storageretiremententry.FieldProjectID).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			projects[entry.ProjectID] = true
		}
	}
	return projects, nil
}

func retirementContains(values []int, want int) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// inventory 是权威只读依赖扫描。它不做任何写入、不发起远端请求、不读取协调器忙碌状态
// (主线程会在 ioGuard.Exclusive 内调用本函数)。分页只应用于授权投影,不影响此处的完整判定。
func (s *StorageRetirementService) inventory(ctx context.Context, client *ent.Client, target RetirementTarget) (*retirementInventory, error) {
	if s.local || client == nil {
		return nil, storage.ErrUnsupported
	}
	connID := target.TargetID
	if target.TargetKind == retirementKindSpace && len(target.Spaces) == 1 {
		connID = target.Spaces[0].ConnectionID
	}
	conn, err := client.StorageConnection.Get(ctx, connID)
	if err != nil {
		return nil, err
	}
	inv := &retirementInventory{Projects: []*ent.Project{}, Dependencies: []RetirementDependency{}, HasUsage: false}
	for _, sp := range target.Spaces {
		if sp.ConnectionID != conn.ID {
			return nil, ErrStorageRetirementConflict
		}
	}
	spaces := s.retirementScopeSpaces(target)
	scope := retirementSpaceSet(target)
	now := time.Now().UTC()
	deps := newRetirementDependencySet()
	var nextCheck *time.Time
	observe := func(candidate *time.Time) {
		if candidate == nil {
			return
		}
		value := candidate.UTC()
		if nextCheck == nil || value.Before(*nextCheck) {
			nextCheck = &value
		}
	}

	// 空间身份与远端使用事实:unknown 绝不是"从未使用"。
	spaceRows, err := client.StorageSpace.Query().Where(storagespace.IDIn(spaces...)).Order(ent.Asc(storagespace.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range spaceRows {
		if row.RemoteUseState != storagespace.RemoteUseStateNever {
			inv.HasUsage = true
		}
	}
	inv.HasUsage = inv.HasUsage || conn.RemoteUseState != storageconnection.RemoteUseStateNever

	projectIDs, err := s.retirementScopeProjects(ctx, client, conn, spaceRows, spaces)
	if err != nil {
		return nil, err
	}
	ids := sortedIntKeys(projectIDs)
	tasks, err := client.StorageTask.Query().Where(storagetask.Or(storagetask.TargetSpaceIDIn(spaces...), storagetask.ProjectIDIn(ids...))).Order(ent.Asc(storagetask.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, task := range tasks {
		if task.ProjectID > 0 {
			projectIDs[task.ProjectID] = true
		}
	}
	ids = sortedIntKeys(projectIDs)
	projects := make([]*ent.Project, 0, len(ids))
	if len(ids) > 0 {
		projects, err = client.Project.Query().Where(project.IDIn(ids...)).Order(ent.Asc(project.FieldID)).All(ctx)
		if err != nil {
			return nil, err
		}
	}
	inv.Projects = projects

	// 项目默认绑定:项目名称、资源与任务内容都不进入依赖投影。
	activeTask := map[int]*ent.StorageTask{}
	for _, task := range tasks {
		if task.ProjectID <= 0 {
			continue
		}
		if task.Status == storagetask.StatusCompleted || task.Status == storagetask.StatusCancelled || task.Status == storagetask.StatusFailed {
			continue
		}
		current, ok := activeTask[task.ProjectID]
		if !ok || task.UpdatedAt.Before(current.UpdatedAt) {
			activeTask[task.ProjectID] = task
		}
	}
	for _, row := range projects {
		if row.StorageSpaceID == nil || !scope[*row.StorageSpaceID] {
			continue
		}
		var wait *time.Time
		if task, ok := activeTask[row.ID]; ok {
			observe(task.NextRetryAt)
			wait = task.NextRetryAt
		}
		inv.HasUsage = true
		deps.add(retirementDependencyProjectBinding, "needs_action", row.ID, *row.StorageSpaceID, 0, 0, wait, strconv.Itoa(row.ID), retirementDependencyPendingReason)
	}
	// 探针与持久身份标记是独立的写入事实,先归类以便把它们的对象排除在普通依赖之外。
	probeWrites, err := client.StorageWrite.Query().Where(storagewrite.SpaceIDIn(spaces...), storagewrite.HasTaskWith(storagetask.KindIn("storage_probe", "storage_marker"))).WithTask().Order(ent.Asc(storagewrite.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	probeLocationIDs := map[int]bool{}
	for _, write := range probeWrites {
		if write.LocationID != nil {
			probeLocationIDs[*write.LocationID] = true
		}
	}

	// 项目真实对象位置:活动位置与候选位置都必须迁移或清理。
	locations, err := client.BlobLocation.Query().Where(bloblocation.SpaceIDIn(spaces...), bloblocation.StatusNotIn(bloblocation.StatusDeleted)).Order(ent.Asc(bloblocation.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	locationByID := map[int]*ent.BlobLocation{}
	locationBlobIDs := make([]int, 0, len(locations))
	for _, location := range locations {
		locationByID[location.ID] = location
		if location.BlobID != nil {
			locationBlobIDs = append(locationBlobIDs, *location.BlobID)
		}
	}
	blobToProject := map[int]int{}
	if len(locationBlobIDs) > 0 {
		rows, err := client.Blob.Query().Where(blob.IDIn(locationBlobIDs...)).Select(blob.FieldID, blob.FieldProjectID).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			blobToProject[row.ID] = row.ProjectID
		}
	}
	for _, location := range locations {
		if probeLocationIDs[location.ID] {
			// 探针/标记对象由退出协调器按精确 key 回收,不重复计入普通依赖。
			continue
		}
		pid := 0
		if location.BlobID != nil {
			pid = blobToProject[*location.BlobID]
		}
		kind := retirementDependencyActiveLocation
		if location.Status == bloblocation.StatusCandidate {
			kind = retirementDependencyCandidate
		}
		inv.HasUsage = true
		observe(location.RetainUntil)
		deps.add(kind, "pending", pid, location.SpaceID, 0, location.Size, location.RetainUntil, strconv.Itoa(location.ID), retirementDependencyPendingReason)
	}

	// 预留与结果不明写入:仅看账本合计为零不构成证明。
	writes, err := client.StorageWrite.Query().Where(storagewrite.SpaceIDIn(spaces...), storagewrite.PhaseNotIn("committed", "cleaned")).WithTask().Order(ent.Asc(storagewrite.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	businessWriteIDs := make([]int, 0, len(writes))
	for _, write := range writes {
		if write.Edges.Task != nil && (write.Edges.Task.Kind == "storage_probe" || write.Edges.Task.Kind == "storage_marker") {
			continue
		}
		businessWriteIDs = append(businessWriteIDs, write.ID)
	}
	reservations := map[int]*ent.StorageReservation{}
	if len(businessWriteIDs) > 0 {
		rows, err := client.StorageReservation.Query().Where(storagereservation.WriteIDIn(businessWriteIDs...)).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			reservations[row.WriteID] = row
		}
	}
	for _, write := range writes {
		if write.Edges.Task != nil && (write.Edges.Task.Kind == "storage_probe" || write.Edges.Task.Kind == "storage_marker") {
			continue
		}
		pid := 0
		if write.Edges.Task != nil {
			pid = write.Edges.Task.ProjectID
		}
		status := "pending"
		if write.OutcomeUnknown {
			status = "needs_action"
		}
		inv.HasUsage = true
		observe(write.ExpiresAt)
		deps.add(retirementDependencyUnknownWrite, status, pid, write.SpaceID, write.TaskID, write.ActualBytes, write.ExpiresAt, strconv.Itoa(write.ID), retirementDependencyPendingReason)
		if row, ok := reservations[write.ID]; ok && row.State != storagereservation.StateFreed {
			deps.add(retirementDependencyReservation, "pending", pid, write.SpaceID, write.TaskID, row.Bytes, write.ExpiresAt, strconv.Itoa(row.ID), retirementDependencyPendingReason)
		}
	}

	// 持久身份标记单列:它由退出协调器最后回收,不阻止 finalizing,但账本必须核对。
	markerRecovered, err := s.retirementMarkerLedger(ctx, client, spaces)
	if err != nil {
		return nil, err
	}
	for _, write := range probeWrites {
		kind := ""
		pid := 0
		if write.Edges.Task != nil {
			kind = write.Edges.Task.Kind
			pid = write.Edges.Task.ProjectID
		}
		if kind == "storage_marker" {
			inv.HasUsage = true
			status := "pending"
			reasons := []string{retirementDependencyPendingReason}
			if markerRecoveredBy(write, markerRecovered) {
				status = "completed"
				reasons = nil
			}
			observe(write.ExpiresAt)
			deps.add(retirementDependencyMarker, status, pid, write.SpaceID, write.TaskID, 0, write.ExpiresAt, strconv.Itoa(write.ID), reasons...)
			if write.Phase != "committed" && write.Phase != "cleaned" {
				// 未终结的标记尝试仍是结果不明的远端写入,必须按原意图对账。
				deps.add(retirementDependencyProbe, "needs_action", pid, write.SpaceID, write.TaskID, write.ActualBytes, write.ExpiresAt, strconv.Itoa(write.ID), retirementDependencyPendingReason)
			}
			continue
		}
		if write.Phase == "committed" || write.Phase == "cleaned" {
			// 已经终结的探针不构成依赖。
			continue
		}
		inv.HasUsage = true
		observe(write.ExpiresAt)
		deps.add(retirementDependencyProbe, "needs_action", pid, write.SpaceID, write.TaskID, write.ActualBytes, write.ExpiresAt, strconv.Itoa(write.ID), retirementDependencyPendingReason)
	}
	// 精确清理记录:403、失去版本查询能力与响应丢失都不能报告成功。
	entries, err := client.DeletionEntry.Query().Where(deletionentry.StatusNEQ(deletionentry.StatusDone), deletionentry.HasLocationWith(bloblocation.SpaceIDIn(spaces...))).Order(ent.Asc(deletionentry.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	if err = retirementFillLocations(ctx, client, locationByID, entries, nil); err != nil {
		return nil, err
	}
	for _, entry := range entries {
		spaceID := 0
		if location := locationByID[entry.LocationID]; location != nil {
			spaceID = location.SpaceID
		}
		inv.HasUsage = true
		status := "pending"
		if entry.Status == deletionentry.StatusBlocked || entry.ErrorCode != "" {
			status = "needs_action"
		}
		observe(entry.NextRetryAt)
		if entry.NotBefore.After(now) {
			observe(&entry.NotBefore)
		}
		reasons := []string{retirementDependencyPendingReason}
		if entry.Status == deletionentry.StatusBlocked {
			reasons = append(reasons, storageCode(errors.New(entry.ErrorCode)))
		}
		deps.add(retirementDependencyDeletion, status, entry.ProjectID, spaceID, 0, 0, entry.NextRetryAt, strconv.Itoa(entry.ID), reasons...)
	}

	// 读取、签名与其他执行引用:租约过期的旧 worker 也仍然计入,直到确认结束。
	for _, task := range tasks {
		if task.Kind == "storage_probe" || task.Kind == "storage_marker" {
			continue
		}
		if task.Status == storagetask.StatusCompleted || task.Status == storagetask.StatusCancelled || task.Status == storagetask.StatusFailed {
			continue
		}
		if !(task.ProjectID > 0 && projectIDs[task.ProjectID]) && (task.TargetSpaceID == nil || !retirementContains(spaces, *task.TargetSpaceID)) {
			continue
		}
		spaceID := 0
		if task.TargetSpaceID != nil {
			spaceID = *task.TargetSpaceID
		}
		status := "pending"
		if task.Status == storagetask.StatusNeedsAction {
			status = "needs_action"
		}
		inv.HasUsage = true
		observe(task.NextRetryAt)
		deps.add(retirementDependencyExecution, status, task.ProjectID, spaceID, task.ID, 0, task.NextRetryAt, strconv.Itoa(task.ID), retirementDependencyPendingReason)
	}

	// BackupPin:已有保护按原期限保护,不能提前到期或改写原备份清单。
	pins, err := client.BackupPin.Query().Where(backuppin.ExpiresAtGT(now), backuppin.HasLocationWith(bloblocation.SpaceIDIn(spaces...))).All(ctx)
	if err != nil {
		return nil, err
	}
	if err = retirementFillLocations(ctx, client, locationByID, nil, pins); err != nil {
		return nil, err
	}
	for _, pin := range pins {
		spaceID := 0
		projectID := 0
		protected := int64(0)
		if location := locationByID[pin.LocationID]; location != nil {
			spaceID = location.SpaceID
			protected = location.Size
			if location.BlobID != nil {
				projectID = blobToProject[*location.BlobID]
			}
		}
		inv.HasUsage = true
		expires := pin.ExpiresAt
		observe(&expires)
		deps.add(retirementDependencyBackupPin, "waiting_retry", projectID, spaceID, 0, protected, &expires, strconv.Itoa(pin.ID), retirementDependencyPendingReason)
	}

	// 默认政策与配置来源:退出请求不顺带改写政策,部署配置撤下需要实际加载配置核验。
	if s.storage.defaultSpaceID > 0 && scope[s.storage.defaultSpaceID] {
		deps.add(retirementDependencyDefaultPolicy, "needs_action", 0, s.storage.defaultSpaceID, 0, 0, nil, "", "policy_disallowed")
	}
	if target.ConfigurationSource == string(storageconnection.AuthSourceDeployment) {
		if target.BackendID != nil && *target.BackendID == s.cfg.DefaultSiteSpace {
			deps.add(retirementDependencyDefaultPolicy, "needs_action", 0, 0, 0, 0, nil, "", retirementSelectionRequiredReason)
		}
		deps.add(retirementDependencyConfiguration, "pending", 0, 0, 0, 0, nil, "", retirementDependencyConfigurationReason)
	}

	inv.Dependencies = deps.sorted()
	inv.identities = deps.identityKeys()
	for _, dep := range inv.Dependencies {
		if retirementDependencyBlocks(dep) {
			inv.Blocking = true
		}
		if retirementDependencyProvesUsage(dep) {
			inv.HasUsage = true
		}
	}
	if !inv.Blocking {
		balanced, err := retirementFinalLedgerBalanced(ctx, client, spaceRows, probeWrites, markerRecovered)
		if err != nil {
			return nil, err
		}
		if !balanced {
			deps.add(retirementDependencyReservation, "needs_action", 0, 0, 0, 0, nil, "ledger_mismatch", retirementDependencyPendingReason)
			inv.Dependencies = deps.sorted()
			inv.Blocking = true
			inv.HasUsage = true
		}
	}
	for _, sp := range spaceRows {
		inv.identities = append(inv.identities, fmt.Sprintf("ledger:%d:%d:%d:%d:%d", sp.ID, sp.ReservedBytes, sp.CandidateBytes, sp.LiveBytes, sp.PendingDeleteBytes))
	}
	if nextCheck != nil && nextCheck.After(now) {
		value := *nextCheck
		inv.NextCheckAt = &value
	}
	fingerprint, err := s.retirementInventoryFingerprint(ctx, client, target, conn, inv)
	if err != nil {
		return nil, err
	}
	inv.Fingerprint = fingerprint
	return inv, nil
}

// Once ordinary references are gone, only the registered marker reservations
// can account for remaining bytes. Residual or missing accounting requires
// reconciliation; removal must never make it disappear by clearing counters.
func retirementFinalLedgerBalanced(ctx context.Context, c *ent.Client, spaces []*ent.StorageSpace, writes []*ent.StorageWrite, ledger map[int][]*ent.StorageRetirementEntry) (bool, error) {
	expected := map[int][4]int64{}
	ids := []int{}
	byID := map[int]*ent.StorageWrite{}
	for _, w := range writes {
		if w.Edges.Task != nil && w.Edges.Task.Kind == "storage_marker" && w.Phase != "cleaned" {
			if markerRecoveredBy(w, ledger) {
				return false, nil
			}
			ids = append(ids, w.ID)
			byID[w.ID] = w
		}
	}
	if len(ids) > 0 {
		rows, err := c.StorageReservation.Query().Where(storagereservation.WriteIDIn(ids...)).All(ctx)
		if err != nil {
			return false, err
		}
		if len(rows) != len(ids) {
			return false, nil
		}
		for _, res := range rows {
			w := byID[res.WriteID]
			v := expected[w.SpaceID]
			index := -1
			switch res.State {
			case storagereservation.StateReserved:
				index = 0
			case storagereservation.StateCandidate:
				index = 1
			case storagereservation.StateLive:
				index = 2
			case storagereservation.StatePendingDelete:
				index = 3
			case storagereservation.StateFreed:
				continue
			default:
				return false, nil
			}
			if res.Bytes > MaxStorageInteger-v[index] {
				return false, nil
			}
			v[index] += res.Bytes
			expected[w.SpaceID] = v
		}
	}
	for _, sp := range spaces {
		if expected[sp.ID] != [4]int64{sp.ReservedBytes, sp.CandidateBytes, sp.LiveBytes, sp.PendingDeleteBytes} {
			return false, nil
		}
	}
	return true, nil
}

// retirementMarkerLedger 返回每个空间已完成回收的标记账本证据。调用方必须继续
// 核对对象键与已登记版本:仅凭"存在完成条目"无法区分旧一代回收与重新注册的标记。
func (s *StorageRetirementService) retirementMarkerLedger(ctx context.Context, client *ent.Client, spaces []int) (map[int][]*ent.StorageRetirementEntry, error) {
	recovered := map[int][]*ent.StorageRetirementEntry{}
	if len(spaces) == 0 {
		return recovered, nil
	}
	rows, err := client.StorageRetirementEntry.Query().Where(storageretiremententry.KindEQ("marker"), storageretiremententry.SpaceIDIn(spaces...), storageretiremententry.StatusEQ(storageretiremententry.StatusCompleted)).Select(storageretiremententry.FieldSpaceID, storageretiremententry.FieldEntryKey, storageretiremententry.FieldObjectKey, storageretiremententry.FieldProviderVersion).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.SpaceID != nil {
			recovered[*row.SpaceID] = append(recovered[*row.SpaceID], row)
		}
	}
	return recovered, nil
}

// markerRecoveredBy 核对完成条目与已登记标记写入的对象键与提供方版本。
func markerRecoveredBy(write *ent.StorageWrite, ledger map[int][]*ent.StorageRetirementEntry) bool {
	for _, entry := range ledger[write.SpaceID] {
		if entry.ObjectKey != write.ObjectKey {
			continue
		}
		if entry.ProviderVersion == write.ProviderVersion {
			return true
		}
	}
	return false
}

// retirementFingerprintDependency 是快照指纹中的依赖事实;不含纯调度提示(next_check_at)。
type retirementFingerprintDependency struct {
	Kind            string   `json:"kind"`
	Status          string   `json:"status"`
	Count           int      `json:"count"`
	ProjectID       int      `json:"project_id"`
	SpaceID         int      `json:"space_id"`
	MigrationTaskID int      `json:"migration_task_id"`
	ProtectedBytes  int64    `json:"protected_bytes"`
	ReasonCodes     []string `json:"reason_codes"`
}

// retirementInventoryFingerprint 覆盖规范化目标、范围/代次、绑定与对象、默认政策与已加载配置。
func (s *StorageRetirementService) retirementInventoryFingerprint(ctx context.Context, client *ent.Client, target RetirementTarget, conn *ent.StorageConnection, inv *retirementInventory) (string, error) {
	policyGeneration := int64(0)
	if policy, err := storagePolicy(ctx, client); err == nil {
		policyGeneration = policy.Generation
	} else if !errors.Is(err, ErrStoragePolicy) {
		return "", err
	}
	backends := make([]string, 0, len(s.cfg.Backends))
	for _, backend := range s.cfg.Backends {
		backends = append(backends, backend.ID+":"+backend.Driver)
	}
	sort.Strings(backends)
	projects := make([]string, 0, len(inv.Projects))
	for _, row := range inv.Projects {
		spaceID := 0
		if row.StorageSpaceID != nil {
			spaceID = *row.StorageSpaceID
		}
		migrationTaskID := 0
		if row.StorageMigrationTaskID != nil {
			migrationTaskID = *row.StorageMigrationTaskID
		}
		projects = append(projects, fmt.Sprintf("%d|%s|%d|%d|%d", row.ID, row.StorageState, row.StorageGeneration, spaceID, migrationTaskID))
	}
	dependencies := make([]retirementFingerprintDependency, 0, len(inv.Dependencies))
	for _, dep := range inv.Dependencies {
		bytes := int64(0)
		if dep.ProtectedBytes != nil {
			bytes = *dep.ProtectedBytes
		}
		dependencies = append(dependencies, retirementFingerprintDependency{Kind: dep.Kind, Status: dep.Status, Count: dep.Count, ProjectID: dep.ProjectID, SpaceID: dep.SpaceID, MigrationTaskID: dep.MigrationTaskID, ProtectedBytes: bytes, ReasonCodes: dep.ReasonCodes})
	}
	facts := struct {
		Target             RetirementTarget                  `json:"target"`
		PolicyGeneration   int64                             `json:"policy_generation"`
		DefaultSpaceID     int                               `json:"default_space_id"`
		DefaultSiteBackend string                            `json:"default_site_backend"`
		Maintenance        bool                              `json:"maintenance"`
		Enabled            bool                              `json:"enabled"`
		Backends           []string                          `json:"backends"`
		Connection         string                            `json:"connection"`
		Projects           []string                          `json:"projects"`
		Dependencies       []retirementFingerprintDependency `json:"dependencies"`
		HasUsage           bool                              `json:"has_usage"`
		Identities         []string                          `json:"identities"`
	}{
		Target: target, PolicyGeneration: policyGeneration, DefaultSpaceID: s.storage.defaultSpaceID,
		DefaultSiteBackend: s.cfg.DefaultSiteSpace, Maintenance: s.cfg.Maintenance, Enabled: s.cfg.Enabled,
		Backends: backends, Projects: projects, Dependencies: dependencies, HasUsage: inv.HasUsage, Identities: inv.identities,
	}
	if conn != nil {
		facts.Connection = fmt.Sprintf("%d|%s|%s|%s|%d|%d|%d", conn.ID, conn.Lifecycle, conn.Status, conn.RemoteUseState, conn.ManagementGeneration, conn.ExecutionGeneration, conn.ActiveAuthGeneration)
	}
	return retirementHash(facts), nil
}

// retirementFillLocations 一次补齐缺失的位置行,避免依赖扫描出现逐行查询。
func retirementFillLocations(ctx context.Context, client *ent.Client, known map[int]*ent.BlobLocation, entries []*ent.DeletionEntry, pins []*ent.BackupPin) error {
	missing := []int{}
	for _, entry := range entries {
		if _, ok := known[entry.LocationID]; !ok {
			missing = append(missing, entry.LocationID)
		}
	}
	for _, pin := range pins {
		if _, ok := known[pin.LocationID]; !ok {
			missing = append(missing, pin.LocationID)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	rows, err := client.BlobLocation.Query().Where(bloblocation.IDIn(missing...)).All(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		known[row.ID] = row
	}
	return nil
}
