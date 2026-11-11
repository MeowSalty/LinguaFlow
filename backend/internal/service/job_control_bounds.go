package service

import (
	"context"
	stdsql "database/sql"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	"fmt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobresource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobround"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/project"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
	"strings"
	"sync"
	"sync/atomic"
)

type jobControlBudget struct {
	root    *resourcebudget.RootBudget
	prepare func(context.Context, int) error
	limit   int64
}
type jobControlOwner struct {
	ctx     context.Context
	release func()
	build   func(func() error) error
	resize  func(int64, int64) error
	prepare func(func() error) error
}
type jobControlOwners struct {
	mu     sync.Mutex
	owners map[*ent.Job]jobControlOwner
}
type executionControlValidationError struct{ err error }

func (e *executionControlValidationError) Error() string { return e.err.Error() }
func (e *executionControlValidationError) Unwrap() error { return e.err }

func (s *JobService) SetExecutionControlBudget(root *resourcebudget.RootBudget, prepare func(context.Context, int) error, limit int64) {
	s.controlBudget = jobControlBudget{root, prepare, limit}
	s.controlOwnerState = &jobControlOwners{owners: make(map[*ent.Job]jobControlOwner)}
}

type controlDefinitionKey struct{}
type controlDefinition struct {
	id       int
	snapshot *JobExecutionSnapshot
}

var controlSequence atomic.Uint64

