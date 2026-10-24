package snapshot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
)

func TestExternalProtectionUsesDeclarationWithoutPostgresTools(t *testing.T) {
	ctx := context.Background()
	resolved := &config.ResolvedServer{Config: config.ServerConfig{Database: config.DatabaseConfig{Driver: "postgres", DSN: "postgres://operator@example.invalid/source"}}}
	id, _ := operation.NewID()
	p := &operation.Plan{Version: operation.Version, Strategy: operation.Inplace, DatabaseBackup: operation.BackupExternal, ID: id, Kind: "upgrade", Build: "test", Source: operation.Endpoint{Driver: "postgres", Identity: "endpoint-and-oid", InputDigest: InputDigest(resolved)}, SourceFingerprint: operation.Digest("source"), SourceSchema: operation.Digest("schema")}
	p.Baseline = &operation.Baseline{Version: operation.Version, ID: p.ID, PlanDigest: operation.Digest(p), SourceIdentity: p.Source.Identity, SourceFingerprint: p.SourceFingerprint, SourceSchema: p.SourceSchema, InputDigest: p.Source.InputDigest, ConversionTime: time.Now().UTC(), Tables: []operation.TableBaseline{{Name: "users", Columns: []string{"id"}, PrimaryKey: []string{"id"}, Keys: operation.Digest("keys"), Preserved: operation.Digest("values")}}}
	p.Baseline.Facts = map[string]string{"backup_reference": "provider:stopped-source:backup-123"}
	t.Setenv("LINGUAFLOW_MIGRATION_POSTGRES_BIN", filepath.Join(t.TempDir(), "no-tools"))
	directory := filepath.Join(t.TempDir(), "protection")
	if _, err := CaptureProtection(ctx, nil, resolved, p, directory, ""); err == nil {
		t.Fatal("accepted missing backup declaration")
	}
	if _, err := os.Lstat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing declaration created protection directory", err)
	}
	b, err := CaptureProtection(ctx, nil, resolved, p, directory, "provider:stopped-source:backup-123")
	if err != nil {
		t.Fatal(err)
	}
	if b.State != OperatorDeclared || b.Reference != "provider:stopped-source:backup-123" || b.Database.Name != "" {
		t.Fatal("declaration was relabeled as an automatic archive")
	}
	if _, err := LoadProtection(ctx, directory, p); err != nil {
		t.Fatal("bound declaration did not reload", err)
	}
	if _, err := os.Stat(filepath.Join(directory, "database.pgdump")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("external protection created dump archive", err)
	}
	changed := *b
	changed.Reference = "provider:another-backup"
	if err := VerifyProtection(ctx, directory, p, &changed); err == nil {
		t.Fatal("accepted a changed operator declaration")
	}
	changed = *b
	changed.CreatedAt = changed.CreatedAt.Add(time.Second)
	if err := VerifyProtection(ctx, directory, p, &changed); err == nil {
		t.Fatal("accepted a changed confirmation time")
	}
	if err := os.Remove(filepath.Join(directory, b.Deployment.Name)); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProtection(ctx, directory, p); err == nil {
		t.Fatal("accepted missing actual configuration recovery material")
	}
}
