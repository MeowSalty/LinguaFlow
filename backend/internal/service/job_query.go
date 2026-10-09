package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/project"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

// AccessibleJobListOptions filters jobs across all projects readable by an actor.
// A zero Limit defaults to 50; State and Status are mutually exclusive.
type AccessibleJobListOptions struct {
	State         string
	Status        string
	TriggerType   string
	Cursor        string
	ProjectID     int
	Limit         int
	UpdatedFrom   *time.Time
	UpdatedBefore *time.Time
}

// AccessibleJobPage contains lightweight rows with only their project edge loaded.
type AccessibleJobPage struct {
	Items      []*ent.Job
	NextCursor string
}

type JobSummaryOptions struct {
	ProjectID   int
	TriggerType string
}

type JobCountsSummary struct {
	Pending           int
	Running           int
	Pausing           int
	Paused            int
	RecentFailed      int
	RecentFailedSince time.Time
	AsOf              time.Time
}

// The cursor stores the timestamp read from the database before response
// formatting, preserving all fractional digits in canonical UTC.
type accessibleJobCursor struct {
	Version   int       `json:"v"`
	UpdatedAt time.Time `json:"updated_at"`
	ID        int       `json:"id"`
}

func (s *JobService) ListAccessibleJobs(ctx context.Context, actorUserID int, opts AccessibleJobListOptions) (*AccessibleJobPage, error) {
	if err := validateAccessibleJobOptions(opts); err != nil {
		return nil, err
	}
	if opts.Limit == 0 {
		opts.Limit = 50
	}
	q := s.accessibleJobsQuery(actorUserID, opts.ProjectID, opts.TriggerType)
	if opts.Status != "" {
		q.Where(job.StatusEQ(opts.Status))
	} else {
		switch opts.State {
		case "", "active":
			q.Where(job.StatusIn(JobStatusPending, JobStatusRunning, JobStatusPausing, JobStatusPaused))
		case "terminal":
			q.Where(job.StatusIn(JobStatusCompleted, JobStatusFailed, JobStatusCancelled))
		}
	}
	if opts.UpdatedFrom != nil {
		q.Where(jobUpdatedAtCompare(sql.OpGTE, *opts.UpdatedFrom))
	}
	if opts.UpdatedBefore != nil {
		q.Where(jobUpdatedAtCompare(sql.OpLT, *opts.UpdatedBefore))
	}
	var cursorPrecisionErr error
	if opts.Cursor != "" {
		cursor, err := decodeAccessibleJobCursor(opts.Cursor)
		if err != nil {
			return nil, err
		}
		q.Where(func(selector *sql.Selector) {
			if selector.Dialect() == dialect.Postgres && cursor.UpdatedAt.Nanosecond()%1000 != 0 {
				cursorPrecisionErr = fmt.Errorf("%w: cursor exceeds database timestamp precision", ErrInvalidInput)
				selector.AddError(cursorPrecisionErr)
			}
		})
		q.Where(job.Or(
			jobUpdatedAtCompare(sql.OpLT, cursor.UpdatedAt),
			job.And(jobUpdatedAtCompare(sql.OpEQ, cursor.UpdatedAt), job.IDLT(cursor.ID)),
		))
	}
	q.Select(
		job.FieldID, job.FieldProjectID, job.FieldStatus, job.FieldTriggerType,
		job.FieldResourceCount, job.FieldCompletedResources, job.FieldFailedResources,
		job.FieldProgressTotal, job.FieldProgressCompleted,
		job.FieldCreatedAt, job.FieldUpdatedAt, job.FieldStartedAt, job.FieldFinishedAt,
	)
	rows, err := q.WithProject(func(pq *ent.ProjectQuery) {
		pq.Select(project.FieldID, project.FieldName)
	}).Order(jobUpdatedAtDescending, ent.Desc(job.FieldID)).Limit(opts.Limit + 1).All(ctx)
	// ent's SQL builder concatenates errors as text, so retain our typed input
	// error separately for the API's uniform 400 response.
	if cursorPrecisionErr != nil {
		return nil, cursorPrecisionErr
	}
	if err != nil {
		return nil, fmt.Errorf("list accessible jobs: %w", err)
	}
	page := &AccessibleJobPage{Items: rows}
	if page.Items == nil {
		page.Items = []*ent.Job{}
	}
	if len(rows) > opts.Limit {
		page.Items = rows[:opts.Limit]
		page.NextCursor, err = encodeAccessibleJobCursor(page.Items[len(page.Items)-1])
		if err != nil {
			return nil, err
		}
	}
	return page, nil
}

func (s *JobService) GetJobsSummary(ctx context.Context, actorUserID int, opts JobSummaryOptions) (*JobCountsSummary, error) {
	return s.getJobsSummary(ctx, actorUserID, opts, timeutil.NowUTC())
}

