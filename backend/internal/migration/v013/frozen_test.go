package v013

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

// The digest covers the full persisted v1 snapshot: all six rounds, Ruby retry,
// exact frozen templates, nil/empty selections, options and provenance. It is
// intentionally a literal rather than invoking the application's resolver.
func TestFrozenExecutionGolden(t *testing.T) {
	ctx, client, owner, credentials, backends := credentialTestServices(t)
	backend := credentialTestBackend(t, ctx, backends, owner, "current", "different-current-secret")
	job := legacyExecutionFixture(t, ctx, client, owner.ID, backend.ID)
	runLegacyExecutionMigration(t, ctx, client, credentials.keys)
	raw, err := json.Marshal(client.Job.GetX(ctx, job.ID).ExecutionConfig)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	const expected = "15e71b335bc33192776ad6534e420fed7b23a70aa726c8e61e41f7212d60fa33"
	if hex.EncodeToString(digest[:]) != expected {
		t.Fatalf("v0.13.0 to execution v1 golden changed: got %x; review compatibility rather than adopting current defaults", digest)
	}
}

func TestFrozenSourceAssets(t *testing.T) {
	raw, err := os.ReadFile("testdata/source-assets.json")
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string]string
	if err := json.Unmarshal(raw, &expected); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"adjudication": adjudicationTemplate, "semantic_qa": semanticQATemplate, "revise": reviseTemplate, "ruby_json": rubyJSONTemplate, "ruby_text": rubyTextTemplate, "retry_reminder": retryReminderTemplate} {
		digest := sha256.Sum256([]byte(sourceTemplate(body)))
		if hex.EncodeToString(digest[:]) != expected[name] {
			t.Errorf("%s no longer matches the frozen source release", name)
		}
	}
}

func TestFrozenBackendDefaults(t *testing.T) {
	for _, tc := range []struct {
		provider, endpoint string
		maxTokens          int
	}{
		{"openai", "https://api.openai.com/v1/", 0},
		{"anthropic", "https://api.anthropic.com/", 8192},
		{"google", "https://generativelanguage.googleapis.com/", 8192},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			input := map[string]any{"model": "old-model", "temperature": 0.0}
			out, err := freezeBackendOptions(tc.provider, input)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]any{"model": "old-model", "temperature": 0.0, "max_tokens": tc.maxTokens, "timeout": 60, "response_format": "json_schema", "stream": false, "base_url": tc.endpoint}
			if tc.provider == "anthropic" {
				want["enable_prompt_cache"] = true
			}
			if !reflect.DeepEqual(out, want) {
				t.Fatalf("source defaults changed: %+v", out)
			}
			if len(input) != 2 {
				t.Fatal("freezing changed the source options")
			}
		})
	}
	out, err := freezeBackendOptions("anthropic", map[string]any{"model": "old-model", "max_tokens": 2048.0, "thinking_level": "minimal", "enable_prompt_cache": false, "timeout": 0.0, "response_format": ""})
	if err != nil || out["thinking_budget_tokens"] != int64(1024) || out["enable_prompt_cache"] != false || out["timeout"] != 0.0 || out["response_format"] != "json_schema" {
		t.Fatalf("saved values or source defaults changed: %+v %v", out, err)
	}
	if _, err := freezeBackendOptions("openai", map[string]any{"model": "old-model", "timeout": "30s"}); err == nil {
		t.Fatal("a duration string acquired semantics absent from v0.13.0")
	}
}

