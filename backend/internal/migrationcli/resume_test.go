package migrationcli

import (
	"database/sql"
	"testing"
)

const oldRunnableSnapshot = `{"execution_plan_id":1,"execution_plan_name":"old-plan","source_lang":"ja","target_lang":"en","strategy":{"glossary":{"bootstrap":{"enabled":false,"inline_conflict_strategy":"rewrite-local"}},"context":{"enabled":true,"before":1,"after":1}},"rounds":[{"mode":"translate","backend":{"id":1,"scope":"user","name":"old-backend","type":"openai","options":{"model":"old-model","api_key":"job-original-key"}},"translate":{"prompt":{"template_name":"saved","content":"old custom prompt"},"batch_size":10,"concurrency":1,"fallback_shrink":1,"segment_filter":{"status_filter":"all"},"retry":{"max_attempts":0,"backoff_ms":0,"jitter":false}}}]}`

// Both source fixtures contain a real paused job with a saved creator and a
// completed segment checkpoint. Only the SQLite fixture needs the resource
// file, since the PostgreSQL importer leaves the deployment filestore in place.
func seedLegacyRunnableJob(t *testing.T, db *sql.DB, postgres bool) {
	t.Helper()
	timestamp := "2026-09-29 11:00:00.123456789 +0800 CST"
	if postgres {
		timestamp = "2026-09-29T11:00:00.123456+08:00"
	}
	statements := []string{
		`INSERT INTO resources(id,created_at,updated_at,path,format,storage_path,total_segments,project_id) VALUES(1,$1,$1,'migration.txt','txt','resources/migration.txt',2,1)`,
		`INSERT INTO segments(id,created_at,updated_at,segment_index,source_text,target_text,status,resource_id) VALUES(1,$1,$1,0,'old source','saved target','translated',1),(2,$1,$1,1,'remaining source',NULL,'pending',1)`,
		`INSERT INTO jobs(id,created_at,updated_at,project_id,user_created_jobs,execution_plan_id,status,resource_count,progress_total,progress_completed,execution_config) VALUES(1,$1,$1,1,1,1,'paused',1,2,1,$2)`,
		`INSERT INTO job_resources(id,created_at,updated_at,status,segment_ids,segment_count,completed_segments,work_weight,job_job_resources,resource_job_resources) VALUES(1,$1,$1,'running','[1,2]',2,1,2,1,1)`,
		`INSERT INTO job_rounds(id,created_at,updated_at,round_index,mode,status,segment_total,segment_completed,job_id,job_resource_id) VALUES(1,$1,$1,0,'translate','running',2,1,1,1)`,
	}
	for index, statement := range statements {
		args := []any{timestamp}
		if index == 2 {
			args = append(args, oldRunnableSnapshot)
		}
		if _, err := db.Exec(statement, args...); err != nil {
			t.Fatalf("seed resumable job statement %d: %v", index, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO job_round_segments(id,job_round_id,segment_id) VALUES(1,1,1)`); err != nil {
		t.Fatal(err)
	}
}
