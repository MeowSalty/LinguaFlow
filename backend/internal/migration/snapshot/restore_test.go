package snapshot

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
)

func TestSQLiteProtectionAndRecoveryKeepCommittedWALAndResumePublication(t *testing.T) {
	for _, boundary := range []string{"before_restore_displace", "restore_displaced", "restore_published"} {
		t.Run(boundary, func(t *testing.T) {
			ctx := context.Background()
			directory := t.TempDir()
			path := filepath.Join(directory, "source.sqlite")
			db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=wal_autocheckpoint(0)")
			if err != nil {
				t.Fatal(err)
			}
			for _, query := range []string{"CREATE TABLE example(id INTEGER PRIMARY KEY,value TEXT)", "INSERT INTO example VALUES(1,'original committed WAL')"} {
				if _, err := db.ExecContext(ctx, query); err != nil {
					t.Fatal(err)
				}
			}
			cfg := config.ServerConfig{DataDir: directory, Database: config.DatabaseConfig{Driver: "sqlite", DSN: path}}
			resolved := &config.ResolvedServer{Config: cfg}
			id, _ := operation.NewID()
			p := &operation.Plan{Version: operation.Version, Strategy: operation.Inplace, DatabaseBackup: operation.BackupAuto, ID: id, Kind: "upgrade", Build: "test", Source: operation.Endpoint{Driver: "sqlite", Identity: "original", InputDigest: InputDigest(resolved)}, SourceFingerprint: operation.Digest("source"), SourceSchema: operation.Digest("schema")}
			p.Baseline = &operation.Baseline{Version: operation.Version, ID: p.ID, PlanDigest: operation.Digest(p), SourceIdentity: p.Source.Identity, SourceFingerprint: p.SourceFingerprint, SourceSchema: p.SourceSchema, InputDigest: p.Source.InputDigest, ConversionTime: time.Now().UTC(), Tables: []operation.TableBaseline{{Name: "example", Columns: []string{"id", "value"}, PrimaryKey: []string{"id"}, Keys: operation.Digest("keys"), Preserved: operation.Digest("values")}}}
			backupDir := filepath.Join(t.TempDir(), "backup")
			b, err := CaptureProtection(ctx, db, resolved, p, backupDir, "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, "UPDATE example SET value='failed migration'"); err != nil {
				t.Fatal(err)
			}
			r, err := PrepareSQLiteRestore(ctx, db, &cfg, p, backupDir, b)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(r.Directory, "failed-main.sqlite-wal")); err != nil {
				t.Fatal("failed WAL was not preserved", err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			injected := errors.New("recovery cancellation")
			fired := false
			err = PublishSQLiteRestore(ctx, &cfg, p, r, b, func() error { return nil }, func(phase string) error {
				if phase == boundary {
					fired = true
					return injected
				}
				return nil
			})
			if !fired || !errors.Is(err, injected) {
				t.Fatal("boundary not reached", err)
			}
			if err := PublishSQLiteRestore(ctx, &cfg, p, r, b, func() error { return nil }, nil); err != nil {
				t.Fatal("resume publication", err)
			}
			restored, _, err := Open(ctx, &cfg, true)
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			var value string
			if err := restored.QueryRowContext(ctx, "SELECT value FROM example WHERE id=1").Scan(&value); err != nil || value != "original committed WAL" {
				t.Fatal("original WAL content not recovered", value, err)
			}
			failedCfg := config.ServerConfig{DataDir: r.Directory, Database: config.DatabaseConfig{Driver: "sqlite", DSN: filepath.Join(r.Directory, r.FailedDatabase.Name)}}
			failed, _, err := Open(ctx, &failedCfg, true)
			if err != nil {
				t.Fatal(err)
			}
			defer failed.Close()
			if err := failed.QueryRowContext(ctx, "SELECT value FROM example WHERE id=1").Scan(&value); err != nil || value != "failed migration" {
				t.Fatal("failed snapshot lost committed state", value, err)
			}
		})
	}
}
