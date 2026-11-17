package service

import (
	"context"
	stdjson "encoding/json"
	"entgo.io/ent/dialect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobresource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/project"
)

// SQL reads own Prepare and finite backing. Only JSON construction enters a
// short Build; generated ORM JSON decoders are excluded from this query.
func loadControlResultRows(ctx context.Context, c *ent.Client, actor, id int, bytes, limit int64, driver string, build func(func() error) error) (*ent.Job, error) {
	if build == nil {
		build = func(fn func() error) error { return fn() }
	}
	fields := make([]string, 0, len(job.Columns))
	for _, f := range job.Columns {
		if f != job.FieldExecutionConfig {
			fields = append(fields, f)
		}
	}
	projectFields := make([]string, 0, len(project.Columns))
	for _, f := range project.Columns {
		if f != project.FieldConfig {
			projectFields = append(projectFields, f)
		}
	}
	resourceFields := make([]string, 0, len(jobresource.Columns))
	for _, f := range jobresource.Columns {
		if f != jobresource.FieldSegmentIds {
			resourceFields = append(resourceFields, f)
		}
	}
	row, err := c.Job.Query().Where(job.IDEQ(id), job.HasProjectWith(readableProjectPredicate(actor))).Select(fields...).WithCreatedBy().WithProject(func(q *ent.ProjectQuery) { q.Select(projectFields...) }).WithJobResources(func(q *ent.JobResourceQuery) {
		q.Select(resourceFields...).WithResource().WithRounds(func(rq *ent.JobRoundQuery) {
			rq.Select(jobround.FieldID, jobround.FieldRoundIndex, jobround.FieldMode, jobround.FieldStatus, jobround.FieldSegmentTotal, jobround.FieldSegmentCompleted, jobround.FieldErrorMessage, jobround.FieldStartedAt, jobround.FieldFinishedAt).Order(ent.Asc(jobround.FieldRoundIndex))
		}).Order(ent.Asc(jobresource.FieldID))
	}).Only(ctx)
	if err != nil {
		return nil, err
	}
	b, err := executionMetadataBounds(ctx, bytes)
	if err != nil {
		return nil, err
	}
	rows, err := c.QueryContext(ctx, "SELECT j.execution_config,p.config FROM jobs j JOIN projects p ON p.id=j.project_id WHERE j.id=$1", id)
	if err != nil {
		return nil, err
	}
	var config, projectConfig []byte
	if !rows.Next() {
		rows.Close()
		return nil, ErrJobNotFound
	}
	err = rows.Scan(&config, &projectConfig)
	readErr := rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return nil, err
	}
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	err = build(func() error {
		if err := b.DecodeJSONLimited(config, &row.ExecutionConfig, limit); err != nil {
			return err
		}
		if len(projectConfig) > 0 {
			return b.DecodeJSONLimited(projectConfig, &row.Edges.Project.Config, limit)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	clear(config)
	clear(projectConfig)
	segmentIDsLength := "json_array_length(segment_ids)"
	if driver == dialect.Postgres {
		segmentIDsLength = "jsonb_array_length(segment_ids)"
	}
	rows, err = c.QueryContext(ctx, "SELECT id,segment_ids,"+segmentIDsLength+" FROM job_resources WHERE "+jobresource.JobColumn+"=$1 ORDER BY id", id)
	if err != nil {
		return nil, err
	}
	index := 0
	for rows.Next() {
		var current int
		var raw []byte
		var count int64
		if err = rows.Scan(&current, &raw, &count); err != nil {
			break
		}
		if index >= len(row.Edges.JobResources) || current != row.Edges.JobResources[index].ID {
			err = ErrInvalidInput
			break
		}
		item := row.Edges.JobResources[index]
		if len(raw) > 0 {
			if count < 0 || count > MaxJobDefinitionSegmentIDs || int64(len(raw)) > limit {
				err = backend.ErrBuildCapacity
				break
			}
			// The SQL array length sizes the decoded slice exactly; decoding is
			// not charged against the shared node arena.
			err = build(func() error {
				item.SegmentIds = make([]int, 0, int(count))
				return stdjson.Unmarshal(raw, &item.SegmentIds)
			})
			if err != nil {
				break
			}
		}
		clear(raw)
		index++
	}
	readErr = rows.Err()
	closeErr = rows.Close()
	if err != nil {
		return nil, err
	}
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if index != len(row.Edges.JobResources) {
		return nil, ErrInvalidInput
	}
	return row, nil
}
