package v013

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func legacyExecutionFixture(t *testing.T, ctx context.Context, client *ent.Client, ownerID, backendID int) *ent.Job {
	t.Helper()
	// This represents v0.13.0's saved JSON, including omitted selections and
	// runtime-only builtin templates, without deriving it from today's resolver.
	raw := `{
		"execution_plan_id":1,"execution_plan_name":"legacy plan",
		"source_lang":"ja","target_lang":"en",
		"strategy":{"protect":{"enabled":true,"rules":["code"]},
			"postprocess":{"enabled":false,"trim_spaces":false},
			"repair":{"enabled":true,"prompt_upgrade":true},
			"glossary":{"bootstrap":{"enabled":true,"max_terms_per_1000_chars":3,"min_source_len":2,"inline_conflict_strategy":"rewrite-local"}},
			"context":{"enabled":true,"before":1,"after":1,"max_chars":0},
			"ruby":{"enabled":true,"preserve_kinds":["phonetic","creative"]},
			"qa":{"enabled":true,"auto_reject":false,"length_ratio_min":0,"length_ratio_max":0}},
		"rounds":[
			{"mode":"translate","translate":{"prompt":{"template_name":"custom","content":"saved user prompt"},"batch_size":10,"concurrency":1,"fallback_shrink":1,"segment_filter":{"status_filter":"all"},"retry":{"max_attempts":0,"backoff_ms":0,"jitter":false}}},
			{"mode":"extract","extract":{"template_content":"saved extract prompt","batch_size":10,"concurrency":1}},
			{"mode":"adjudicate","adjudicate":{"batch_size":10,"concurrency":1}},
			{"mode":"semantic_qa","semantic_qa":{"batch_size":10,"concurrency":1}},
			{"mode":"revise","revise":{"batch_size":10,"concurrency":1}},
			{"mode":"correct","correct":{"concurrency":1,"rules":[]}}
		],"ruby_retry":{"enabled":true,"max_attempts":0}
	}`
	var config map[string]any
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		t.Fatal(err)
	}
	backend := func() map[string]any {
		return map[string]any{"id": backendID, "scope": "user", "name": "historical backend", "type": "openai", "rate_limit_per_minute": 42,
			"options": map[string]any{"api_key": "snapshot-original-secret", "model": "original-model", "temperature": 0.0}}
	}
	for _, item := range config["rounds"].([]any) {
		round := item.(map[string]any)
		if round["mode"] != "correct" {
			round["backend"] = backend()
		}
	}
	config["ruby_retry"].(map[string]any)["backend"] = backend()
	project := client.Project.Create().SetName("legacy project").SetOwnerUserID(ownerID).SaveX(ctx)
	return client.Job.Create().SetProjectID(project.ID).SetExecutionPlanID(1).SetExecutionConfig(config).
		SetStatus(service.JobStatusPaused).SetProgressTotal(40).SetProgressCompleted(17).
		SetCreatedAt(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)).
		SetUpdatedAt(time.Date(2025, 1, 2, 0, 0, 0, 0, time.UTC)).SaveX(ctx)
}

func runLegacyExecutionMigration(t *testing.T, ctx context.Context, client *ent.Client, keys *credential.Keyring) executionReport {
	t.Helper()
	tx, err := client.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	report, err := migrateExecution(ctx, tx.Client(), keys)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return report
}

