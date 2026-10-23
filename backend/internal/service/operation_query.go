package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/orgmembership"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/predicate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/project"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageretirement"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagespace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/synctask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

const (
	OperationTranslation       = "translation"
	OperationGlossarySync      = "glossary_sync"
	OperationStorage           = "storage"
	OperationStorageRetirement = "storage_retirement"
)

// OperationQueryService 投影独立的业务实体，但不持有它们的生命周期。
type OperationQueryService struct{ client *ent.Client }

func NewOperationQueryService(client *ent.Client) *OperationQueryService {
	return &OperationQueryService{client: client}
}

type OperationListOptions struct {
	AccessibleJobListOptions
	TaskType string
}
type OperationSummaryOptions struct {
	TaskType    string
	ProjectID   int
	TriggerType string
}
type OperationRow struct {
	TaskType       string
	Job            *ent.Job
	SyncTask       *ent.SyncTask
	StorageTask    *ent.StorageTask
	Retirement     *ent.StorageRetirement
	ProjectName    string
	RetirementName string
}

func (r OperationRow) ID() int {
	if r.Retirement != nil {
		return r.Retirement.ID
	}
	if r.StorageTask != nil {
		return r.StorageTask.ID
	}
	if r.Job != nil {
		return r.Job.ID
	}
	return r.SyncTask.ID
}
func (r OperationRow) ProjectID() int {
	if r.Retirement != nil {
		return 0
	}
	if r.StorageTask != nil {
		return r.StorageTask.ProjectID
	}
	if r.Job != nil {
		return r.Job.ProjectID
	}
	return r.SyncTask.ProjectID
}
func (r OperationRow) UpdatedAt() time.Time {
	if r.Retirement != nil {
		return r.Retirement.UpdatedAt
	}
	if r.StorageTask != nil {
		return r.StorageTask.UpdatedAt
	}
	if r.Job != nil {
		return r.Job.UpdatedAt
	}
	return r.SyncTask.UpdatedAt
}

type OperationPage struct {
	Items      []OperationRow
	NextCursor string
}
type OperationCounts struct {
	Pending      int `json:"pending"`
	Running      int `json:"running"`
	Pausing      int `json:"pausing"`
	Paused       int `json:"paused"`
	RecentFailed int `json:"recent_failed"`
	WaitingRetry int `json:"waiting_retry"`
	NeedsAction  int `json:"needs_action"`
}
type OperationsCountsSummary struct {
	Total  OperationCounts `json:"total"`
	ByType struct {
		Translation       OperationCounts `json:"translation"`
		GlossarySync      OperationCounts `json:"glossary_sync"`
		Storage           OperationCounts `json:"storage"`
		StorageRetirement OperationCounts `json:"storage_retirement"`
	} `json:"by_type"`
	RecentFailedSince time.Time `json:"recent_failed_since"`
	AsOf              time.Time `json:"as_of"`
}
type operationCursor struct {
	Version   int       `json:"v"`
	Filter    string    `json:"filter"`
	UpdatedAt time.Time `json:"updated_at"`
	TaskType  string    `json:"task_type"`
	TaskID    int       `json:"task_id"`
}

func validateOperationScope(kind string, projectID int, trigger string) error {
	if kind != "" && kind != OperationTranslation && kind != OperationGlossarySync && kind != OperationStorage && kind != OperationStorageRetirement {
		return fmt.Errorf("%w: invalid task_type", ErrInvalidInput)
	}
	if trigger != "" && kind != OperationTranslation {
		return fmt.Errorf("%w: trigger_type requires task_type=translation", ErrInvalidInput)
	}
	return validateJobQueryScope(projectID, trigger)
}
func operationFilterKey(opts OperationListOptions) string {
	opts.Cursor = ""
	opts.Limit = 0
	if opts.State == "" && opts.Status == "" {
		opts.State = "active"
	}
	if opts.UpdatedFrom != nil {
		t := opts.UpdatedFrom.UTC()
		opts.UpdatedFrom = &t
	}
	if opts.UpdatedBefore != nil {
		t := opts.UpdatedBefore.UTC()
		opts.UpdatedBefore = &t
	}
	raw, _ := json.Marshal(opts) // 所有字段均为 JSON 基本类型或已校验的时间值。
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}
func decodeOperationCursor(raw, filter string) (*operationCursor, error) {
	invalid := fmt.Errorf("%w: invalid operation cursor", ErrInvalidInput)
	if len(raw) > 2048 {
		return nil, invalid
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil || base64.RawURLEncoding.EncodeToString(data) != raw {
		return nil, invalid
	}
	var c operationCursor
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, invalid
	}
	if dec.Decode(new(any)) != io.EOF || c.Version != 1 || c.Filter != filter || c.TaskID <= 0 || c.UpdatedAt.IsZero() || (c.TaskType != OperationTranslation && c.TaskType != OperationGlossarySync && c.TaskType != OperationStorage && c.TaskType != OperationStorageRetirement) {
		return nil, invalid
	}
	c.UpdatedAt = c.UpdatedAt.UTC()
	return &c, nil
}

