// Package persistenceacceptance contains isolated acceptance fixtures, never
// production connection discovery. Only explicitly supplied test DSNs are used.
package persistenceacceptance

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

type Fixture struct {
	Client *ent.Client
	DB     *sql.DB
	Probe  *Probe
	Store  *workstate.Store
	Scope  workstate.Scope
	IDs    []int
}

// FullWorkload selects full size only in an explicitly invoked acceptance
// runner. Ordinary CI checks the same protocols on smaller fixtures.
func FullWorkload() bool {
	return os.Getenv("LINGUAFLOW_PERSISTENCE_DRIVER") != ""
}

func Driver() string {
	if os.Getenv("LINGUAFLOW_PERSISTENCE_DRIVER") == dialect.Postgres {
		return dialect.Postgres
	}
	return dialect.SQLite
}

// Open owns a fresh SQLite file or a unique schema in a test-only PostgreSQL
// instance. PostgreSQL selection without its DSN fails instead of skipping.
func Open(t testing.TB) (*ent.Client, *sql.DB, *Probe) {
	t.Helper()
	name := Driver()
	var db *sql.DB
	if name == dialect.Postgres {
		dsn := os.Getenv("LINGUAFLOW_TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Fatal("isolated PostgreSQL acceptance requires its runner-owned test DSN")
		}
		cfg, err := pgx.ParseConfig(dsn)
		if err != nil {
			t.Fatal("invalid test-only PostgreSQL DSN")
		}
		admin := stdlib.OpenDB(*cfg)
		schema := fmt.Sprintf("persistence_%d", time.Now().UnixNano())
		quoted := pgx.Identifier{schema}.Sanitize()
		if _, err := admin.ExecContext(t.Context(), "CREATE SCHEMA "+quoted); err != nil {
			admin.Close()
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := admin.ExecContext(ctx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
				t.Errorf("remove owned test schema: %v", err)
			}
			admin.Close()
		})
		cfg.RuntimeParams["search_path"] = schema
		cfg.RuntimeParams["timezone"] = "UTC"
		db = stdlib.OpenDB(*cfg)
	} else {
		var err error
		db, err = sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "acceptance.db"))+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
		if err != nil {
			t.Fatal(err)
		}
	}
	db.SetMaxOpenConns(4)
	p := &Probe{}
	driver := &observedDriver{Driver: database.NewDriver(entsql.OpenDB(name, db)), probe: p}
	c := ent.NewClient(ent.Driver(driver))
	t.Cleanup(func() { c.Close(); db.Close() })
	if err := c.Schema.Create(t.Context()); err != nil {
		t.Fatal(err)
	}
	return c, db, p
}

func Seed(t testing.TB, n int, seal bool) *Fixture {
	t.Helper()
	c, db, p := Open(t)
	ctx := t.Context()
	project := c.Project.Create().SetName("isolated-persistence").SaveX(ctx)
	resource := c.Resource.Create().SetProjectID(project.ID).SetPath("fixture.txt").SetStoragePath("fixture.txt").SetFormat("txt").SaveX(ctx)
	job := c.Job.Create().SetProjectID(project.ID).SetExecutionPlanID(1).SetExecutionConfig(map[string]any{"schema_version": 4, "defaults_version": 4, "response_format_version": 1}).SetStatus("running").SaveX(ctx)
	return SeedResource(t, c, db, p, project.ID, job.ID, resource.ID, n, seal)
}

func SeedResource(t testing.TB, c *ent.Client, db *sql.DB, p *Probe, projectID, jobID, resourceID, n int, seal bool) *Fixture {
	t.Helper()
	ctx := t.Context()
	j := c.Job.GetX(ctx, jobID)
	if len(j.ExecutionConfig) == 0 {
		c.Job.UpdateOneID(jobID).SetExecutionConfig(map[string]any{"schema_version": 4, "defaults_version": 4, "response_format_version": 1}).ExecX(ctx)
	}
	jr := c.JobResource.Create().SetJobID(jobID).SetResourceID(resourceID).SetStatus("running").SaveX(ctx)
	round := c.JobRound.Create().SetJobID(jobID).SetJobResourceID(jr.ID).SetRoundIndex(0).SetMode("extract").SetStatus("running").SaveX(ctx)
	f := &Fixture{Client: c, DB: db, Probe: p, Store: workstate.NewStore(c), Scope: workstate.Scope{JobID: jobID, ResourceID: resourceID, JobResourceID: jr.ID, RoundID: round.ID}}
	for offset := 0; offset < n; offset += 300 {
		builders := make([]*ent.SegmentCreate, 0, min(300, n-offset))
		for i := offset; i < min(offset+300, n); i++ {
			builders = append(builders, c.Segment.Create().SetResourceID(resourceID).SetSegmentIndex(i).SetSourceText(fmt.Sprintf("synthetic source %d", i)))
		}
		rows, err := c.Segment.CreateBulk(builders...).Save(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			f.IDs = append(f.IDs, row.ID)
		}
	}
	if seal {
		if _, err := f.Store.SealRound(ctx, f.Scope, f.IDs); err != nil {
			t.Fatal(err)
		}
	}
	p.Reset(0)
	return f
}

// SetExecutionSnapshot gives staged fixtures the same digest source as their
// concrete roundStore before reserving any model request.
func (f *Fixture) SetExecutionSnapshot(t testing.TB, spec *execution.JobExecutionSnapshot) {
	t.Helper()
	if err := execution.ValidateSpec(spec); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err = json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	f.Client.Job.UpdateOneID(f.Scope.JobID).SetExecutionConfig(config).ExecX(t.Context())
}
