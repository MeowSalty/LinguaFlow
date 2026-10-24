package v015

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/snapshot"
)

func rollbackInplace(ctx context.Context, directory string, p *operation.Plan, j *operation.Journal, options RunOptions) (*operation.Report, error) {
	b, err := loadMaterials(ctx, directory, p, j)
	if err != nil {
		return nil, err
	}
	narrow, err := config.ResolveMigrationDatabase(PlannedInputs(p))
	if err != nil {
		return nil, err
	}
	resolved, err := ResolveProbedInputs(PlannedInputs(p), narrow)
	if err != nil {
		return nil, err
	}
	if InputDigest(resolved) != p.Source.InputDigest {
		return nil, operation.Fail(operation.BaselineChanged, "restore configuration or encryption keys changed")
	}
	unlock, err := snapshot.LockDatabase(ctx, &resolved.Config)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if p.Source.Driver == "postgres" {
		return requestPostgresRestore(ctx, directory, p, j, resolved, b)
	}
	intentPath := filepath.Join(directory, "restore-intent.json")
	if _, err := os.Lstat(intentPath); err == nil {
		var intent operation.Restore
		if err := operation.Read(intentPath, &intent); err != nil {
			return nil, err
		}
		if j.Restore != nil && (j.Restore.Directory != intent.Directory || j.Restore.ReplacementIdentity != intent.ReplacementIdentity || j.Restore.DatabaseIdentity != intent.DatabaseIdentity) {
			return nil, operation.Fail(operation.Incompatible, "SQLite journal and durable restore intent differ")
		}
		if j.Restore == nil {
			j.Restore = &intent
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if j.Restore == nil {
		db, _, err := snapshot.Open(ctx, &resolved.Config, true)
		if err != nil {
			return nil, err
		}
		identity, identityErr := EndpointIdentity(ctx, db, resolved.Config)
		if identityErr != nil || identity != p.Source.Identity {
			db.Close()
			return nil, operation.Fail(operation.BaselineChanged, "selected original SQLite database identity changed")
		}
		if err := verifyRestoredSource(ctx, db, p, resolved, b); err == nil {
			db.Close()
			return recordRestored(directory, p, j, b, identity, "original_unchanged")
		}
		owned, err := operation.Owned(ctx, db, p)
		if err != nil || !owned {
			db.Close()
			return nil, operation.Fail(operation.Incompatible, "SQLite database has neither original baseline nor an owned conversion")
		}
		if err := verifyRollbackBoundary(ctx, db, p); err != nil {
			db.Close()
			return nil, err
		}
		intent, err := snapshot.PrepareSQLiteRestore(ctx, db, &resolved.Config, p, j.BackupDirectory, &b.ProtectionReceipt)
		closeErr := db.Close()
		if err := errors.Join(err, closeErr); err != nil {
			return nil, err
		}
		if err := operation.Write(intentPath, intent, true); err != nil {
			return nil, err
		}
		j.Restore, j.Phase = intent, "restoring"
		if err := operation.SaveJournal(directory, j); err != nil {
			return nil, err
		}
		if err := fault(options, "restore_prepared"); err != nil {
			return nil, err
		}
	}
	j.Phase = "restoring"
	save := func() error { return operation.SaveJournal(directory, j) }
	if err := snapshot.PublishSQLiteRestore(ctx, &resolved.Config, p, j.Restore, &b.ProtectionReceipt, save, options.Fault); err != nil {
		return nil, err
	}
	db, _, err := snapshot.Open(ctx, &resolved.Config, true)
	if err != nil {
		return nil, err
	}
	verifyErr := verifyRestoredSource(ctx, db, p, resolved, b)
	identity, identityErr := EndpointIdentity(ctx, db, resolved.Config)
	closeErr := db.Close()
	fileID, fileErr := operation.Identity(j.Restore.DatabasePath)
	if errors.Join(verifyErr, identityErr, closeErr, fileErr) != nil || fileID != j.Restore.ReplacementIdentity {
		return nil, operation.Fail(operation.NeedsAction, "published SQLite restoration has not verified its original baseline, keys and identity")
	}
	if err := fault(options, "restore_verified"); err != nil {
		return nil, err
	}
	return recordRestored(directory, p, j, b, identity, "verified")
}

func verifyRollbackBoundary(ctx context.Context, db *sql.DB, p *operation.Plan) error {
	_, state, err := operation.Steps(ctx, db, p)
	if err != nil {
		return err
	}
	if state == "ready" {
		result, err := operation.ResultDigest(ctx, db, p)
		if err != nil {
			return err
		}
		current, err := BusinessFingerprint(ctx, db, p.Source.Driver)
		if err != nil || current != result {
			return operation.Fail(operation.NeedsAction, "business writes occurred after migration; automatic database overwrite is refused")
		}
	}
	return nil
}

func verifyRestoredSource(ctx context.Context, db *sql.DB, p *operation.Plan, resolved *config.ResolvedServer, b *BackupReceipt) error {
	probe, err := Probe(ctx, db, p.Source.Driver)
	if err != nil || probe.Format != "v015" || probe.Fingerprint != p.SourceFingerprint || probe.SchemaDigest != p.SourceSchema || probe.Mode != p.Source.Mode {
		return operation.Fail(operation.BaselineChanged, "restored database differs from the original released baseline")
	}
	if err := VerifySourceKeys(ctx, db, resolved.CredentialKeys); err != nil {
		return err
	}
	return VerifyOriginalReferences(ctx, p, b)
}

func requestPostgresRestore(ctx context.Context, directory string, p *operation.Plan, j *operation.Journal, resolved *config.ResolvedServer, b *BackupReceipt) (*operation.Report, error) {
	db, _, err := snapshot.Open(ctx, &resolved.Config, true)
	if err != nil {
		return nil, err
	}
	identity, identityErr := EndpointIdentity(ctx, db, resolved.Config)
	if identityErr != nil || identity != p.Source.Identity {
		db.Close()
		return nil, operation.Fail(operation.BaselineChanged, "PostgreSQL OID or endpoint changed; use independent restoration acceptance before registering recovery")
	}
	if err := verifyRestoredSource(ctx, db, p, resolved, b); err == nil {
		db.Close()
		return recordRestored(directory, p, j, b, identity, "verified")
	}
	owned, err := operation.Owned(ctx, db, p)
	if err != nil || !owned {
		db.Close()
		return nil, operation.Fail(operation.Incompatible, "PostgreSQL restoration requires the original database or its owned conversion")
	}
	if err := verifyRollbackBoundary(ctx, db, p); err != nil {
		db.Close()
		return nil, err
	}
	if err := db.Close(); err != nil {
		return nil, err
	}
	db, _, err = snapshot.Open(ctx, &resolved.Config, false)
	if err != nil {
		return nil, err
	}
	err = operation.Abandon(ctx, db, p.Source.Driver, p)
	if err := errors.Join(err, db.Close()); err != nil {
		return nil, err
	}
	j.Phase = "restoring"
	j.Restore = &operation.Restore{Version: operation.Version, ID: p.ID, PlanDigest: operation.Digest(p), BaselineDigest: operation.BaselineDigest(p), Phase: "restore_required", OriginalIdentity: identity}
	if err := operation.SaveJournal(directory, j); err != nil {
		return nil, err
	}
	r := upgradeReport(p, j, b, nil)
	r.Verified, r.Phase, r.Restoration = false, "restore_required", "restore_required"
	r.Recovery = j.Restore
	r.Next = []string{"Keep all service writers, workers and GC stopped. Have the backup owner restore the recorded database backup into the original database, preserving its database OID and endpoint.", "Restore the protected deployment and keyring bytes when needed, then run rollback --offline with this operation directory to verify the original baseline and register recovery.", "An OID or endpoint change requires independent restoration acceptance; this operation will not adopt another database automatically."}
	if p.DatabaseBackup == operation.BackupExternal {
		r.Next = append([]string{"External database protection is the operator declaration bound to backup reference " + b.Reference + "; this tool has not captured or restored a database archive."}, r.Next...)
	} else {
		r.Next = append([]string{"The captured custom archive is " + filepath.Join(j.BackupDirectory, b.Database.Name) + ". Restore it with matching pg_restore under the backup owner's control."}, r.Next...)
	}
	instructions := filepath.Join(directory, "restore-instructions.json")
	_, statErr := os.Lstat(instructions)
	if err := operation.Write(instructions, r, errors.Is(statErr, os.ErrNotExist)); err != nil {
		return nil, err
	}
	return r, saveReport(directory, r)
}

func recordRestored(directory string, p *operation.Plan, j *operation.Journal, b *BackupReceipt, identity, result string) (*operation.Report, error) {
	if j.Restore == nil {
		j.Restore = &operation.Restore{Version: operation.Version, ID: p.ID, PlanDigest: operation.Digest(p), BaselineDigest: operation.BaselineDigest(p), OriginalIdentity: p.Source.Identity}
	}
	j.Restore.Phase, j.Restore.RestoredIdentity = "verified", identity
	j.Phase, j.ErrorCode = "rolled_back", ""
	if err := operation.SaveJournal(directory, j); err != nil {
		return nil, err
	}
	r := upgradeReport(p, j, b, nil)
	r.Restoration, r.Recovery = result, j.Restore
	r.Next = []string{"Original database baseline, retained files and encryption keys verified. Select the original release while writers remain stopped.", "Keep the original protection and all failure-preservation materials; restoration returns to the recorded backup time."}
	return r, saveReport(directory, r)
}