func TestMigrateExecutionPreservesSnapshotsAndCredentials(t *testing.T) {
	ctx, client, user, credentials, backends := credentialTestServices(t)
	backend := credentialTestBackend(t, ctx, backends, user, "current backend", "current-different-secret")
	job := legacyExecutionFixture(t, ctx, client, user.ID, backend.ID)
	profile := sourceExecutionProfileConfigData{}
	profile.Glossary.Bootstrap.Enabled = true
	profile.Glossary.Bootstrap.InlineConflictStrategy = "rewrite-local"
	storedProfile := legacyTestProfile(t, ctx, client, user.ID, profile)
	report := runLegacyExecutionMigration(t, ctx, client, credentials.keys)
	if report.Profiles != 1 || report.Jobs != 1 || report.Credentials != 1 {
		t.Fatalf("unexpected migration counts: %+v", report)
	}
	after := client.Job.GetX(ctx, job.ID)
	if after.Status != service.JobStatusPaused || after.ProgressTotal != 40 || after.ProgressCompleted != 17 || !after.UpdatedAt.Equal(job.UpdatedAt) || !after.CreatedAt.Equal(job.CreatedAt) {
		t.Fatal("migration changed job status, checkpoint, or original timestamps")
	}
	spec, err := service.GetSnapshot(after)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Rounds[0].Translate.Prompt.Content != "saved user prompt" || spec.Rounds[1].Extract.TemplateContent != "saved extract prompt" {
		t.Fatal("migration replaced historical custom prompts")
	}
	if spec.Rounds[2].Adjudicate.TemplateContent == "" || spec.Rounds[3].SemanticQA.TemplateContent == "" || spec.Rounds[4].Revise.TemplateContent == "" || spec.RubyTemplates.JSON == "" || spec.RetryReminderTemplate == "" {
		t.Fatal("migration did not freeze legacy runtime templates")
	}
	if spec.Strategy.QA.LengthRatioMin != 0 || spec.Strategy.QA.LengthRatioMax != 0 || spec.Strategy.Postprocess.Enabled || spec.RubyRetry.MaxAttempts != 1 {
		t.Fatal("migration replaced saved zero or boolean settings with current defaults")
	}
	if spec.Rounds[0].Translate.InlineTermExtraction != nil {
		t.Fatal("legacy profile bootstrap was inherited by the translation round")
	}
	for _, binding := range spec.Bindings() {
		got, err := credentials.Resolve(ctx, binding, "openai", "https://api.openai.com/v1")
		if err != nil || got != "snapshot-original-secret" {
			t.Fatalf("historical secret not preserved: %v", err)
		}
		if binding.ID == backend.Credential.ID {
			t.Fatal("migration rebound historical snapshot to a different current secret")
		}
	}
	if client.CredentialJobReference.Query().CountX(ctx) != 1 {
		t.Fatal("migration failed to retain deduplicated snapshot credential")
	}
	raw, err := json.Marshal(after.ExecutionConfig)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("api_key")) || bytes.Contains(raw, []byte("snapshot-original-secret")) {
		t.Fatal("plaintext survived in migrated snapshot")
	}
	if bytes.Contains(raw, []byte(`"glossary":`)) {
		t.Fatal("legacy glossary strategy survived in migrated snapshot")
	}
	cfg := client.ExecutionProfile.GetX(ctx, storedProfile.ID).Config
	if cfg.SchemaVersion != 1 || cfg.Ruby.Enabled || len(cfg.Ruby.PreserveKinds) != 3 || !cfg.Context.Enabled || cfg.Context.Before != 1 {
		t.Fatal("legacy profile semantics were not preserved")
	}
	profileRaw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(profileRaw, []byte(`"glossary":`)) {
		t.Fatal("legacy glossary strategy survived in migrated profile")
	}
	tx, err := client.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := migrateExecution(ctx, tx.Client(), credentials.keys); err == nil {
		t.Fatal("mixed or already migrated execution state was accepted")
	}

}

