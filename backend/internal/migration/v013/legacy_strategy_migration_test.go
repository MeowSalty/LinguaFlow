package v013

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func legacyRoundStrategyResidue() map[string]any {
	return map[string]any{
		"context":                map[string]any{"enabled": true, "before": 99, "after": 88, "max_chars": 999},
		"protect":                map[string]any{"enabled": true, "rules": []any{"ignored-rule"}},
		"ruby":                   map[string]any{"enabled": true, "preserve_kinds": []any{"ignored-kind"}},
		"glossary":               map[string]any{"bootstrap": map[string]any{"enabled": true}},
		"schema_version":         999,
		"api_key":                "ignored-round-strategy-secret",
		"private-residue-marker": []any{nil, true, "ignored-round-strategy-value"},
	}
}

func legacyStrategyFiles(t *testing.T, root string) map[string][sha256.Size]byte {
	t.Helper()
	files := make(map[string][sha256.Size]byte)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[relative] = sha256.Sum256(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func legacyStrategyJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestLegacyRoundStrategySQLiteRehearsalAndApply(t *testing.T) {
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
			// A missing top-level strategy had zero-value runtime semantics. A
			// second task without the ignored residue provides the full-output
			// baseline, including every provenance digest and credential binding.
			delete(old, "strategy")
			baseline := legacyStrategyJSON(t, old)
			if _, err := db.Exec(`INSERT INTO jobs(id,created_at,updated_at,project_id,execution_plan_id,status,progress_total,progress_completed,execution_config)
				SELECT 2,created_at,updated_at,project_id,execution_plan_id,status,progress_total,progress_completed,? FROM jobs WHERE id=1`, string(baseline)); err != nil {
				t.Fatal(err)
			}
			old["rounds"].([]any)[0].(map[string]any)["translate"].(map[string]any)["strategy"] = legacyRoundStrategyResidue()
			if _, err := db.Exec("UPDATE jobs SET execution_config=? WHERE id=1", string(legacyStrategyJSON(t, old))); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			original := legacyStrategyFiles(t, dir)
			parent := filepath.Dir(dir)
			retainedStage := filepath.Join(parent, ".LinguaFlow.v013-stage-ffffffffffffffffffffffff")
			if err := os.Mkdir(retainedStage, 0700); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(retainedStage, "preserve.txt")
			if err := os.WriteFile(marker, []byte("unrelated previous staging"), 0600); err != nil {
				t.Fatal(err)
			}
			assertStages := func() {
				t.Helper()
				stages, err := filepath.Glob(filepath.Join(parent, ".LinguaFlow.v013-stage-*"))
				if err != nil || !reflect.DeepEqual(stages, []string{retainedStage}) {
					t.Fatalf("unexpected staging directories: %v %v", stages, err)
				}
				if string(readLocalBytes(t, marker)) != "unrelated previous staging" {
					t.Fatal("migration changed unrelated staging")
				}
			}
			rehearsal, err := RunSQLite(ctx, options)
			if err != nil || rehearsal.Applied || rehearsal.Jobs != 2 || rehearsal.CredentialsCreated != 2 {
				t.Fatalf("rehearsal: %+v %v", rehearsal, err)
			}
			if !reflect.DeepEqual(original, legacyStrategyFiles(t, dir)) {
				t.Fatal("rehearsal changed original files")
			}
			assertStages()
			if backups, err := filepath.Glob(dir + ".v013-backup-*"); err != nil || len(backups) != 0 {
				t.Fatalf("rehearsal published a backup: %v %v", backups, err)
			}
			if _, err := os.Stat(filepath.Join(parent, ".LinguaFlow.v013-switch.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rehearsal published a switch journal: %v", err)
			}
			if mode == config.ModeServer {
				if _, err := os.Stat(options.KeyringFile); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("rehearsal published an external keyring: %v", err)
				}
			}
			options.Apply = true
			applied, err := RunSQLite(ctx, options)
			if err != nil || !applied.Applied || applied.BackupDir == "" {
				t.Fatalf("apply: %+v %v", applied, err)
			}
			if !reflect.DeepEqual(original, legacyStrategyFiles(t, applied.BackupDir)) {
				t.Fatal("backup did not preserve all original files")
			}
			for relative, digest := range original {
				if relative != localDatabaseName && sha256.Sum256(readLocalBytes(t, filepath.Join(dir, relative))) != digest {
					t.Fatalf("migration changed original resource %s", relative)
				}
			}
			assertStages()
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
			if snapshot.Rounds[0].Translate.Prompt.Content != "old custom prompt" || snapshot.SourceLang != "ja" || snapshot.TargetLang != "en" {
				t.Fatal("migration changed the saved prompt or languages")
			}
			strategy := snapshot.Strategy
			if strategy.QA.LengthMethod != "char_weight" || len(strategy.QA.Checks) == 0 {
				t.Fatal("required frozen QA metadata missing")
			}
			strategy.QA.Checks, strategy.QA.LengthMethod = nil, ""
			if !reflect.DeepEqual(strategy, execution.StrategySnapshot{}) {
				t.Fatal("missing top-level strategy acquired round-level or current defaults")
			}
			currentJSON := legacyStrategyJSON(t, job.ExecutionConfig)
			if !bytes.Equal(currentJSON, legacyStrategyJSON(t, client.Job.GetX(ctx, 2).ExecutionConfig)) {
				t.Fatal("ignored round strategy changed migrated execution or provenance")
			}
			for _, value := range []string{"ignored-round-strategy-secret", "ignored-round-strategy-value", "private-residue-marker", "job-original-key", "api_key"} {
				if bytes.Contains(currentJSON, []byte(value)) {
					t.Fatal("discarded data or plaintext leaked into the migrated snapshot")
				}
			}
			logs := legacyStrategyJSON(t, client.ActivityLog.Query().AllX(ctx))
			if bytes.Contains(logs, []byte("ignored-round-strategy")) || bytes.Contains(logs, []byte("private-residue-marker")) {
				t.Fatal("discarded strategy leaked into migration audit metadata")
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
			historical := snapshot.Rounds[0].Backend.Credential
			current := backendBinding(ctx, client, 1)
			if historical.ID == current.ID {
				t.Fatal("historical job reused the current backend credential")
			}
			for binding, expected := range map[credential.Binding]string{historical: "job-original-key", current: "backend-original-key"} {
				secret, err := credentials.Resolve(ctx, binding, "openai", "https://api.openai.com/v1")
				if err != nil || secret != expected {
					t.Fatalf("saved credential changed: %v", err)
				}
			}
			if client.Credential.Query().CountX(ctx) != 2 || client.CredentialJobReference.Query().CountX(ctx) != 2 {
				t.Fatal("historical credential deduplication or retention changed")
			}
			if err := client.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLegacyRoundStrategyFailureRollsBackAllExecutionChanges(t *testing.T) {
	ctx, client, owner, credentials, backends := credentialTestServices(t)
	backend := credentialTestBackend(t, ctx, backends, owner, "backend", "current-secret")
	first := legacyExecutionFixture(t, ctx, client, owner.ID, backend.ID)
	invalid := legacyExecutionFixture(t, ctx, client, owner.ID, backend.ID)
	for _, row := range []struct {
		id     int
		config map[string]any
	}{{first.ID, first.ExecutionConfig}, {invalid.ID, invalid.ExecutionConfig}} {
		delete(row.config, "strategy")
		translate := row.config["rounds"].([]any)[0].(map[string]any)["translate"].(map[string]any)
		translate["strategy"] = legacyRoundStrategyResidue()
		if row.id == invalid.ID {
			translate["private-unsupported-field"] = "private-unsupported-value"
		}
		client.Job.UpdateOneID(row.id).SetExecutionConfig(row.config).ExecX(ctx)
	}
	profile := legacyTestProfile(t, ctx, client, owner.ID, sourceExecutionProfileConfigData{})
	beforeJobs := legacyStrategyJSON(t, client.Job.Query().AllX(ctx))
	beforeProfile := legacyStrategyJSON(t, client.ExecutionProfile.GetX(ctx, profile.ID))
	beforeCredentials := legacyStrategyJSON(t, client.Credential.Query().AllX(ctx))
	beforeVersions := legacyStrategyJSON(t, client.CredentialVersion.Query().AllX(ctx))
	beforeReferences := legacyStrategyJSON(t, client.CredentialJobReference.Query().AllX(ctx))
	tx, err := client.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, err = migrateExecution(ctx, tx.Client(), credentials.keys)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("migrate job %d execution", invalid.ID)) {
		t.Fatalf("second task did not reject its unsupported sibling field: %v", err)
	}
	for _, sensitive := range []string{"private-unsupported-field", "private-unsupported-value", "ignored-round-strategy", "snapshot-original-secret", "mixed current"} {
		if strings.Contains(err.Error(), sensitive) {
			t.Fatal("migration error exposed private data or falsely diagnosed mixed data")
		}
	}
	converted := tx.Client().Job.GetX(ctx, first.ID)
	if _, exists := converted.ExecutionConfig["schema_version"]; !exists || tx.Client().Credential.Query().CountX(ctx) != 2 || tx.Client().CredentialJobReference.Query().CountX(ctx) != 1 {
		t.Fatal("test did not reach partial conversion before the second task failed")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	for _, pair := range []struct{ before, after []byte }{
		{beforeJobs, legacyStrategyJSON(t, client.Job.Query().AllX(ctx))},
		{beforeProfile, legacyStrategyJSON(t, client.ExecutionProfile.GetX(ctx, profile.ID))},
		{beforeCredentials, legacyStrategyJSON(t, client.Credential.Query().AllX(ctx))},
		{beforeVersions, legacyStrategyJSON(t, client.CredentialVersion.Query().AllX(ctx))},
		{beforeReferences, legacyStrategyJSON(t, client.CredentialJobReference.Query().AllX(ctx))},
	} {
		if !bytes.Equal(pair.before, pair.after) {
			t.Fatal("rollback did not restore execution, profiles, credentials, versions and references")
		}
	}
}
