package workstate

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"modernc.org/sqlite"
)

func scopeProjectionFixture(t *testing.T) (context.Context, *ent.Client, Scope, *storageSQLProbe) {
	t.Helper()
	ctx := t.Context()
	probe := &storageSQLProbe{}
	db := sql.OpenDB(storageSQLConnector{name: filepath.Join(t.TempDir(), "scope-projection.db") + "?_pragma=foreign_keys(1)", probe: probe, sqlite: &sqlite.Driver{}})
	db.SetMaxOpenConns(1)
	client := ent.NewClient(ent.Driver(database.NewDriver(entsql.OpenDB(dialect.SQLite, db))))
	t.Cleanup(func() { _ = client.Close(); _ = db.Close() })
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	_, _, _, scope, _ := seedFixture(t, ctx, client)
	client.Job.UpdateOneID(scope.JobID).SetRetryEpoch(9).SetExecutionConfig(map[string]any{"unused": strings.Repeat("x", 4096)}).ExecX(ctx)
	client.Resource.UpdateOneID(scope.ResourceID).SetSourceGeneration(5).SetTranslationGeneration(11).SetCurrentSourceRevisionID(37).ExecX(ctx)
	scope.RetryEpoch, scope.SourceGeneration = 9, 5
	revision := 37
	scope.SourceRevisionID = &revision
	return ctx, client, scope, probe
}

var scopeProjectionSQL = regexp.MustCompile("(?i)\\bSELECT\\s+(.+?)\\s+FROM\\s+[`\"]?([a-z_]+)")

func scopeSelectedColumns(t *testing.T, query string) (string, []string) {
	t.Helper()
	match := scopeProjectionSQL.FindStringSubmatch(query)
	if len(match) != 3 {
		t.Fatalf("unrecognized actual projection: %s", query)
	}
	var columns []string
	for _, field := range strings.Split(strings.TrimPrefix(match[1], "DISTINCT "), ",") {
		if dot := strings.LastIndexByte(field, '.'); dot >= 0 {
			field = field[dot+1:]
		}
		columns = append(columns, strings.Trim(field, " `\""))
	}
	return match[2], columns
}

func TestGuardScopeProjectsOnlyRequiredLockedFacts(t *testing.T) {
	for _, name := range []string{"nonzero_revision", "null_revision"} {
		t.Run(name, func(t *testing.T) {
			ctx, client, scope, probe := scopeProjectionFixture(t)
			if name == "null_revision" {
				client.Resource.UpdateOneID(scope.ResourceID).ClearCurrentSourceRevisionID().ExecX(ctx)
				scope.SourceRevisionID = nil
			}
			probe.reset()
			client.Job.GetX(ctx, scope.JobID)
			client.Resource.GetX(ctx, scope.ResourceID)
			client.JobRound.GetX(ctx, scope.RoundID)
			client.JobResource.GetX(ctx, scope.JobResourceID)
			full := probe.stop()
			probe.reset()
			err := Transaction(ctx, client, func(tx *ent.Client) error { return guardScope(ctx, tx, scope, true) })
			actual := probe.stop()
			if err != nil || actual.begins != 1 || actual.commits != 1 || actual.queries != 5 || actual.rows != 5 || len(actual.calls) != 7 {
				t.Fatalf("guard changed physical operations: %+v error=%v", actual, err)
			}
			if actual.calls[0].kind != "exec" || !strings.HasPrefix(actual.calls[0].query, "UPDATE `jobs`") || actual.calls[2].kind != "exec" || !strings.HasPrefix(actual.calls[2].query, "UPDATE `resources`") {
				t.Fatal("job lock was not first or resource CAS disappeared", actual.calls)
			}
			want := []struct {
				table   string
				columns []string
			}{{"jobs", []string{"id", "retry_epoch", "status", "pause_requested"}}, {"resources", []string{"id", "current_source_revision_id"}}, {"job_rounds", []string{"id", "job_id", "job_resource_id"}}, {"job_resources", []string{"id"}}}
			queries := []storageSQLCall{}
			for _, call := range actual.calls {
				if call.kind == "query" {
					queries = append(queries, call)
				}
			}
			var projectionBytes int64
			for i, expected := range want {
				table, columns := scopeSelectedColumns(t, queries[i].query)
				if table != expected.table || !reflect.DeepEqual(columns, expected.columns) || queries[i].rows != 1 {
					t.Fatalf("unexpected actual fields/FKs/rows: table=%s columns=%v call=%+v", table, columns, queries[i])
				}
				projectionBytes += queries[i].valueBytes
			}
			if !strings.Contains(queries[4].query, "job_resources") || queries[4].rows != 1 {
				t.Fatal("resource ownership edge was not read from SQL", queries[4])
			}
			if full.queries != 4 || full.rows != 4 || full.valueBytes < 4096 || projectionBytes >= full.valueBytes || projectionBytes > 128 {
				t.Fatalf("unused definitions were returned or typed bytes not observed: full=%+v projected=%d", full, projectionBytes)
			}
			r := client.Resource.GetX(ctx, scope.ResourceID)
			if r.SourceGeneration != 5 || r.TranslationGeneration != 11 || !sameInt(r.CurrentSourceRevisionID, scope.SourceRevisionID) {
				t.Fatal("guard CAS changed source facts")
			}
			t.Logf("physical queries=%d rows=%d projection_fields=4/2/3/1 typed_projection_bytes=%d full_get_bytes=%d", actual.queries, actual.rows, projectionBytes, full.valueBytes)
		})
	}
}

