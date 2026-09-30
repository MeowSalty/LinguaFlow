package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

func TestJobTimePredicatesUseIndexedColumns(t *testing.T) {
	from := time.Date(2026, 9, 22, 12, 30, 0, 123456789, time.FixedZone("east", 8*60*60))
	before := from.Add(7 * 24 * time.Hour)
	for _, name := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(name, func(t *testing.T) {
			selector := entsql.Dialect(name).Select().From(entsql.Table(job.Table))
			jobUpdatedAtCompare(entsql.OpGTE, from)(selector)
			jobUpdatedAtCompare(entsql.OpLT, before)(selector)
			jobUpdatedAtCompare(entsql.OpEQ, from.Truncate(time.Microsecond))(selector)
			jobUpdatedAtDescending(selector)
			query, args := selector.Query()
			for _, unwanted := range []string{"datetime(", "substr(", "printf(", "strftime(", "cast("} {
				if strings.Contains(strings.ToLower(query), unwanted) {
					t.Fatalf("timestamp column must remain indexable: %s", query)
				}
			}
			if len(args) != 3 {
				t.Fatalf("args=%v; want three direct time parameters", args)
			}
			wants := []time.Time{from.UTC(), before.UTC(), from.UTC().Truncate(time.Microsecond)}
			if name == dialect.Postgres {
				wants[0], wants[1] = timeutil.CeilMicrosecond(from), timeutil.CeilMicrosecond(before)
			}
			for i, want := range wants {
				got, ok := args[i].(time.Time)
				if !ok || got != want {
					t.Errorf("argument %d=%v; want UTC %s", i, args[i], want)
				}
			}
		})
	}
}

func TestAccessibleJobsSQLiteUsesTimeIndexes(t *testing.T) {
	client, db, recorder := jobQueryRecordingClient(t)
	actor := createTestUser(t, client, "indexed-query")
	project := createTestProject(t, client, "indexed-query", actor.ID)
	from := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	for i := range 4 {
		seedQueryJob(t, client, project.ID, JobStatusRunning, "manual", from.Add(time.Duration(i)*time.Nanosecond))
	}
	svc := newJobRoundTestService(t, client, nil)
	for _, tc := range []struct {
		name  string
		opts  AccessibleJobListOptions
		index string
	}{
		{"all", AccessibleJobListOptions{State: "all", UpdatedFrom: &from, Limit: 1}, "job_updated_at_id"},
		{"status", AccessibleJobListOptions{Status: JobStatusRunning, UpdatedFrom: &from, Limit: 1}, "job_status_updated_at_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder.queries, recorder.args = nil, nil
			page, err := svc.ListAccessibleJobs(context.Background(), actor.ID, tc.opts)
			if err != nil || page.NextCursor == "" {
				t.Fatalf("list=%+v, err=%v", page, err)
			}
			args := append([]any(nil), recorder.args[0]...)
			for i, arg := range args {
				if timestamp, ok := arg.(time.Time); ok {
					args[i] = timestamp.UTC().Format(timeutil.SQLiteLayout)
				}
			}
			rows, err := db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+recorder.queries[0], args...)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var plans []string
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
					t.Fatal(err)
				}
				plans = append(plans, detail)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			plan := strings.Join(plans, "\n")
			if !strings.Contains(plan, tc.index) || strings.Contains(plan, "TEMP B-TREE") {
				t.Fatalf("want ordered index %s without temporary sorting:\n%s", tc.index, plan)
			}
		})
	}
}

// Query construction needs only Dialect; a bad cursor must fail before touching
// the underlying database. Embedding nil makes any attempted I/O fail the test.
type postgresQueryOnlyDriver struct{ dialect.Driver }

func (postgresQueryOnlyDriver) Dialect() string { return dialect.Postgres }

func TestAccessibleJobsRejectsPostgresNanosecondCursor(t *testing.T) {
	client := ent.NewClient(ent.Driver(postgresQueryOnlyDriver{}))
	svc := newJobRoundTestService(t, client, nil)
	cursor, err := encodeAccessibleJobCursor(&ent.Job{ID: 1, UpdatedAt: time.Date(2026, 9, 29, 0, 0, 0, 1, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ListAccessibleJobs(context.Background(), 1, AccessibleJobListOptions{Cursor: cursor}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cursor error=%v; want ErrInvalidInput for HTTP 400", err)
	}
}