// 裸时间戳列保留索引。PostgreSQL 的小数边界向上取整，
// 使半开区间在数据库的微秒精度下仍保持其含义。
func operationTimePredicate(op sql.Op, instant time.Time) func(*sql.Selector) {
	return func(s *sql.Selector) {
		bound := instant.UTC()
		if s.Dialect() == dialect.Postgres && (op == sql.OpGTE || op == sql.OpLT) {
			bound = timeutil.CeilMicrosecond(bound)
		}
		s.Where(sql.P(func(b *sql.Builder) { b.Ident(s.C("updated_at")).WriteOp(op).Arg(bound) }))
	}
}
func activeOperationStatuses(kind string) []string {
	switch kind {
	case OperationTranslation:
		return activeJobStatuses()
	case OperationGlossarySync:
		return []string{SyncTaskStatusPending, SyncTaskStatusRunning}
	case OperationStorage:
		return []string{string(storagetask.StatusPending), string(storagetask.StatusRunning), string(storagetask.StatusWaitingRetry), string(storagetask.StatusNeedsAction)}
	case OperationStorageRetirement:
		return []string{string(storageretirement.StatusPending), string(storageretirement.StatusRunning), string(storageretirement.StatusWaitingRetry), string(storageretirement.StatusNeedsAction)}
	default:
		return nil
	}
}

func operationQueryFilter(opts OperationListOptions, kind string, c *operationCursor, precisionErr *error) func(*sql.Selector) {
	return func(s *sql.Selector) {
		if kind != OperationTranslation && (opts.Status == JobStatusPausing || opts.Status == JobStatusPaused) {
			s.Where(sql.EQ(s.C("id"), 0))
			return
		}
		if opts.Status != "" {
			s.Where(sql.EQ(s.C("status"), opts.Status))
		} else {
			switch opts.State {
			case "", "active":
				sql.FieldIn("status", activeOperationStatuses(kind)...)(s)
			case "terminal":
				s.Where(sql.In(s.C("status"), "completed", "failed", "cancelled"))
			}
		}
		if opts.UpdatedFrom != nil {
			operationTimePredicate(sql.OpGTE, *opts.UpdatedFrom)(s)
		}
		if opts.UpdatedBefore != nil {
			operationTimePredicate(sql.OpLT, *opts.UpdatedBefore)(s)
		}
		if c == nil {
			return
		}
		if s.Dialect() == dialect.Postgres && c.UpdatedAt.Nanosecond()%1000 != 0 {
			*precisionErr = fmt.Errorf("%w: cursor exceeds database timestamp precision", ErrInvalidInput)
			s.AddError(*precisionErr)
			return
		}
		before := sql.LT(s.C("updated_at"), c.UpdatedAt)
		switch {
		case kind < c.TaskType:
			s.Where(sql.Or(before, sql.EQ(s.C("updated_at"), c.UpdatedAt)))
		case kind == c.TaskType:
			s.Where(sql.Or(before, sql.And(sql.EQ(s.C("updated_at"), c.UpdatedAt), sql.LT(s.C("id"), c.TaskID))))
		default:
			s.Where(before)
		}
	}
}
func (s *OperationQueryService) jobs(actor, projectID int, trigger string) *ent.JobQuery {
	q := s.client.Job.Query().Where(job.HasProjectWith(readableProjectPredicate(actor)))
	if projectID != 0 {
		q.Where(job.ProjectIDEQ(projectID))
	}
	if trigger != "" {
		q.Where(job.TriggerTypeEQ(trigger))
	}
	return q
}
func (s *OperationQueryService) syncTasks(actor, projectID int) *ent.SyncTaskQuery {
	q := s.client.SyncTask.Query().Where(synctask.HasProjectWith(readableProjectPredicate(actor)))
	if projectID != 0 {
		q.Where(synctask.ProjectIDEQ(projectID))
	}
	return q
}