// Execution controls need a snapshot and source identities, not history detail
// JSON/segment lists. Counts and physical byte projections precede the shared
// Root grant, codec and every ORM resource allocation in one SQL snapshot.
func (s *JobService) loadControlDefinition(ctx context.Context, id int, actor int) (context.Context, *ent.Job, func(), error) {
	cleanup := func() {}
	if actor > 0 {
		ok, err := s.client.Job.Query().Where(job.IDEQ(id), job.HasProjectWith(readableProjectPredicate(actor))).Exist(ctx)
		if err != nil {
			return ctx, nil, cleanup, err
		}
		if !ok {
			exists, err := s.client.Job.Query().Where(job.IDEQ(id)).Exist(ctx)
			if err != nil {
				return ctx, nil, cleanup, err
			}
			if !exists {
				return ctx, nil, cleanup, ErrJobNotFound
			}
			return ctx, nil, cleanup, ErrForbidden
		}
	}
	if err := s.CheckExecutionFormat(ctx, id); err != nil {
		return ctx, nil, cleanup, err
	}
	root := s.controlBudget.root
	if root != nil {
		owner, err := (workstate.InventorySource{DB: s.client}).Actor(ctx, id)
		if err != nil {
			return ctx, nil, cleanup, err
		}
		if owner <= 0 {
			return ctx, nil, cleanup, ErrJobActorMissing
		}
		ctx = WithExecutionJobActor(ctx, owner, id)
		if s.controlBudget.prepare != nil {
			if err = s.controlBudget.prepare(ctx, owner); err != nil {
				ReleaseExecutionActor(ctx)
				return ctx, nil, cleanup, err
			}
		}
		cleanup = func() { ReleaseExecutionActor(ctx); _ = root.ForgetActor(owner) }
	}
	var driver string
	var stat []struct {
		Bytes  int64 `json:"bytes"`
		Rounds int64 `json:"rounds"`
	}
	err := s.client.Job.Query().Where(job.IDEQ(id)).Modify(func(q *sql.Selector) { driver = q.Dialect(); q.Select(q.C(job.FieldID)) }).Scan(ctx, &[]struct {
		ID int `json:"id"`
	}{})
	if err != nil {
		cleanup()
		return ctx, nil, func() {}, err
	}
	var opts *stdsql.TxOptions
	if driver == dialect.Postgres {
		opts = &stdsql.TxOptions{ReadOnly: true, Isolation: stdsql.LevelRepeatableRead}
	}
	tx, err := s.client.BeginTx(ctx, opts)
	if err != nil {
		cleanup()
		return ctx, nil, func() {}, err
	}
	defer tx.Rollback()
	c := tx.Client()
	err = c.Job.Query().Where(job.IDEQ(id), workstate.SupportedExecution(workstate.ExecutionDefinitionReadLimit)).Modify(func(q *sql.Selector) {
		config := q.C(job.FieldExecutionConfig)
		size := "COALESCE(length(CAST(" + config + " AS BLOB)),0)"
		rounds := "CASE WHEN json_type(" + config + ",'$.rounds')='array' THEN json_array_length(" + config + ",'$.rounds') ELSE -1 END"
		if q.Dialect() == dialect.Postgres {
			size = "COALESCE(octet_length(CAST(" + config + " AS TEXT)),0)"
			rounds = "CASE WHEN jsonb_typeof(" + config + "->'rounds')='array' THEN jsonb_array_length(" + config + "->'rounds') ELSE -1 END"
		}
		q.Select(sql.As(size, "bytes"), sql.As(rounds, "rounds"))
	}).Scan(ctx, &stat)
	if err != nil || len(stat) != 1 {
		cleanup()
		if err == nil {
			err = backend.ErrBuildCapacity
		}
		return ctx, nil, func() {}, err
	}
	resources, err := c.JobResource.Query().Where(jobresource.HasJobWith(job.IDEQ(id))).Count(ctx)
	if err != nil {
		cleanup()
		return ctx, nil, func() {}, err
	}
	roundRows, err := c.JobRound.Query().Where(jobround.JobIDEQ(id)).Count(ctx)
	if err != nil {
		cleanup()
		return ctx, nil, func() {}, err
	}
	n := int64(resources)
	rounds := stat[0].Rounds
	bytes := stat[0].Bytes
	limit := s.controlBudget.limit
	if limit <= 0 || limit > DefaultExecutionInputBytes {
		limit = DefaultExecutionInputBytes
	}
	if bytes < 0 || bytes > limit || n < 0 || n > 65536 || rounds < 1 || rounds > 256 || roundRows < 0 || roundRows > 65536 {
		cleanup()
		return ctx, nil, func() {}, backend.ErrBuildCapacity
	}
	definitions := int64(1) + n + max(int64(roundRows), n*rounds) + rounds
	if definitions > 65536 {
		cleanup()
		return ctx, nil, func() {}, backend.ErrBuildCapacity
	}
	bytes, err = controlHistoryBytes(ctx, c, id, driver)
	if err != nil || bytes < 0 || bytes > limit {
		cleanup()
		if err == nil {
			err = backend.ErrBuildCapacity
		}
		return ctx, nil, func() {}, err
	}
	// Each resource only has fixed integer/version fields and a validated enum.
	ok, err := c.JobResource.Query().Where(jobresource.HasJobWith(job.IDEQ(id)), jobresource.StatusNotIn(JobResourceStatusPending, JobResourceStatusRunning, JobResourceStatusCompleted, JobResourceStatusFailed, JobResourceStatusCancelled)).Exist(ctx)
	if err != nil || ok {
		cleanup()
		if err == nil {
			err = backend.ErrBuildCapacity
		}
		return ctx, nil, func() {}, err
	}
	credit, err := ExecutionInputAllocationBound(bytes, definitions)
	if err != nil {
		cleanup()
		return ctx, nil, func() {}, err
	}
	scope := ExecutionScope(ctx)
	identity := fmt.Sprintf("control:%d:%d", id, controlSequence.Add(1))
	usage := resourcebudget.Vector{PayloadBytes: credit, QueuedDescriptors: definitions, PrepareOwners: 1}
	releaseActor := cleanup
	if root != nil {
		if err = root.Claim(scope, identity, usage); err != nil {
			cleanup()
			return ctx, nil, func() {}, err
		}
	}
	var once sync.Once
	cleanup = func() {
		once.Do(func() {
			if root != nil {
				_ = root.Transfer(scope, identity, resourcebudget.Vector{})
			}
			releaseActor()
		})
	}
	build := func(fn func() error) error {
		if root == nil {
			return fn()
		}
		next := usage
		next.BuildWorkers = 1
		if err := root.Transfer(scope, identity, next); err != nil {
			return err
		}
		defer func() { _ = root.Transfer(scope, identity, usage) }()
		return fn()
	}
	if err = validateControlJSON(ctx, c, id, bytes, limit, build); err != nil {
		cleanup()
		return ctx, nil, func() {}, err
	}
	config, err := workstate.LoadExecutionConfig(ctx, c, id, limit, build)
	if err != nil {
		cleanup()
		return ctx, nil, func() {}, err
	}
	row, err := c.Job.Query().Where(job.IDEQ(id)).Select(job.FieldID, job.FieldStatus, job.FieldPauseRequested, job.FieldRetryEpoch).Only(ctx)
	if err != nil {
		cleanup()
		return ctx, nil, func() {}, err
	}
	row.ExecutionConfig = config
	var snapshot *JobExecutionSnapshot
	if err = build(func() error { var err error; snapshot, err = GetSnapshot(row); return err }); err != nil {
		cleanup()
		return ctx, nil, func() {}, &executionControlValidationError{err}
	}
	row.Edges.JobResources, err = c.JobResource.Query().Where(jobresource.HasJobWith(job.IDEQ(id))).Select(jobresource.FieldID, jobresource.FieldStatus, jobresource.FieldSourceGeneration, jobresource.FieldSourceRevisionID).Order(ent.Asc(jobresource.FieldID)).Limit(65536).All(ctx)
	if err != nil {
		cleanup()
		return ctx, nil, func() {}, err
	}
	var digest string
	if err = build(func() error { var err error; digest, err = workstate.ExecutionSnapshotDigest(config); return err }); err != nil {
		cleanup()
		return ctx, nil, func() {}, err
	}
	if err = tx.Commit(); err != nil {
		cleanup()
		return ctx, nil, func() {}, err
	}
	ctx = context.WithValue(ctx, controlDefinitionKey{}, &controlDefinition{id, snapshot})
	ctx = workstate.WithExecutionSnapshotDigest(ctx, digest)
	usage.PrepareOwners = 0
	usage.BuildWorkers = 0
	if root != nil {
		if err = root.Transfer(scope, identity, usage); err != nil {
			cleanup()
			return ctx, nil, func() {}, err
		}
	}
	resize := func(n, defs int64) error {
		credit, err := ExecutionInputAllocationBound(n, defs)
		if err != nil {
			return err
		}
		next := usage
		// The previous typed definition remains live until the service hands
		// this result off. Retain the larger finite copy arena across overlap.
		next.PayloadBytes = max(usage.PayloadBytes, credit)
		next.QueuedDescriptors = max(usage.QueuedDescriptors, defs)
		if root != nil {
			if err = root.Transfer(scope, identity, next); err != nil {
				return err
			}
		}
		usage.PayloadBytes = next.PayloadBytes
		usage.QueuedDescriptors = next.QueuedDescriptors
		return nil
	}
	prepare := func(fn func() error) error {
		usage.PrepareOwners = 1
		if root != nil {
			if err := root.Transfer(scope, identity, usage); err != nil {
				usage.PrepareOwners = 0
				return err
			}
		}
		defer func() {
			usage.PrepareOwners = 0
			if root != nil {
				_ = root.Transfer(scope, identity, usage)
			}
		}()
		return fn()
	}
	ctx = context.WithValue(ctx, controlOwnerKey{}, jobControlOwner{ctx, cleanup, build, resize, prepare})
	return ctx, row, cleanup, nil
}

