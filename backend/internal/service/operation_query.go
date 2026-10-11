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
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/project"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/synctask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

const (
	OperationTranslation  = "translation"
	OperationGlossarySync = "glossary_sync"
	OperationStorage      = "storage"
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
	TaskType    string
	Job         *ent.Job
	SyncTask    *ent.SyncTask
	StorageTask *ent.StorageTask
	ProjectName string
}

func (r OperationRow) ID() int {
	if r.StorageTask != nil {
		return r.StorageTask.ID
	}
	if r.Job != nil {
		return r.Job.ID
	}
	return r.SyncTask.ID
}
func (r OperationRow) ProjectID() int {
	if r.StorageTask != nil {
		return r.StorageTask.ProjectID
	}
	if r.Job != nil {
		return r.Job.ProjectID
	}
	return r.SyncTask.ProjectID
}
func (r OperationRow) UpdatedAt() time.Time {
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
		Translation  OperationCounts `json:"translation"`
		GlossarySync OperationCounts `json:"glossary_sync"`
		Storage      OperationCounts `json:"storage"`
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
	if kind != "" && kind != OperationTranslation && kind != OperationGlossarySync && kind != OperationStorage {
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
	if dec.Decode(new(any)) != io.EOF || c.Version != 1 || c.Filter != filter || c.TaskID <= 0 || c.UpdatedAt.IsZero() || (c.TaskType != OperationTranslation && c.TaskType != OperationGlossarySync && c.TaskType != OperationStorage) {
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
		if !seen[row.ProjectID()] {
			ids = append(ids, row.ProjectID())
			seen[row.ProjectID()] = true
		}
	}
	projects, err := s.client.Project.Query().Where(project.IDIn(ids...)).Select(project.FieldID, project.FieldName).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("load operation project names: %w", err)
	}
	names := map[int]string{}
	for _, p := range projects {
		names[p.ID] = p.Name
	}
	for i := range page.Items {
		page.Items[i].ProjectName = names[page.Items[i].ProjectID()]
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
				if kind == OperationStorage {
					c.WaitingRetry = r.Count
				}
			case "needs_action":
				if kind == OperationStorage {
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
	a, b, c := result.ByType.Translation, result.ByType.GlossarySync, result.ByType.Storage
	result.Total = OperationCounts{
		Pending:      a.Pending + b.Pending + c.Pending,
		Running:      a.Running + b.Running + c.Running,
		Pausing:      a.Pausing + b.Pausing + c.Pausing,
		Paused:       a.Paused + b.Paused + c.Paused,
		RecentFailed: a.RecentFailed + b.RecentFailed + c.RecentFailed,
		WaitingRetry: a.WaitingRetry + b.WaitingRetry + c.WaitingRetry,
		NeedsAction:  a.NeedsAction + b.NeedsAction + c.NeedsAction,
	}
	return result, nil
}