func TestFrozenSelectionsPreserveSourceSemantics(t *testing.T) {
	ctx, client, owner, credentials, backends := credentialTestServices(t)
	backend := credentialTestBackend(t, ctx, backends, owner, "current", "current-secret")
	job := legacyExecutionFixture(t, ctx, client, owner.ID, backend.ID)
	job.ExecutionConfig["strategy"].(map[string]any)["qa"].(map[string]any)["checks"] = []any{}
	rounds := job.ExecutionConfig["rounds"].([]any)
	rounds[2].(map[string]any)["adjudicate"].(map[string]any)["adjudicate_codes"] = []any{}
	rounds[3].(map[string]any)["semantic_qa"].(map[string]any)["issue_codes"] = []any{}
	rounds[4].(map[string]any)["revise"].(map[string]any)["issue_codes"] = []any{}
	client.Job.UpdateOneID(job.ID).SetExecutionConfig(job.ExecutionConfig).ExecX(ctx)
	runLegacyExecutionMigration(t, ctx, client, credentials.keys)
	snapshot, err := service.GetSnapshot(client.Job.GetX(ctx, job.ID))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Strategy.QA.Checks == nil || len(snapshot.Strategy.QA.Checks) != 0 {
		t.Fatal("explicit empty QA checks were replaced")
	}
	// The old adjudication/revision handlers used len==0 as their fallback;
	// current handlers deliberately differ, so freeze the old meaning now.
	if !reflect.DeepEqual(snapshot.Rounds[2].Adjudicate.AdjudicateCodes, []string{"source_residual", "punctuation_surplus"}) || !reflect.DeepEqual(snapshot.Rounds[4].Revise.IssueCodes, semanticCodes()) {
		t.Fatal("old handler fallback was not frozen")
	}
	if snapshot.Rounds[3].SemanticQA.IssueCodes == nil || len(snapshot.Rounds[3].SemanticQA.IssueCodes) != 0 {
		t.Fatal("empty semantic QA selection was replaced")
	}
}

func TestFrozenProfilesRejectMixedOrUnknownSourceFields(t *testing.T) {
	for _, raw := range []string{`{"schema_version":1}`, `{"schema_version":0}`, `{"unknown":"private"}`, `{"ruby":{"new_field":true}}`, `null`, `{} {}`} {
		if _, err := convertProfile([]byte(raw)); err == nil || strings.Contains(err.Error(), "private") {
			t.Fatalf("invalid source profile was accepted or disclosed: %s", raw)
		}
	}
	cfg, err := convertProfile([]byte(`{"ruby":{"preserve_kinds":[]},"qa":{"checks":[]},"context":{"enabled":false,"max_chars":100}}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Ruby.PreserveKinds == nil || len(cfg.Ruby.PreserveKinds) != 0 || cfg.QA.Checks == nil || cfg.Context.Enabled || cfg.Context.Before != 1 || cfg.Context.After != 1 {
		t.Fatal("explicit selections or old context defaults changed")
	}
}

func TestLegacyLocalIdentity(t *testing.T) {
	for _, scenario := range []string{"valid", "missing", "inactive", "ordinary"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			client := testClient(t)
			legacyMigrationAdmin(t, ctx, client)
			var localID int
			if scenario != "missing" {
				role := "admin"
				if scenario == "ordinary" {
					role = "user"
				}
				row := client.User.Create().SetUsername("local").SetEmail("preserve-custom@example.test").SetPasswordHash("preserved-local-hash").SetRole(role).SetActive(scenario != "inactive").SaveX(ctx)
				localID = row.ID
			}
			tx, err := client.Tx(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			_, err = Convert(ctx, tx.Client(), credentialTestKeyring(t, "one", "one"), "local")
			if scenario != "valid" {
				if err == nil {
					t.Fatal("invalid local identity was accepted")
				}
				if err := tx.Rollback(); err != nil {
					t.Fatal(err)
				}
				if client.InstanceInitialization.Query().ExistX(ctx) {
					t.Fatal("failed local conversion published a marker")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			local, err := service.NewInitializationService(client).Validate(ctx, "local")
			if err != nil || local.ID != localID || local.PasswordHash != "preserved-local-hash" || local.Email != "preserve-custom@example.test" {
				t.Fatalf("local identity changed: %v", err)
			}
		})
	}
}

func TestFrozenTargetCompatibility(t *testing.T) {
	if err := checkTargetCompatibility(); err != nil {
		t.Fatal(err)
	}
	if targetSchemaVersion != 1 || targetDefaultsVersion != 1 || targetInitializationVersion != 1 || targetCredentialVersion != 1 || targetSQLiteVersion != 1 || execution.SchemaVersion != 1 {
		t.Fatal("explicit compatibility review required")
	}
}