type controlOwnerKey struct{}

func controlHistoryBytes(ctx context.Context, c *ent.Client, id int, driver string) (int64, error) {
	length := func(alias string, fields []string) string {
		parts := make([]string, len(fields))
		for i, f := range fields {
			col := alias + "." + f
			if driver == dialect.Postgres {
				parts[i] = "COALESCE(octet_length(CAST(" + col + " AS TEXT)),0)"
			} else {
				parts[i] = "COALESCE(length(CAST(" + col + " AS BLOB)),0)"
			}
		}
		return strings.Join(parts, "+")
	}
	rounds := []string{jobround.FieldID, jobround.FieldRoundIndex, jobround.FieldMode, jobround.FieldStatus, jobround.FieldSegmentTotal, jobround.FieldSegmentCompleted, jobround.FieldErrorMessage, jobround.FieldStartedAt, jobround.FieldFinishedAt}
	query := "SELECT " + length("j", job.Columns) + "+" + length("p", project.Columns) + "+" + length("u", user.Columns) + "+COALESCE((SELECT SUM(" + length("jr", jobresource.Columns) + "+" + length("r", resource.Columns) + ") FROM job_resources jr JOIN resources r ON r.id=jr." + jobresource.ResourceColumn + " WHERE jr." + jobresource.JobColumn + "=j.id),0)+COALESCE((SELECT SUM(" + length("rd", rounds) + ") FROM job_rounds rd WHERE rd.job_id=j.id),0) FROM jobs j JOIN projects p ON p.id=j.project_id LEFT JOIN users u ON u.id=j." + job.CreatedByColumn + " WHERE j.id=$1"
	rows, err := c.QueryContext(ctx, query, id)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	if !rows.Next() {
		return 0, ErrJobNotFound
	}
	var bytes int64
	err = rows.Scan(&bytes)
	if err != nil {
		return 0, err
	}
	return bytes, rows.Err()
}