func TestMigrateExecutionFailureRollsBack(t *testing.T) {
	ctx, client, user, credentials, backends := credentialTestServices(t)
	backend := credentialTestBackend(t, ctx, backends, user, "backend", "current-secret")
	first := legacyExecutionFixture(t, ctx, client, user.ID, backend.ID)
	invalid := legacyExecutionFixture(t, ctx, client, user.ID, backend.ID)
	config := invalid.ExecutionConfig
	config["rounds"].([]any)[0].(map[string]any)["backend"].(map[string]any)["options"].(map[string]any)["api_key"] = ""
	client.Job.UpdateOneID(invalid.ID).SetExecutionConfig(config).ExecX(ctx)
	tx, err := client.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = migrateExecution(ctx, tx.Client(), credentials.keys)
	if err == nil || !strings.Contains(err.Error(), "no API key") || strings.Contains(err.Error(), "snapshot-original-secret") {
		t.Fatalf("expected actionable secret-safe error: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	after := client.Job.GetX(ctx, first.ID)
	if _, exists := after.ExecutionConfig["schema_version"]; exists || client.Credential.Query().CountX(ctx) != 1 || client.CredentialJobReference.Query().CountX(ctx) != 0 {
		t.Fatal("failed migration left partial execution or credential changes")
	}
}

func TestMigrateExecutionPreservesDeletedBackendHistory(t *testing.T) {
	ctx, client, user, credentials, _ := credentialTestServices(t)
	job := legacyExecutionFixture(t, ctx, client, user.ID, 987654)
	client.Job.UpdateOneID(job.ID).SetStatus(service.JobStatusCompleted).ExecX(ctx)
	runLegacyExecutionMigration(t, ctx, client, credentials.keys)
	after := client.Job.GetX(ctx, job.ID)
	spec, err := service.GetSnapshot(after)
	if err != nil || after.Status != service.JobStatusCompleted {
		t.Fatalf("deleted backend history became unreadable: %v", err)
	}
	if spec.Rounds[0].Backend.ID != 987654 || client.Backend.Query().CountX(ctx) != 0 {
		t.Fatal("migration resurrected a deleted backend")
	}
}

func TestMigrateExecutionRejectsChangedOwnershipScope(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		name := "existing_backend"
		if deleted {
			name = "deleted_backend"
		}
		t.Run(name, func(t *testing.T) {
			ctx, client, user, credentials, backends := credentialTestServices(t)
			backend := credentialTestBackend(t, ctx, backends, user, "backend", "current-secret")
			backendID := backend.ID
			if deleted {
				backendID = 987654
			}
			job := legacyExecutionFixture(t, ctx, client, user.ID, backendID)
			// A personal project can historically use an organization backend.
			// Neither an extant personal backend nor the personal project proves
			// ownership of a saved organization credential.
			job.ExecutionConfig["rounds"].([]any)[0].(map[string]any)["backend"].(map[string]any)["scope"] = service.ScopeOrg
			client.Job.UpdateOneID(job.ID).SetExecutionConfig(job.ExecutionConfig).ExecX(ctx)
			tx, err := client.Tx(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			_, err = migrateExecution(ctx, tx.Client(), credentials.keys)
			if err == nil || !strings.Contains(err.Error(), "snapshot scope differs") {
				t.Fatalf("expected ownership scope mismatch: %v", err)
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if client.Credential.Query().CountX(ctx) != 1 {
				t.Fatal("scope mismatch published a credential to a different owner")
			}
		})
	}
}

func TestMigrateExecutionRejectsPartialCurrentFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"backend_credential_id", func(cfg map[string]any) {
			cfg["rounds"].([]any)[0].(map[string]any)["backend"].(map[string]any)["credential"] = map[string]any{"id": 1}
		}},
		{"backend_credential_version", func(cfg map[string]any) {
			cfg["rounds"].([]any)[0].(map[string]any)["backend"].(map[string]any)["credential"] = map[string]any{"version": 1}
		}},
		{"correct_credential_id", func(cfg map[string]any) {
			cfg["rounds"].([]any)[5].(map[string]any)["backend"] = map[string]any{"credential": map[string]any{"id": 1}}
		}},
		{"correct_credential_version", func(cfg map[string]any) {
			cfg["rounds"].([]any)[5].(map[string]any)["backend"] = map[string]any{"credential": map[string]any{"version": 1}}
		}},
		{"disabled_ruby_credential_id", func(cfg map[string]any) {
			cfg["ruby_retry"] = map[string]any{"enabled": false, "backend": map[string]any{"credential": map[string]any{"id": 1}}}
		}},
		{"disabled_ruby_credential_version", func(cfg map[string]any) {
			cfg["ruby_retry"] = map[string]any{"enabled": false, "backend": map[string]any{"credential": map[string]any{"version": 1}}}
		}},
		{"disabled_ruby_plaintext", func(cfg map[string]any) {
			cfg["ruby_retry"] = map[string]any{"enabled": false, "backend": map[string]any{"options": map[string]any{"api_key": "must-not-survive"}}}
		}},
		{"sources_would_be_replaced", func(cfg map[string]any) {
			cfg["sources"] = []any{map[string]any{"kind": "round", "id": "0", "digest": "already-frozen"}}
		}},
		{"template_would_be_replaced", func(cfg map[string]any) {
			cfg["rounds"].([]any)[2].(map[string]any)["adjudicate"].(map[string]any)["template_content"] = "already frozen prompt"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, client, user, credentials, backends := credentialTestServices(t)
			backend := credentialTestBackend(t, ctx, backends, user, "backend", "current-secret")
			job := legacyExecutionFixture(t, ctx, client, user.ID, backend.ID)
			test.mutate(job.ExecutionConfig)
			client.Job.UpdateOneID(job.ID).SetExecutionConfig(job.ExecutionConfig).ExecX(ctx)
			tx, err := client.Tx(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			_, err = migrateExecution(ctx, tx.Client(), credentials.keys)
			if err == nil {
				t.Fatal("ambiguous partial migration was accepted")
			}
			if strings.Contains(err.Error(), "must-not-survive") || strings.Contains(err.Error(), "snapshot-original-secret") {
				t.Fatal("migration error disclosed a secret")
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			after := client.Job.GetX(ctx, job.ID)
			if _, exists := after.ExecutionConfig["schema_version"]; exists || client.Credential.Query().CountX(ctx) != 1 || client.CredentialJobReference.Query().CountX(ctx) != 0 {
				t.Fatal("refused partial format changed persistent execution state")
			}
		})
	}
}
