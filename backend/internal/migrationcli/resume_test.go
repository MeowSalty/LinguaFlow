package migrationcli

import (
	"context"
	"database/sql"
	"reflect"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
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

func assertMigratedJobReadableAndResumable(t *testing.T, client *ent.Client, keys *credential.Keyring) {
	t.Helper()
	ctx := context.Background()
	users := service.NewUserService(client, nil)
	credentials := service.NewCredentialService(client, keys, users)
	backends := service.NewBackendService(client, users, nil)
	backends.SetCredentials(credentials)
	jobs := service.NewJobService(client, service.NewProjectService(client, users), nil, backends, nil, nil, nil, nil, nil)
	current, err := jobs.GetJob(ctx, 1, 1)
	if err != nil {
		t.Fatalf("migrated history is unreadable through current JobService: %v", err)
	}
	if current.Status != service.JobStatusPaused || current.Edges.CreatedBy == nil || current.Edges.CreatedBy.ID != 1 {
		t.Fatal("migration changed saved creator or paused state")
	}
	if current.ProgressTotal != 2 || current.ProgressCompleted != 1 || len(current.Edges.JobResources) != 1 {
		t.Fatal("migrated historical progress or resource associations changed")
	}
	if _, err := jobs.GetJob(ctx, 999, 1); err == nil {
		t.Fatal("unrelated actor gained access to migrated history")
	}
	if _, err := backends.GetByID(ctx, 1); err != nil {
		t.Fatalf("migrated backend unreadable: %v", err)
	}
	snapshot, err := jobs.GetExecutionSnapshot(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Rounds[0].Translate.Prompt.Content != "old custom prompt" {
		t.Fatal("custom historical prompt changed")
	}
	backendBinding, release, err := credentials.AcquireBackend(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	const endpoint = "https://api.openai.com/v1"
	if value, err := credentials.Resolve(ctx, backendBinding, "openai", endpoint); err != nil || value != "backend-original-key" {
		t.Fatalf("current backend credential lost: %v", err)
	}
	historicalBinding := snapshot.Rounds[0].Backend.Credential
	if historicalBinding == backendBinding {
		t.Fatal("historical key was replaced by current backend credential")
	}
	if value, err := credentials.Resolve(ctx, historicalBinding, "openai", endpoint); err != nil || value != "job-original-key" {
		t.Fatalf("historical job credential lost: %v", err)
	}
	resumed, err := jobs.ResumeJob(ctx, 1, 1)
	if err != nil {
		t.Fatalf("migrated job rejected by current ResumeJob policies: %v", err)
	}
	if resumed.Status != service.JobStatusPending || !reflect.DeepEqual(current.ExecutionConfig, resumed.ExecutionConfig) {
		t.Fatal("resume did not queue the job with its unchanged frozen snapshot")
	}
	if resumed.ProgressTotal != 2 || resumed.ProgressCompleted != 1 || len(resumed.Edges.JobResources) != 1 {
		t.Fatal("resume lost saved progress or resource associations")
	}
	resource := resumed.Edges.JobResources[0]
	if resource.Status != service.JobResourceStatusPending || len(resource.Edges.Rounds) != 1 || resource.Edges.Rounds[0].Status != service.JobRoundStatusPending || resource.Edges.Rounds[0].SegmentCompleted != 1 {
		t.Fatal("resume did not preserve and requeue the historical round checkpoint")
	}
	if count, err := client.JobRoundSegment.Query().Count(ctx); err != nil || count != 1 {
		t.Fatalf("checkpoint binding lost: %v", err)
	}
	if value, err := credentials.Resolve(ctx, historicalBinding, "openai", endpoint); err != nil || value != "job-original-key" {
		t.Fatalf("resume changed historical credential: %v", err)
	}
}