// getJobsSummary uses one timestamp for the window and all count buckets.
func (s *JobService) getJobsSummary(ctx context.Context, actorUserID int, opts JobSummaryOptions, asOf time.Time) (*JobCountsSummary, error) {
	if err := validateJobQueryScope(opts.ProjectID, opts.TriggerType); err != nil {
		return nil, err
	}
	summary := &JobCountsSummary{AsOf: asOf.UTC(), RecentFailedSince: asOf.UTC().Add(-7 * 24 * time.Hour)}
	var counts []struct {
		Status string `json:"status"`
		Count  int    `json:"count"`
	}
	err := s.accessibleJobsQuery(actorUserID, opts.ProjectID, opts.TriggerType).
		Where(job.Or(
			job.StatusIn(JobStatusPending, JobStatusRunning, JobStatusPausing, JobStatusPaused),
			job.And(
				job.StatusEQ(JobStatusFailed),
				jobUpdatedAtCompare(sql.OpGTE, summary.RecentFailedSince),
				jobUpdatedAtCompare(sql.OpLT, summary.AsOf),
			),
		)).
		GroupBy(job.FieldStatus).
		Aggregate(ent.Count()).
		Scan(ctx, &counts)
	if err != nil {
		return nil, fmt.Errorf("get accessible job counts: %w", err)
	}
	for _, count := range counts {
		switch count.Status {
		case JobStatusPending:
			summary.Pending = count.Count
		case JobStatusRunning:
			summary.Running = count.Count
		case JobStatusPausing:
			summary.Pausing = count.Count
		case JobStatusPaused:
			summary.Paused = count.Count
		case JobStatusFailed:
			summary.RecentFailed = count.Count
		}
	}
	return summary, nil
}

func (s *JobService) accessibleJobsQuery(actorUserID, projectID int, triggerType string) *ent.JobQuery {
	q := s.client.Job.Query().Where(job.HasProjectWith(readableProjectPredicate(actorUserID)))
	if projectID != 0 {
		q.Where(job.ProjectIDEQ(projectID))
	}
	if triggerType != "" {
		q.Where(job.TriggerTypeEQ(triggerType))
	}
	return q
}

func validateAccessibleJobOptions(opts AccessibleJobListOptions) error {
	if err := validateJobQueryScope(opts.ProjectID, opts.TriggerType); err != nil {
		return err
	}
	if opts.Limit < 0 || opts.Limit > 100 {
		return fmt.Errorf("%w: limit must be between 1 and 100", ErrInvalidInput)
	}
	if opts.State != "" && opts.Status != "" {
		return fmt.Errorf("%w: state and status are mutually exclusive", ErrInvalidInput)
	}
	switch opts.State {
	case "", "active", "terminal", "all":
	default:
		return fmt.Errorf("%w: invalid state", ErrInvalidInput)
	}
	switch opts.Status {
	case "", JobStatusPending, JobStatusRunning, JobStatusPausing, JobStatusPaused, JobStatusCompleted, JobStatusFailed, JobStatusCancelled:
	default:
		return fmt.Errorf("%w: invalid status", ErrInvalidInput)
	}
	if opts.UpdatedFrom != nil && opts.UpdatedBefore != nil && !opts.UpdatedFrom.Before(*opts.UpdatedBefore) {
		return fmt.Errorf("%w: updated_from must precede updated_before", ErrInvalidInput)
	}
	return nil
}

func validateJobQueryScope(projectID int, triggerType string) error {
	if projectID < 0 {
		return fmt.Errorf("%w: project_id must be positive", ErrInvalidInput)
	}
	switch triggerType {
	case "", JobTriggerManual, "file_update", "glossary_change", "web_edit":
		return nil
	default:
		return fmt.Errorf("%w: invalid trigger_type", ErrInvalidInput)
	}
}

func encodeAccessibleJobCursor(row *ent.Job) (string, error) {
	raw, err := json.Marshal(accessibleJobCursor{Version: 1, UpdatedAt: row.UpdatedAt.UTC(), ID: row.ID})
	if err != nil {
		return "", fmt.Errorf("encode accessible job cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeAccessibleJobCursor(raw string) (accessibleJobCursor, error) {
	invalid := fmt.Errorf("%w: invalid cursor", ErrInvalidInput)
	if len(raw) > 1024 {
		return accessibleJobCursor{}, invalid
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil || base64.RawURLEncoding.EncodeToString(data) != raw {
		return accessibleJobCursor{}, invalid
	}
	var cursor accessibleJobCursor
	if err := json.Unmarshal(data, &cursor); err != nil || cursor.Version != 1 || cursor.ID <= 0 || cursor.UpdatedAt.IsZero() {
		return accessibleJobCursor{}, invalid
	}
	cursor.UpdatedAt = cursor.UpdatedAt.UTC()
	return cursor, nil
}
