package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

func insertPlanRows(t *testing.T, c *ent.Client, table string, columns []string, rows [][]any) {
	t.Helper()
	batchSize := 800 / len(columns)
	for start := 0; start < len(rows); start += batchSize {
		part := rows[start:min(start+batchSize, len(rows))]
		args := make([]any, 0, len(part)*len(columns))
		values := make([]string, len(part))
		for i, row := range part {
			names := make([]string, len(row))
			for j, v := range row {
				args = append(args, v)
				names[j] = fmt.Sprintf("$%d", len(args))
			}
			values[i] = "(" + strings.Join(names, ",") + ")"
		}
		if _, err := c.ExecContext(t.Context(), "INSERT INTO "+table+"("+strings.Join(columns, ",")+") VALUES "+strings.Join(values, ","), args...); err != nil {
			t.Fatal(table, err)
		}
	}
}

func explainPlan(t *testing.T, c *ent.Client, name, query string, args ...any) string {
	t.Helper()
	prefix := "EXPLAIN QUERY PLAN "
	if name == "postgres" {
		prefix = "EXPLAIN (FORMAT JSON) "
	}
	rows, err := c.QueryContext(t.Context(), prefix+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	var details []string
	for rows.Next() {
		if name == "sqlite" {
			var id, parent, unused int
			var detail string
			if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
				break
			}
			details = append(details, detail)
		} else {
			var detail string
			if err = rows.Scan(&detail); err != nil {
				break
			}
			var compact any
			if err = json.Unmarshal([]byte(detail), &compact); err != nil {
				break
			}
			b, _ := json.Marshal(compact)
			details = append(details, string(b))
		}
	}
	if err := errors.Join(err, rows.Err(), rows.Close()); err != nil {
		t.Fatal(err)
	}
	return strings.Join(details, " | ")
}