func validateControlJSON(ctx context.Context, c *ent.Client, id int, bytes, limit int64, build func(func() error) error) error {
	b, err := executionMetadataBounds(bytes)
	if err != nil {
		return err
	}
	for _, query := range []string{"SELECT execution_config,pipeline_runtime_state FROM jobs WHERE id=$1", "SELECT p.config FROM projects p JOIN jobs j ON j.project_id=p.id WHERE j.id=$1", "SELECT segment_ids FROM job_resources WHERE " + jobresource.JobColumn + "=$1"} {
		rows, err := c.QueryContext(ctx, query, id)
		if err != nil {
			return err
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return err
		}
		for rows.Next() {
			raw := make([][]byte, len(columns))
			dest := make([]any, len(raw))
			for i := range raw {
				dest[i] = &raw[i]
			}
			if err = rows.Scan(dest...); err != nil {
				break
			}
			for _, data := range raw {
				if len(data) > 0 {
					var ignored any
					if err = build(func() error { return b.DecodeJSONLimited(data, &ignored, limit) }); err != nil {
						break
					}
				}
			}
			clear(raw)
			if err != nil {
				break
			}
		}
		readErr := rows.Err()
		closeErr := rows.Close()
		if err != nil {
			return err
		}
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

// Resume/Retry return a Root owned result. Root backed direct callers release
// it explicitly after consuming its fields; HTTP handlers release after Write.
func (s *JobService) controlResult(ctx context.Context, actor, id int, release func()) (*ent.Job, error) {
	var row *ent.Job
	owner, _ := ctx.Value(controlOwnerKey{}).(jobControlOwner)
	prepare := owner.prepare
	if prepare == nil {
		prepare = func(fn func() error) error { return fn() }
	}
	err := prepare(func() error {
		var driver string
		if err := s.client.Job.Query().Where(job.IDEQ(id)).Modify(func(q *sql.Selector) { driver = q.Dialect(); q.Select(q.C(job.FieldID)) }).Scan(ctx, &[]struct {
			ID int `json:"id"`
		}{}); err != nil {
			return err
		}
		var opts *stdsql.TxOptions
		if driver == dialect.Postgres {
			opts = &stdsql.TxOptions{ReadOnly: true, Isolation: stdsql.LevelRepeatableRead}
		}
		tx, err := s.client.BeginTx(ctx, opts)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		c := tx.Client()
		bytes, err := controlHistoryBytes(ctx, c, id, driver)
		if err != nil {
			return err
		}
		limit := s.controlBudget.limit
		if limit <= 0 || limit > DefaultExecutionInputBytes {
			limit = DefaultExecutionInputBytes
		}
		if bytes < 0 || bytes > limit {
			return backend.ErrBuildCapacity
		}
		n, err := c.JobResource.Query().Where(jobresource.HasJobWith(job.IDEQ(id))).Count(ctx)
		if err != nil {
			return err
		}
		rounds, err := c.JobRound.Query().Where(jobround.JobIDEQ(id)).Count(ctx)
		if err != nil {
			return err
		}
		if n < 0 || n > 65536 || rounds < 0 || rounds > 65536 {
			return backend.ErrBuildCapacity
		}
		defs := int64(1) + int64(n) + int64(rounds)
		if defs > 65536 {
			return backend.ErrBuildCapacity
		}
		if owner.resize != nil {
			if err = owner.resize(bytes, defs); err != nil {
				return err
			}
		}
		row, err = loadControlResultRows(ctx, c, actor, id, bytes, limit, owner.build)
		if err != nil {
			return err
		}
		return tx.Commit()
	})
	if err != nil {
		return nil, err
	}
	if s.controlBudget.root == nil {
		release()
		return row, nil
	}
	s.controlOwnerState.mu.Lock()
	s.controlOwnerState.owners[row] = owner
	s.controlOwnerState.mu.Unlock()
	return row, nil
}

// The write transaction has joined. Only fixed identity/digest observations
// are needed now; all previous JSON and source-array references end before
// reading the final history result in its own bounded snapshot.
func retireControlDefinition(ctx context.Context, current *ent.Job) {
	current.ExecutionConfig = nil
	current.Edges = ent.JobEdges{}
	if definition, ok := ctx.Value(controlDefinitionKey{}).(*controlDefinition); ok {
		definition.snapshot = nil
	}
}
func (s *JobService) ExecutionControlContext(row *ent.Job, fallback context.Context) context.Context {
	if s.controlOwnerState == nil {
		return fallback
	}
	s.controlOwnerState.mu.Lock()
	owner, ok := s.controlOwnerState.owners[row]
	s.controlOwnerState.mu.Unlock()
	if ok {
		return owner.ctx
	}
	return fallback
}
func (s *JobService) RunExecutionControlBuild(row *ent.Job, fn func() error) error {
	if s.controlOwnerState == nil {
		return fn()
	}
	s.controlOwnerState.mu.Lock()
	owner, ok := s.controlOwnerState.owners[row]
	s.controlOwnerState.mu.Unlock()
	if ok && owner.build != nil {
		return owner.build(fn)
	}
	return fn()
}
func (s *JobService) ReleaseExecutionControlResult(row *ent.Job) {
	if s.controlOwnerState == nil {
		return
	}
	s.controlOwnerState.mu.Lock()
	owner, ok := s.controlOwnerState.owners[row]
	delete(s.controlOwnerState.owners, row)
	s.controlOwnerState.mu.Unlock()
	if ok {
		row.ExecutionConfig = nil
		row.Edges = ent.JobEdges{}
		owner.release()
	}
}
