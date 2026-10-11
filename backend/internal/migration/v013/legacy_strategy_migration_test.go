package v013

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
