package database

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqlschema "entgo.io/ent/dialect/sql/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	entmigrate "github.com/MeowSalty/LinguaFlow/backend/internal/ent/migrate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

// Exercise an actual upgrade with old rows, including disabled identities and
// existing ciphertext. A fresh-schema test cannot catch destructive defaults.
func TestStorageRetirementSchemaUpgradePreservesExistingFacts(t *testing.T) {
	ctx := t.Context()
	cfg := config.DefaultServerConfig()
	cfg.DataDir = t.TempDir()
	cfg.Database.DSN = filepath.Join(cfg.DataDir, "retirement-upgrade.db")
	cfg.AutoMigrate = true
	db, client, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	tables, err := sqlschema.CopyTables(entmigrate.Tables)
	if err != nil {
		t.Fatal(err)
	}
	legacy := make([]*sqlschema.Table, 0, len(tables))
	for _, table := range tables {
		if table.Name == "storage_retirements" || table.Name == "storage_retirement_entries" || table.Name == "storage_path_claims" {
			continue
		}
		removed := map[string]bool{}
		switch table.Name {
		case "storage_connections", "storage_spaces":
			for _, name := range []string{"lifecycle", "retirement_id", "removed_at", "removed_by", "execution_generation"} {
				removed[name] = true
			}
		case "storage_auth_versions":
			for _, name := range []string{"purpose", "retirement_id", "purged_at"} {
				removed[name] = true
			}
		}
		if len(removed) > 0 {
			columns := make([]*sqlschema.Column, 0, len(table.Columns))
			for _, col := range table.Columns {
				if !removed[col.Name] {
					columns = append(columns, col)
				}
			}
			table.Columns = columns
			indexes := make([]*sqlschema.Index, 0, len(table.Indexes))
			for _, idx := range table.Indexes {
				keep := true
				for _, col := range idx.Columns {
					keep = keep && !removed[col.Name]
				}
				if keep {
					indexes = append(indexes, idx)
				}
			}
			table.Indexes = indexes
			foreignKeys := make([]*sqlschema.ForeignKey, 0, len(table.ForeignKeys))
			for _, fk := range table.ForeignKeys {
				keep := true
				for _, col := range fk.Columns {
					keep = keep && !removed[col.Name]
				}
				if keep {
					foreignKeys = append(foreignKeys, fk)
				}
			}
			table.ForeignKeys = foreignKeys
			if table.Annotation != nil {
				annotation := *table.Annotation
				annotation.Checks = map[string]string{}
				for name, check := range table.Annotation.Checks {
					keep := true
					for col := range removed {
						keep = keep && !strings.Contains(check, col)
					}
					if keep {
						annotation.Checks[name] = check
					}
				}
				table.Annotation = &annotation
			}
		}
		legacy = append(legacy, table)
	}
	if err := entmigrate.Create(ctx, client.Schema, legacy); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(timeutil.SQLiteLayout)
	if _, err := db.ExecContext(ctx, `INSERT INTO storage_connections (id, created_at, updated_at, name, driver, status, auth_source, active_auth_generation, management_generation) VALUES (1, ?, ?, 'old', 's3', 'disabled', 'stored', 7, 9)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO storage_spaces (id, created_at, updated_at, connection_id, name, identity, marker_nonce, bucket, prefix, status, verified, live_bytes, management_generation) VALUES (1, ?, ?, 1, 'old', 'original-identity', 'original-nonce', 'bucket', 'prefix', 'disabled', true, 17, 3)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO storage_auth_versions (id, created_at, updated_at, connection_id, generation, key_id, nonce, ciphertext, status) VALUES (1, ?, ?, 1, 7, 'old-key', ?, ?, 'revoked')`, now, now, []byte("original-nonce"), []byte("original-ciphertext")); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := client.Schema.Create(ctx); err != nil {
			t.Fatal(err)
		}
	}
	conn := client.StorageConnection.GetX(ctx, 1)
	space := client.StorageSpace.GetX(ctx, 1)
	auth := client.StorageAuthVersion.GetX(ctx, 1)
	if conn.Lifecycle != "available" || space.Lifecycle != "available" || conn.RetirementID != nil || space.RetirementID != nil || conn.RemovedAt != nil || space.RemovedAt != nil {
		t.Fatal("upgrade inferred retirement from old administrative state")
	}
	if conn.Status != "disabled" || space.Status != "disabled" || conn.ManagementGeneration != 9 || space.ManagementGeneration != 3 || conn.ActiveAuthGeneration != 7 || !space.Verified || space.LiveBytes != 17 || space.Identity != "original-identity" || space.MarkerNonce != "original-nonce" {
		t.Fatal("upgrade changed old identity, authorization, or accounting")
	}
	if auth.Purpose != "business" || auth.RetirementID != nil || auth.PurgedAt != nil || auth.Status != "revoked" || !bytes.Equal(auth.Ciphertext, []byte("original-ciphertext")) || !bytes.Equal(auth.Nonce, []byte("original-nonce")) {
		t.Fatal("upgrade discarded or reactivated old authorization")
	}
	if client.StorageRetirement.Query().CountX(ctx) != 0 || client.StoragePathClaim.Query().CountX(ctx) != 0 {
		t.Fatal("schema upgrade fabricated retirement or path reconciliation evidence")
	}
}