// Candidate DDL exists only in this owned fixture. Production source indexes
// are chosen from these real plans, not from the schema's names alone.
func TestRepresentativeIndexPlans(t *testing.T) {
	for _, name := range []string{"sqlite", "postgres"} {
		t.Run(name, func(t *testing.T) {
			_, c := observedSchemaFixture(t, name)
			ctx := t.Context()
			now := time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)
			projects := make([]*ent.Project, 64)
			resources := make([]*ent.Resource, 64)
			for i := range projects {
				projects[i] = c.Project.Create().SetName(fmt.Sprintf("plan_%02d", i)).SaveX(ctx)
				resources[i] = c.Resource.Create().SetProjectID(projects[i].ID).SetPath("resource.txt").SetFormat("txt").SetStoragePath("fixture").SaveX(ctx)
			}
			blob := c.Blob.Create().SetIdentity("plan-fixture-blob").SetProjectID(projects[0].ID).SetPurpose("source").SaveX(ctx)
			var glossary, jobs, events, revisions, exports [][]any
			const count = 4096
			for i := 0; i < count; i++ {
				p := i % 64
				if i < count/2 {
					p = 0
				}
				id := i + 1
				status := "completed"
				if i%128 == 0 {
					status = "pending"
				}
				glossary = append(glossary, []any{id, now, now, projects[p].ID, fmt.Sprintf("term_%05d", i), "fixture", "fixture", i%2 == 0})
				jobs = append(jobs, []any{id, now, now, projects[p].ID, status, 1, `{"schema_version":4,"defaults_version":4,"response_format_version":2}`})
				events = append(events, []any{id, id, int64(id), "info", "info", "fixture", now})
				revisions = append(revisions, []any{id, now, now, resources[p].ID, projects[p].ID, blob.ID, "txt", "fixture", false, i%5 != 0})
				exports = append(exports, []any{id, now, now, projects[p].ID, resources[p].ID, id, "fixture", "fixture.txt"})
			}
			insertPlanRows(t, c, "glossary_entries", []string{"id", "created_at", "updated_at", "project_id", "source_key", "source", "target", "forbidden"}, glossary)
			insertPlanRows(t, c, "jobs", []string{"id", "created_at", "updated_at", "project_id", "status", "execution_plan_id", "execution_config"}, jobs)
			insertPlanRows(t, c, "sse_events", []string{"id", "job_id", "seq", "type", "level", "message", "created_at"}, events)
			insertPlanRows(t, c, "source_revisions", []string{"id", "created_at", "updated_at", "resource_id", "project_id", "source_blob_id", "format", "parser_version", "current", "deleted"}, revisions)
			insertPlanRows(t, c, "export_artifacts", []string{"id", "created_at", "updated_at", "project_id", "resource_id", "source_revision_id", "renderer_version", "filename"}, exports)
			if _, err := c.ExecContext(ctx, "ANALYZE"); err != nil {
				t.Fatal(err)
			}
			type candidate struct {
				label, ddl, query string
				args              []any
			}
			pendingFormat := "CASE WHEN COALESCE(length(CAST(execution_config AS BLOB)),0)<=67108864 AND json_valid(execution_config) THEN COALESCE((json_extract(execution_config,'$.schema_version')=4 AND json_extract(execution_config,'$.defaults_version')=4 AND json_extract(execution_config,'$.response_format_version')=2),FALSE) ELSE FALSE END"
			if name == "postgres" {
				pendingFormat = "CASE WHEN COALESCE(octet_length(CAST(execution_config AS TEXT)),0)<=67108864 THEN COALESCE((execution_config->'schema_version'='4'::jsonb AND execution_config->'defaults_version'='4'::jsonb AND execution_config->'response_format_version'='2'::jsonb),FALSE) ELSE FALSE END"
			}
			cases := []candidate{
				{"glossary_sparse", "CREATE INDEX plan_glossary_project_key_id ON glossary_entries(project_id,source_key,id)", "SELECT * FROM glossary_entries WHERE project_id=$1 ORDER BY source_key,id", []any{projects[7].ID}},
				{"glossary_dense", "", "SELECT * FROM glossary_entries WHERE project_id=$1 ORDER BY source_key,id", []any{projects[0].ID}},
				{"pending_sparse", "CREATE INDEX plan_job_status_id ON jobs(status,id)", "SELECT id FROM jobs WHERE status='pending' AND id>$1 AND " + pendingFormat + " ORDER BY id LIMIT 128", []any{0}},
				{"sse_max", "CREATE INDEX plan_sse_seq ON sse_events(seq)", "SELECT COALESCE(MAX(seq),0) FROM sse_events", nil},
				{"source_revision_page", "CREATE INDEX plan_revision_resource_deleted_id ON source_revisions(resource_id,deleted,id)", "SELECT * FROM source_revisions WHERE resource_id=$1 AND deleted=FALSE ORDER BY id DESC LIMIT 101", []any{resources[7].ID}},
				{"export_page", "CREATE INDEX plan_export_project_resource_id ON export_artifacts(project_id,resource_id,id)", "SELECT * FROM export_artifacts WHERE project_id=$1 AND resource_id=$2 ORDER BY id DESC LIMIT 101", []any{projects[7].ID, resources[7].ID}},
				{"project_jobs", "CREATE INDEX plan_job_project_updated_id ON jobs(project_id,updated_at,id)", "SELECT id,updated_at FROM jobs WHERE project_id=$1 ORDER BY updated_at DESC,id DESC LIMIT 101", []any{projects[7].ID}},
			}
			for _, test := range cases {
				t.Logf("fixture=%s case=%s generated_source_plan=%s", name, test.label, explainPlan(t, c, name, test.query, test.args...))
			}
			// Only owned fixture indexes are removed for the old A/B baseline;
			// the generated production schema has already been explained above.
			for _, indexName := range []string{"glossaryentry_project_id_source_key_id", "job_status_id", "sseevent_seq", "sourcerevision_resource_id_deleted_id", "exportartifact_project_id_resource_id_id"} {
				if _, err := c.ExecContext(ctx, "DROP INDEX IF EXISTS "+indexName); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := c.ExecContext(ctx, "ANALYZE"); err != nil {
				t.Fatal(err)
			}
			baseline := map[string]string{}
			for _, test := range cases {
				baseline[test.label] = explainPlan(t, c, name, test.query, test.args...)
			}
			for _, test := range cases {
				before := baseline[test.label]
				if test.ddl != "" {
					if _, err := c.ExecContext(ctx, test.ddl); err != nil {
						t.Fatal(err)
					}
					if _, err := c.ExecContext(ctx, "ANALYZE"); err != nil {
						t.Fatal(err)
					}
				}
				after := explainPlan(t, c, name, test.query, test.args...)
				t.Logf("fixture=%s rows=%d projects=%d case=%s baseline=%s candidate=%s", name, count, len(projects), test.label, before, after)
				if test.ddl != "" && before == after {
					t.Log("candidate did not change the planner's choice; no production index is justified solely by this case")
				}
			}
		})
	}
}