func (s *OperationQueryService) storageTasks(actor, projectID int) *ent.StorageTaskQuery {
	q := s.client.StorageTask.Query().Where(func(selector *sql.Selector) {
		projects := sql.Dialect(selector.Dialect()).Select(project.FieldID).From(sql.Table(project.Table))
		readableProjectPredicate(actor)(projects)
		selector.Where(sql.In(selector.C(storagetask.FieldProjectID), projects))
	})
	if projectID != 0 {
		q.Where(storagetask.ProjectIDEQ(projectID))
	}
	return q
}

// storageRetirementAccessPredicate 镜像连接/空间的 requireOwner,而不借用项目权限。
// 个人退出归本人,组织退出归组织 owner/admin,站点退出归平台管理员;
// 停用账号与任何归属都不匹配,因此投影在分页之前就排除了无权行。
func storageRetirementAccessPredicate(actor int) predicate.StorageRetirement {
	return func(selector *sql.Selector) {
		dialect := selector.Dialect()
		active := sql.Dialect(dialect).Select(user.FieldID).From(sql.Table(user.Table))
		active.Where(sql.And(sql.EQ(active.C(user.FieldID), actor), sql.EQ(active.C(user.FieldActive), true)))
		admin := sql.Dialect(dialect).Select(user.FieldID).From(sql.Table(user.Table))
		admin.Where(sql.And(sql.EQ(admin.C(user.FieldID), actor), sql.EQ(admin.C(user.FieldActive), true), sql.EQ(admin.C(user.FieldRole), SystemRoleAdmin)))
		orgs := sql.Dialect(dialect).Select(orgmembership.OrganizationColumn).From(sql.Table(orgmembership.Table))
		orgs.Where(sql.And(sql.EQ(orgs.C(orgmembership.UserColumn), actor), sql.In(orgs.C(orgmembership.FieldRole), OrgRoleOwner, OrgRoleAdmin)))
		ownerKind, ownerID := selector.C(storageretirement.FieldOwnerKind), selector.C(storageretirement.FieldOwnerID)
		selector.Where(sql.And(
			sql.Exists(active),
			sql.Or(
				sql.And(sql.EQ(ownerKind, string(storageretirement.OwnerKindUser)), sql.EQ(ownerID, actor)),
				sql.And(sql.EQ(ownerKind, string(storageretirement.OwnerKindOrg)), sql.In(ownerID, orgs)),
				sql.And(sql.EQ(ownerKind, string(storageretirement.OwnerKindSite)), sql.EQ(ownerID, 0), sql.Exists(admin)),
			),
		))
	}
}

func (s *OperationQueryService) retirements(actor int) *ent.StorageRetirementQuery {
	return s.client.StorageRetirement.Query().Where(storageRetirementAccessPredicate(actor))
}

// retirementTargetName 解析连接/空间的当前显示名;tombstone 仍可查询,
// 已不存在时返回空串而不是伪造名称或暴露身份标识。
func (s *OperationQueryService) retirementTargetNames(ctx context.Context, rows []OperationRow) (map[string]string, error) {
	connections, spaces := []int{}, []int{}
	seen := map[string]bool{}
	for _, row := range rows {
		if row.Retirement == nil {
			continue
		}
		key := fmt.Sprintf("%s:%d", row.Retirement.TargetKind, row.Retirement.TargetID)
		if seen[key] {
			continue
		}
		seen[key] = true
		switch row.Retirement.TargetKind {
		case storageretirement.TargetKindConnection:
			connections = append(connections, row.Retirement.TargetID)
		case storageretirement.TargetKindSpace:
			spaces = append(spaces, row.Retirement.TargetID)
		}
	}
	names := map[string]string{}
	if len(connections) > 0 {
		found, err := s.client.StorageConnection.Query().Where(storageconnection.IDIn(connections...)).Select(storageconnection.FieldID, storageconnection.FieldName).All(ctx)
		if err != nil {
			return nil, fmt.Errorf("load retirement connection names: %w", err)
		}
		for _, row := range found {
			names[fmt.Sprintf("connection:%d", row.ID)] = row.Name
		}
	}
	if len(spaces) > 0 {
		found, err := s.client.StorageSpace.Query().Where(storagespace.IDIn(spaces...)).Select(storagespace.FieldID, storagespace.FieldName).All(ctx)
		if err != nil {
			return nil, fmt.Errorf("load retirement space names: %w", err)
		}
		for _, row := range found {
			names[fmt.Sprintf("space:%d", row.ID)] = row.Name
		}
	}
	return names, nil
}

