package worker

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobresource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/project"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

// Job routing has a fixed node bound as well as the Server's descriptor budget.
// These limits precede every ORM/JSON/map/slice allocation for a Job definition.
const maxJobDefinitionNodes int64 = 65536
const maxJobDefinitionRounds int64 = 256

func (r *JobRunner) loadJobDefinition(ctx context.Context, id int) (jobCtx context.Context, exec *service.JobExecution, snapshot *service.JobExecutionSnapshot, registry *roundRegistry, close func(), err error) {
	close = func() {}
	source := r.budgetSource
	if source.DB == nil {
		source = workstate.InventorySource{DB: r.client, Driver: r.dbDriver, ItemBytes: r.pipeCfg.Candidates.ItemBytes}
	}
	actor, err := source.Actor(ctx, id)
	if err != nil {
		return
	}
	if actor <= 0 {
		err = service.ErrJobActorMissing
		return
	}
	jobCtx = service.WithExecutionJobActor(ctx, actor, id)
	if r.prepareActor != nil {
		if err = r.prepareActor(jobCtx, actor); err != nil {
			return
		}
	}
	defer func() {
		if err != nil {
			service.ReleaseExecutionActor(jobCtx)
		}
	}()
	var opts *sql.TxOptions
	if r.dbDriver == "postgres" {
		opts = &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
	}
	tx, err := r.client.BeginTx(jobCtx, opts)
	if err != nil {
		return
	}
	defer tx.Rollback()
	c := tx.Client()
	length := func(alias string, columns []string) string {
		parts := make([]string, 0, len(columns))
		for _, column := range columns {
			if r.dbDriver == "postgres" {
				parts = append(parts, "COALESCE(OCTET_LENGTH(CAST("+alias+"."+column+" AS TEXT)),0)")
			} else {
				parts = append(parts, "COALESCE(LENGTH(CAST("+alias+"."+column+" AS BLOB)),0)")
			}
		}
		return strings.Join(parts, "+")
	}
	row := func(q string, args ...any) (*sql.Rows, error) { return c.QueryContext(jobCtx, q, args...) }
	var bytes, resources, roundRows, rounds int64
	arrayLength := "COALESCE(json_array_length(j.execution_config,'$.rounds'),0)"
	if r.dbDriver == "postgres" {
		arrayLength = "COALESCE(jsonb_array_length(j.execution_config->'rounds'),0)"
	}
	rows, e := row("SELECT "+length("j", job.Columns)+"+"+length("p", project.Columns)+","+arrayLength+" FROM jobs j JOIN projects p ON p.id=j.project_id WHERE j.id=$1", id)
	if e != nil {
		err = e
		return
	}
	if !rows.Next() {
		err = service.ErrJobNotFound
		rows.Close()
		return
	}
	err = rows.Scan(&bytes, &rounds)
	err = joinReadError(err, rows.Err(), rows.Close())
	if err != nil {
		return
	}
	rows, e = row("SELECT COUNT(*),COALESCE(SUM("+length("jr", jobresource.Columns)+"+"+length("r", resource.Columns)+"),0) FROM job_resources jr JOIN resources r ON r.id=jr."+jobresource.ResourceColumn+" WHERE jr."+jobresource.JobColumn+"=$1", id)
	if e != nil {
		err = e
		return
	}
	var resourceBytes int64
	rows.Next()
	err = rows.Scan(&resources, &resourceBytes)
	err = joinReadError(err, rows.Err(), rows.Close())
	if err != nil {
		return
	}
	rows, e = row("SELECT COUNT(*) FROM job_rounds WHERE job_id=$1", id)
	if e != nil {
		err = e
		return
	}
	rows.Next()
	err = rows.Scan(&roundRows)
	err = joinReadError(err, rows.Err(), rows.Close())
	if err != nil {
		return
	}
	if rounds < 1 || rounds > maxJobDefinitionRounds || resources < 0 || resources > maxJobDefinitionNodes || roundRows < 0 || roundRows > maxJobDefinitionNodes || bytes < 0 || resourceBytes < 0 || bytes > service.DefaultExecutionInputBytes-resourceBytes {
		err = backend.ErrBuildCapacity
		return
	}
	bytes += resourceBytes
	definitions := int64(1) + resources + max(roundRows, resources*rounds) + rounds
	if definitions > maxJobDefinitionNodes {
		err = backend.ErrBuildCapacity
		return
	}
	limit := r.pipeCfg.ResponseHandoffBytes
	if limit <= 0 || limit > service.DefaultExecutionInputBytes {
		limit = service.DefaultExecutionInputBytes
	}
	if bytes > limit {
		err = backend.ErrBuildCapacity
		return
	}
	credit, e := service.ExecutionInputAllocationBound(bytes, definitions)
	if e != nil {
		err = e
		return
	}
	scope := service.ExecutionScope(jobCtx)
	identity := fmt.Sprintf("job:%d:definition", id)
	usage := resourcebudget.Vector{PayloadBytes: credit, QueuedDescriptors: definitions, PrepareOwners: 1, BuildWorkers: 1}
	if r.rootBudget != nil {
		if err = r.rootBudget.Claim(scope, identity, usage); err != nil {
			return
		}
	}
	close = func() {
		if r.rootBudget != nil {
			_ = r.rootBudget.Transfer(scope, identity, resourcebudget.Vector{})
		}
		service.ReleaseExecutionActor(jobCtx)
	}
	defer func() {
		if err != nil {
			close()
		}
	}()
	bounds, e := backend.NewBuildBounds(0, bytes)
	if e != nil {
		err = e
		return
	}
	// Validate cumulative JSON nodes before ent's generated JSON decoder runs.
	for _, q := range []string{"SELECT execution_config,pipeline_runtime_state FROM jobs WHERE id=$1", "SELECT p.config FROM projects p JOIN jobs j ON j.project_id=p.id WHERE j.id=$1", "SELECT segment_ids FROM job_resources WHERE " + jobresource.JobColumn + "=$1"} {
		rows, e = row(q, id)
		if e != nil {
			err = e
			return
		}
		cols, e := rows.Columns()
		if e != nil {
			rows.Close()
			err = e
			return
		}
		for rows.Next() {
			raw := make([][]byte, len(cols))
			dest := make([]any, len(cols))
			for i := range raw {
				dest[i] = &raw[i]
			}
			if err = rows.Scan(dest...); err != nil {
				break
			}
			for _, data := range raw {
				if len(data) == 0 {
					continue
				}
				var ignored any
				if err = bounds.DecodeJSONLimited(data, &ignored, limit); err != nil {
					break
				}
			}
			clear(raw)
			if err != nil {
				break
			}
		}
		err = joinReadError(err, rows.Err(), rows.Close())
		if err != nil {
			return
		}
	}
	temporary := service.NewJobService(c, nil, nil, nil, nil, nil, nil, nil, nil)
	exec, err = temporary.LoadJobExecution(jobCtx, id)
	if err != nil {
		return
	}
	snapshot, err = service.GetSnapshot(exec.Job)
	if err != nil {
		return
	}
	registry, err = loadJobRounds(jobCtx, c, id)
	if err != nil {
		return
	}
	err = tx.Commit()
	if err != nil {
		return
	}
	usage.PrepareOwners, usage.BuildWorkers = 0, 0
	if r.rootBudget != nil {
		err = r.rootBudget.Transfer(scope, identity, usage)
	}
	return
}

func joinReadError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
