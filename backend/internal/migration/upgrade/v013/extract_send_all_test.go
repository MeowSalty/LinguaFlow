package v013

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func TestMigrateExecutionExtractSendAllPreservesHistory(t *testing.T) {
	ctx, client, user, credentials, backends := credentialTestServices(t)
	backend := credentialTestBackend(t, ctx, backends, user, "current backend", "current-different-secret")
	old := legacyExecutionFixture(t, ctx, client, user.ID, backend.ID)
	// Change only this fixture instance: the shared six-round golden must stay
	// frozen. The reported failure had an extract round first with saved zeros.
	rounds := old.ExecutionConfig["rounds"].([]any)
	rounds[0], rounds[1] = rounds[1], rounds[0]
	extract := rounds[0].(map[string]any)["extract"].(map[string]any)
	extract["batch_size"], extract["max_words_per_batch"] = 0, 0
	client.Job.UpdateOneID(old.ID).SetExecutionConfig(old.ExecutionConfig).SetUpdatedAt(old.UpdatedAt).ExecX(ctx)

	report := runLegacyExecutionMigration(t, ctx, client, credentials.keys)
	if report.Jobs != 1 || report.Credentials != 1 {
		t.Fatalf("unexpected migration report: %+v", report)
	}
	after := client.Job.GetX(ctx, old.ID)
	if after.Status != old.Status || after.ProgressTotal != old.ProgressTotal || after.ProgressCompleted != old.ProgressCompleted || !after.CreatedAt.Equal(old.CreatedAt) || !after.UpdatedAt.Equal(old.UpdatedAt) {
		t.Fatal("migration changed historical job state")
	}
	snapshot, err := service.GetSnapshot(after)
	if err != nil {
		t.Fatal(err)
	}
	first := snapshot.Rounds[0]
	if first.Mode != "extract" || first.Extract == nil || first.Extract.BatchSize != 0 || first.Extract.MaxWordsPerBatch != 0 || first.Extract.TemplateContent != "saved extract prompt" {
		t.Fatal("migration changed extract send-all parameters or template")
	}
	for _, binding := range snapshot.Bindings() {
		secret, err := credentials.Resolve(ctx, binding, "openai", "https://api.openai.com/v1")
		if err != nil || secret != "snapshot-original-secret" || binding.ID == backend.Credential.ID {
			t.Fatalf("migration failed to preserve the historical credential: %v", err)
		}
	}
	raw, err := json.Marshal(after.ExecutionConfig)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("api_key")) || bytes.Contains(raw, []byte("snapshot-original-secret")) {
		t.Fatal("plaintext credential remained in the migrated snapshot")
	}
}