func (s *OperationQueryService) List(ctx context.Context, actor int, opts OperationListOptions) (*OperationPage, error) {
	if err := validateOperationScope(opts.TaskType, opts.ProjectID, opts.TriggerType); err != nil {
		return nil, err
	}
	validation := opts.AccessibleJobListOptions
	if validation.Status == "waiting_retry" || validation.Status == "needs_action" {
		validation.Status = "pending"
	}
	if err := validateAccessibleJobOptions(validation); err != nil {
		return nil, err
	}
	if opts.Limit == 0 {
		opts.Limit = 50
	}
	filter := operationFilterKey(opts)
	var cursor *operationCursor
	if opts.Cursor != "" {
		var err error
		cursor, err = decodeOperationCursor(opts.Cursor, filter)
		if err != nil {
			return nil, err
		}
	}
	rows := make([]OperationRow, 0, 2*(opts.Limit+1))
	var precisionErr error
	if opts.TaskType == "" || opts.TaskType == OperationTranslation {
		found, err := s.jobs(actor, opts.ProjectID, opts.TriggerType).
			Where(operationQueryFilter(opts, OperationTranslation, cursor, &precisionErr)).
			Select(job.FieldID, job.FieldProjectID, job.FieldStatus, job.FieldTriggerType, job.FieldResourceCount, job.FieldCompletedResources, job.FieldFailedResources, job.FieldProgressTotal, job.FieldProgressCompleted, job.FieldCreatedAt, job.FieldUpdatedAt, job.FieldStartedAt, job.FieldFinishedAt).
			Order(ent.Desc(job.FieldUpdatedAt), ent.Desc(job.FieldID)).Limit(opts.Limit + 1).All(ctx)
		if precisionErr != nil {
			return nil, precisionErr
		}
		if err != nil {
			return nil, fmt.Errorf("query operation jobs: %w", err)
		}
		for _, row := range found {
			rows = append(rows, OperationRow{TaskType: OperationTranslation, Job: row})
		}
	}
	if opts.TaskType == "" || opts.TaskType == OperationGlossarySync {
		found, err := s.syncTasks(actor, opts.ProjectID).
			Where(operationQueryFilter(opts, OperationGlossarySync, cursor, &precisionErr)).
			Select(synctask.FieldID, synctask.FieldProjectID, synctask.FieldStatus, synctask.FieldProcessedSegments, synctask.FieldTotalSegments, synctask.FieldCreatedAt, synctask.FieldUpdatedAt, synctask.FieldStartedAt, synctask.FieldFinishedAt).
			Order(ent.Desc(synctask.FieldUpdatedAt), ent.Desc(synctask.FieldID)).Limit(opts.Limit + 1).All(ctx)
		if precisionErr != nil {
			return nil, precisionErr
		}
		if err != nil {
			return nil, fmt.Errorf("query operation sync tasks: %w", err)
		}
		for _, row := range found {
			rows = append(rows, OperationRow{TaskType: OperationGlossarySync, SyncTask: row})
		}
	}
	if opts.TaskType == "" || opts.TaskType == OperationStorage {
		found, err := s.storageTasks(actor, opts.ProjectID).Where(operationQueryFilter(opts, OperationStorage, cursor, &precisionErr)).
			Select(storagetask.FieldID, storagetask.FieldProjectID, storagetask.FieldKind, storagetask.FieldStatus, storagetask.FieldPhase, storagetask.FieldCleanupStatus, storagetask.FieldErrorCode, storagetask.FieldNextRetryAt, storagetask.FieldCreatedAt, storagetask.FieldUpdatedAt).
			Order(ent.Desc(storagetask.FieldUpdatedAt), ent.Desc(storagetask.FieldID)).Limit(opts.Limit + 1).All(ctx)
		if precisionErr != nil {
			return nil, precisionErr
		}
		if err != nil {
			return nil, fmt.Errorf("query storage tasks: %w", err)
		}
		for _, task := range found {
			rows = append(rows, OperationRow{TaskType: OperationStorage, StorageTask: task})
		}
	}
	// 父退出任务没有 project_id,显式限定项目时排除,避免用项目权限推断连接管理权限。
	if (opts.TaskType == "" || opts.TaskType == OperationStorageRetirement) && opts.ProjectID == 0 {
		found, err := s.retirements(actor).Where(operationQueryFilter(opts, OperationStorageRetirement, cursor, &precisionErr)).
			Select(storageretirement.FieldID, storageretirement.FieldTargetKind, storageretirement.FieldTargetID, storageretirement.FieldTargetIdentity, storageretirement.FieldOwnerKind, storageretirement.FieldOwnerID, storageretirement.FieldStatus, storageretirement.FieldPhase, storageretirement.FieldErrorCode, storageretirement.FieldNextCheckAt, storageretirement.FieldCreatedAt, storageretirement.FieldUpdatedAt, storageretirement.FieldStartedAt, storageretirement.FieldFinishedAt).
			Order(ent.Desc(storageretirement.FieldUpdatedAt), ent.Desc(storageretirement.FieldID)).Limit(opts.Limit + 1).All(ctx)
		if precisionErr != nil {
			return nil, precisionErr
		}
		if err != nil {
			return nil, fmt.Errorf("query storage retirements: %w", err)
		}
		for _, retirement := range found {
			rows = append(rows, OperationRow{TaskType: OperationStorageRetirement, Retirement: retirement})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].UpdatedAt().Equal(rows[j].UpdatedAt()) {
			return rows[i].UpdatedAt().After(rows[j].UpdatedAt())
		}
		if rows[i].TaskType != rows[j].TaskType {
			return rows[i].TaskType > rows[j].TaskType
		}
		return rows[i].ID() > rows[j].ID()
	})
	page := &OperationPage{Items: rows}
	if len(rows) > opts.Limit {
		page.Items = rows[:opts.Limit]
		last := page.Items[len(page.Items)-1]
		raw, err := json.Marshal(operationCursor{1, filter, last.UpdatedAt().UTC(), last.TaskType, last.ID()})
		if err != nil {
			return nil, err
		}
		page.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	if len(page.Items) == 0 {
		return page, nil
	}
	ids := make([]int, 0, len(page.Items))
	seen := map[int]bool{}
	for _, row := range page.Items {
		if projectID := row.ProjectID(); projectID != 0 && !seen[projectID] {
			ids = append(ids, projectID)
			seen[projectID] = true
		}
	}
	names := map[int]string{}
	if len(ids) > 0 {
		projects, err := s.client.Project.Query().Where(project.IDIn(ids...)).Select(project.FieldID, project.FieldName).All(ctx)
		if err != nil {
			return nil, fmt.Errorf("load operation project names: %w", err)
		}
		for _, p := range projects {
			names[p.ID] = p.Name
		}
	}
	for i := range page.Items {
		page.Items[i].ProjectName = names[page.Items[i].ProjectID()]
	}
	retirementNames, err := s.retirementTargetNames(ctx, page.Items)
	if err != nil {
		return nil, err
	}
	for i := range page.Items {
		if page.Items[i].Retirement == nil {
			continue
		}
		r := page.Items[i].Retirement
		page.Items[i].RetirementName = retirementNames[fmt.Sprintf("%s:%d", r.TargetKind, r.TargetID)]
	}
	return page, nil
}

