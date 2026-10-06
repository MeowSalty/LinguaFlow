package v013

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
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

func TestSQLiteExtractSendAllRehearsalAndApply(t *testing.T) {
	for _, mode := range []string{config.ModeLocal, config.ModeServer} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			var dir string
			if mode == config.ModeServer {
				dir = sqliteServeFixture(t)
			} else {
				dir = localFixture(t, true)
			}
			options := SQLiteOptions{DataDir: dir, Mode: mode}
			if mode == config.ModeServer {
				options = sqliteServeOptions(t, dir)
			}
			db := openLocalFixture(t, dir)
			var oldJSON string
			if err := db.QueryRow("SELECT execution_config FROM jobs WHERE id=1").Scan(&oldJSON); err != nil {
				t.Fatal(err)
			}
			var old map[string]any
			if err := json.Unmarshal([]byte(oldJSON), &old); err != nil {
				t.Fatal(err)
			}
			rounds := old["rounds"].([]any)
			extract := map[string]any{
				"mode": "extract", "backend": rounds[0].(map[string]any)["backend"],
				"extract": map[string]any{
					"template_content": "saved extract prompt", "batch_size": 0,
					"max_words_per_batch": 0, "concurrency": 1,
				},
			}
			old["rounds"] = append([]any{extract}, rounds...)
			raw, err := json.Marshal(old)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("UPDATE jobs SET execution_config=? WHERE id=1", string(raw)); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			originalDB := readLocalBytes(t, filepath.Join(dir, localDatabaseName))
			originalResource := readLocalBytes(t, filepath.Join(dir, "jobs", "resources", "test.txt"))
			rehearsal, err := RunSQLite(ctx, options)
			if err != nil || rehearsal.Applied || rehearsal.Jobs != 1 {
				t.Fatalf("rehearsal: %+v %v", rehearsal, err)
			}
			if !bytes.Equal(originalDB, readLocalBytes(t, filepath.Join(dir, localDatabaseName))) || !bytes.Equal(originalResource, readLocalBytes(t, filepath.Join(dir, "jobs", "resources", "test.txt"))) {
				t.Fatal("rehearsal changed original data")
			}
			for _, pattern := range []string{filepath.Join(filepath.Dir(dir), ".LinguaFlow.v013-stage-*"), dir + ".v013-backup-*"} {
				matches, err := filepath.Glob(pattern)
				if err != nil || len(matches) != 0 {
					t.Fatalf("rehearsal left staging or backup: %v %v", matches, err)
				}
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(dir), ".LinguaFlow.v013-switch.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rehearsal published a switch journal: %v", err)
			}
			options.Apply = true
			applied, err := RunSQLite(ctx, options)
			if err != nil || !applied.Applied || applied.BackupDir == "" {
				t.Fatalf("apply: %+v %v", applied, err)
			}
			if !bytes.Equal(originalDB, readLocalBytes(t, filepath.Join(applied.BackupDir, localDatabaseName))) {
				t.Fatal("backup did not preserve original database")
			}
			for _, root := range []string{dir, applied.BackupDir} {
				if !bytes.Equal(originalResource, readLocalBytes(t, filepath.Join(root, "jobs", "resources", "test.txt"))) {
					t.Fatal("resource content changed")
				}
			}
			cfg := config.DefaultServerConfig()
			cfg.DataDir, cfg.AutoMigrate = dir, false
			_, client, err := database.Open(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			job := client.Job.GetX(ctx, 1)
			originalTime, err := parseSQLiteLegacyTime(localOldTimestamp)
			if err != nil {
				t.Fatal(err)
			}
			if job.Status != "paused" || job.ProgressTotal != 40 || job.ProgressCompleted != 17 || !job.CreatedAt.Equal(originalTime) || !job.UpdatedAt.Equal(originalTime) {
				t.Fatal("migration changed historical state or timestamps")
			}
			snapshot, err := service.GetSnapshot(job)
			if err != nil {
				t.Fatal(err)
			}
			first := snapshot.Rounds[0]
			if first.Mode != "extract" || first.Extract == nil || first.Extract.BatchSize != 0 || first.Extract.MaxWordsPerBatch != 0 || first.Extract.TemplateContent != "saved extract prompt" {
				t.Fatal("published snapshot lost extract send-all semantics")
			}
			keyringPath := options.KeyringFile
			if mode == config.ModeLocal {
				keyringPath = filepath.Join(dir, "credentials-keyring.json")
			}
			keys, err := credential.LoadKeyring(keyringPath)
			if err != nil {
				t.Fatal(err)
			}
			credentials := service.NewCredentialService(client, keys, nil)
			if err := credentials.ValidateKeys(ctx); err != nil {
				t.Fatal(err)
			}
			secret, err := credentials.Resolve(ctx, first.Backend.Credential, "openai", "https://api.openai.com/v1")
			if err != nil || secret != "job-original-key" {
				t.Fatalf("published historical credential changed: %v", err)
			}
			if err := client.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