func TestGuardScopeProjectedFactsRejectDriftAndPreserveRollback(t *testing.T) {
	for _, name := range []string{"retry_epoch", "stopped_status", "pause_dispatch", "pausing_dispatch", "pause_drain", "generation", "revision", "null_revision", "round_job", "round_job_resource", "resource_owner", "missing_round", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			ctx, client, scope, probe := scopeProjectionFixture(t)
			dispatch := true
			want := ErrStopped
			switch name {
			case "retry_epoch":
				scope.RetryEpoch--
			case "stopped_status":
				client.Job.UpdateOneID(scope.JobID).SetStatus("completed").ExecX(ctx)
			case "pause_dispatch":
				client.Job.UpdateOneID(scope.JobID).SetPauseRequested(true).ExecX(ctx)
			case "pausing_dispatch", "pause_drain":
				client.Job.UpdateOneID(scope.JobID).SetStatus("pausing").SetPauseRequested(true).ExecX(ctx)
				if name == "pause_drain" {
					dispatch, want = false, nil
				}
			case "generation":
				scope.SourceGeneration++
				want = ErrStale
			case "revision":
				revision := 38
				scope.SourceRevisionID, want = &revision, ErrStale
			case "null_revision":
				scope.SourceRevisionID, want = nil, ErrStale
			case "round_job":
				projectID := client.Job.GetX(ctx, scope.JobID).ProjectID
				j := client.Job.Create().SetProjectID(projectID).SetExecutionPlanID(1).SetStatus("running").SetRetryEpoch(scope.RetryEpoch).SaveX(ctx)
				scope.JobID, want = j.ID, ErrManifest
			case "round_job_resource":
				projectID := client.Job.GetX(ctx, scope.JobID).ProjectID
				r := client.Resource.Create().SetProjectID(projectID).SetPath("other-round-resource.txt").SetStoragePath("other-round-resource.txt").SetFormat("txt").SaveX(ctx)
				jr := client.JobResource.Create().SetJobID(scope.JobID).SetResourceID(r.ID).SetStatus("running").SaveX(ctx)
				scope.JobResourceID = jr.ID
				want = ErrManifest
			case "resource_owner":
				projectID := client.Job.GetX(ctx, scope.JobID).ProjectID
				r := client.Resource.Create().SetProjectID(projectID).SetPath("other.txt").SetStoragePath("other.txt").SetFormat("txt").SetSourceGeneration(5).SetCurrentSourceRevisionID(37).SaveX(ctx)
				scope.ResourceID, want = r.ID, ErrManifest
			case "missing_round":
				scope.RoundID = 123456
				want = nil // Preserve ent's actual NotFound classification below.
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx, want = cancelled, context.Canceled
			}
			factsCtx := t.Context()
			beforeJob := client.Job.GetX(factsCtx, scope.JobID)
			beforeResource := client.Resource.GetX(factsCtx, scope.ResourceID)
			probe.reset()
			err := Transaction(ctx, client, func(tx *ent.Client) error { return guardScope(ctx, tx, scope, dispatch) })
			actual := probe.stop()
			if name == "missing_round" {
				if !ent.IsNotFound(err) {
					t.Fatal("missing row classification changed", err)
				}
			} else if want == nil && err != nil || want != nil && !errors.Is(err, want) {
				t.Fatalf("wrong guard result for %s: %v expected=%v", name, err, want)
			}
			if want != nil || name == "missing_round" {
				if actual.commits != 0 {
					t.Fatal("rejected scope committed its tentative lock/CAS")
				}
				afterJob := client.Job.GetX(factsCtx, scope.JobID)
				afterResource := client.Resource.GetX(factsCtx, scope.ResourceID)
				if !afterJob.UpdatedAt.Equal(beforeJob.UpdatedAt) || !afterResource.UpdatedAt.Equal(beforeResource.UpdatedAt) || afterJob.RetryEpoch != beforeJob.RetryEpoch || afterResource.TranslationGeneration != beforeResource.TranslationGeneration {
					t.Fatal("rejected guard did not roll back actual writes")
				}
			}
		})
	}
}