func (s *OperationQueryService) Summary(ctx context.Context, actor int, opts OperationSummaryOptions) (*OperationsCountsSummary, error) {
	return s.summaryAt(ctx, actor, opts, timeutil.NowUTC())
}
func (s *OperationQueryService) summaryAt(ctx context.Context, actor int, opts OperationSummaryOptions, now time.Time) (*OperationsCountsSummary, error) {
	if err := validateOperationScope(opts.TaskType, opts.ProjectID, opts.TriggerType); err != nil {
		return nil, err
	}
	result := &OperationsCountsSummary{AsOf: now.UTC(), RecentFailedSince: now.UTC().Add(-7 * 24 * time.Hour)}
	filter := func(kind string) func(*sql.Selector) {
		return sql.OrPredicates(
			sql.FieldIn("status", activeOperationStatuses(kind)...),
			sql.AndPredicates(
				sql.FieldEQ("status", "failed"),
				operationTimePredicate(sql.OpGTE, result.RecentFailedSince),
				operationTimePredicate(sql.OpLT, result.AsOf),
			),
		)
	}
	type bucket struct {
		Status string `json:"status"`
		Count  int    `json:"count"`
	}
	convert := func(rows []bucket, kind string) OperationCounts {
		var c OperationCounts
		for _, r := range rows {
			switch r.Status {
			case "pending":
				c.Pending = r.Count
			case "running":
				c.Running = r.Count
			case "pausing":
				if kind == OperationTranslation {
					c.Pausing = r.Count
				}
			case "paused":
				if kind == OperationTranslation {
					c.Paused = r.Count
				}
			case "failed":
				c.RecentFailed = r.Count
			case "waiting_retry":
				if kind == OperationStorage || kind == OperationStorageRetirement {
					c.WaitingRetry = r.Count
				}
			case "needs_action":
				if kind == OperationStorage || kind == OperationStorageRetirement {
					c.NeedsAction = r.Count
				}
			}
		}
		return c
	}
	if opts.TaskType == "" || opts.TaskType == OperationTranslation {
		var buckets []bucket
		if err := s.jobs(actor, opts.ProjectID, opts.TriggerType).Where(filter(OperationTranslation)).GroupBy(job.FieldStatus).Aggregate(ent.Count()).Scan(ctx, &buckets); err != nil {
			return nil, err
		}
		result.ByType.Translation = convert(buckets, OperationTranslation)
	}
	if opts.TaskType == "" || opts.TaskType == OperationGlossarySync {
		var buckets []bucket
		if err := s.syncTasks(actor, opts.ProjectID).Where(filter(OperationGlossarySync)).GroupBy(synctask.FieldStatus).Aggregate(ent.Count()).Scan(ctx, &buckets); err != nil {
			return nil, err
		}
		result.ByType.GlossarySync = convert(buckets, OperationGlossarySync)
	}
	if opts.TaskType == "" || opts.TaskType == OperationStorage {
		var buckets []bucket
		if err := s.storageTasks(actor, opts.ProjectID).Where(filter(OperationStorage)).GroupBy(storagetask.FieldStatus).Aggregate(ent.Count()).Scan(ctx, &buckets); err != nil {
			return nil, err
		}
		result.ByType.Storage = convert(buckets, OperationStorage)
	}
	// 父退出任务没有 project_id,显式限定项目时不计入,与列表投影保持一致。
	if (opts.TaskType == "" || opts.TaskType == OperationStorageRetirement) && opts.ProjectID == 0 {
		var buckets []bucket
		if err := s.retirements(actor).Where(filter(OperationStorageRetirement)).GroupBy(storageretirement.FieldStatus).Aggregate(ent.Count()).Scan(ctx, &buckets); err != nil {
			return nil, err
		}
		result.ByType.StorageRetirement = convert(buckets, OperationStorageRetirement)
	}
	a, b, c, d := result.ByType.Translation, result.ByType.GlossarySync, result.ByType.Storage, result.ByType.StorageRetirement
	result.Total = OperationCounts{
		Pending:      a.Pending + b.Pending + c.Pending + d.Pending,
		Running:      a.Running + b.Running + c.Running + d.Running,
		Pausing:      a.Pausing + b.Pausing + c.Pausing + d.Pausing,
		Paused:       a.Paused + b.Paused + c.Paused + d.Paused,
		RecentFailed: a.RecentFailed + b.RecentFailed + c.RecentFailed + d.RecentFailed,
		WaitingRetry: a.WaitingRetry + b.WaitingRetry + c.WaitingRetry + d.WaitingRetry,
		NeedsAction:  a.NeedsAction + b.NeedsAction + c.NeedsAction + d.NeedsAction,
	}
	return result, nil
}
