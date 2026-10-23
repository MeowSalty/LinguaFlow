package v015

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/snapshot"
)

func TestIndependentRecoveryRejectsOmittedKeysObjectsAndChangedBytes(t *testing.T) {
	ctx := context.Background()
	options, sourceDir := fixtureDeployment(t, "serve")
	resolved, err := configSource(options)
	if err != nil {
		t.Fatal(err)
	}
	db, _, err := snapshot.Open(ctx, &resolved.Config, false)
	if err != nil {
		t.Fatal(err)
	}
	addReleasedBusiness(t, db, resolved.CredentialKeys, sourceDir)
	db.Close()
	p, err := NewPlan(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("stop after independent backup")
	_, err = Run(ctx, RunOptions{Directory: options.Directory, Offline: true, Fault: func(at string) error {
		if at == "backup" {
			return injected
		}
		return nil
	}})
	if !errors.Is(err, injected) {
		t.Fatal(err)
	}
	_, j, err := operation.Load(options.Directory)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := LoadBackupReceipt(ctx, j.BackupDirectory, p)
	if err != nil {
		t.Fatal(err)
	}
	assertIncomplete := func(r *BackupReceipt) {
		t.Helper()
		var classified *operation.Error
		if err := VerifyBackupReceipt(ctx, j.BackupDirectory, p, r); !errors.As(err, &classified) || classified.Code != operation.BackupIncomplete {
			t.Fatalf("invalid evidence accepted: %v", err)
		}
	}
	copy := *receipt
	copy.Keyring = operation.Artifact{}
	assertIncomplete(&copy)
	copy = *receipt
	copy.Objects = append([]BackupObject(nil), receipt.Objects...)
	for i := range copy.Objects {
		if copy.Objects[i].Status == "copied" {
			copy.Objects[i].Status = "missing"
			break
		}
	}
	assertIncomplete(&copy)
	for _, o := range receipt.Objects {
		if o.Status != "copied" {
			continue
		}
		path := filepath.Join(j.BackupDirectory, filepath.FromSlash(o.Recovery))
		if err := os.WriteFile(path, []byte("tampered independent copy"), 0600); err != nil {
			t.Fatal(err)
		}
		assertIncomplete(receipt)
		break
	}
	_, err = Run(ctx, RunOptions{Directory: options.Directory, Offline: true})
	var classified *operation.Error
	if !errors.As(err, &classified) || classified.Code != operation.BackupIncomplete {
		t.Fatalf("invalid backup resumed: %v", err)
	}
	if _, err := os.Stat(options.TargetDataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid evidence created target")
	}
}

func TestPreservedComparisonRejectsUnplannedSourceGenerationAndExtraRows(t *testing.T) {
	ctx := context.Background()
	options, sourceDir := fixtureDeployment(t, "serve")
	resolved, err := configSource(options)
	if err != nil {
		t.Fatal(err)
	}
	original, _, err := snapshot.Open(ctx, &resolved.Config, false)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	addReleasedBusiness(t, original, resolved.CredentialKeys, sourceDir)
	p, err := NewPlan(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, RunOptions{Directory: options.Directory, Offline: true}); err != nil {
		t.Fatal(err)
	}
	cfg := resolved.Config
	cfg.DataDir, cfg.Database.DSN = options.TargetDataDir, ""
	target, _, err := snapshot.Open(ctx, &cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	for _, statement := range []string{
		"UPDATE resources SET source_generation=source_generation+1 WHERE id=24",
		"INSERT INTO system_settings(created_at,updated_at,key,value) SELECT created_at,updated_at,'undeclared','true' FROM system_settings LIMIT 1",
	} {
		tx, err := target.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyPreserved(ctx, original, target, p); err == nil {
			t.Fatal("undeclared business mutation accepted", statement)
		}
		if _, err := target.ExecContext(ctx, "UPDATE resources SET source_generation=6 WHERE id=24"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRehearsalIsolationBackupRestoreAndRollbackTime(t *testing.T) {
	ctx := context.Background()
	options, sourceDir := fixtureDeployment(t, "local")
	p, err := NewPlan(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, RunOptions{Directory: options.Directory, Offline: true, Rehearse: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(options.TargetDataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rehearsal wrote final target")
	}
	if _, err := Run(ctx, RunOptions{Directory: options.Directory, Offline: true}); err != nil {
		t.Fatal(err)
	}
	_, j, err := operation.Load(options.Directory)
	if err != nil {
		t.Fatal(err)
	}
	r, err := LoadBackupReceipt(ctx, j.BackupDirectory, p)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := configSource(options)
	if err != nil {
		t.Fatal(err)
	}
	cfg := resolved.Config
	cfg.DataDir = t.TempDir()
	path := filepath.Join(cfg.DataDir, "restored.sqlite")
	if _, err := operation.Copy(ctx, filepath.Join(j.BackupDirectory, r.Database.Name), path); err != nil {
		t.Fatal(err)
	}
	cfg.Database.DSN = path
	restored, _, err := snapshot.Open(ctx, &cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	probe, err := Probe(ctx, restored, "sqlite")
	if err != nil || probe.Format != "v015" || probe.Fingerprint != p.SourceFingerprint {
		t.Fatal("restored release differs", err)
	}
	for range 2 {
		report, err := RollbackOperation(ctx, options.Directory, true)
		if err != nil || report.BackupTime != r.CreatedAt || report.Phase != "rolled_back" {
			t.Fatal("rollback time lost", err)
		}
	}
	if _, err := os.Stat(filepath.Join(sourceDir, "linguaflow.db")); err != nil {
		t.Fatal("source removed", err)
	}
	if _, err := LoadBackupReceipt(ctx, j.BackupDirectory, p); err != nil {
		t.Fatal("rollback removed backup", err)
	}
}
