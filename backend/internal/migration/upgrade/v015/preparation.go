package v015

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
)

// PrepareBaseline persists original evidence before protection and preparing.
// An owned partially converted database can only reuse that evidence.
func PrepareBaseline(ctx context.Context, db *sql.DB, p *operation.Plan, j *operation.Journal, directory string, owned bool, references ...string) error {
	reference := ""
	if len(references) > 0 {
		reference = strings.TrimSpace(references[0])
	}
	if p.DatabaseBackup == operation.BackupAuto && reference != "" {
		return operation.Fail(operation.Incompatible, "backup-reference only applies to external database protection")
	}
	path := filepath.Join(directory, "baseline.json")
	if _, err := os.Lstat(path); err == nil {
		if err := operation.LoadBaseline(directory, p, j); err != nil {
			return err
		}
		if p.DatabaseBackup == operation.BackupExternal && (p.Baseline.Facts["backup_reference"] == "" || reference != "" && reference != p.Baseline.Facts["backup_reference"]) {
			return operation.Fail(operation.Incompatible, "backup reference differs from the original baseline declaration")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else {
		if owned {
			return operation.Fail(operation.BackupIncomplete, "original baseline is missing from an already reserved database")
		}
		if p.DatabaseBackup == operation.BackupExternal && reference == "" {
			return operation.Fail(operation.BackupIncomplete, "provide --backup-reference before capturing the original PostgreSQL baseline")
		}
		b, err := CaptureBaseline(ctx, db, p)
		if err != nil {
			return err
		}
		if err := CaptureAllocationBaseline(ctx, db, p, b); err != nil {
			return err
		}
		if p.DatabaseBackup == operation.BackupExternal {
			b.Facts["backup_reference"] = reference
		}
		if err := b.Bind(p); err != nil {
			return err
		}
		if err := operation.Write(path, b, true); err != nil {
			return err
		}
		p.Baseline = b
	}
	j.BaselineDigest = operation.BaselineDigest(p)
	if owned {
		if _, _, err := operation.Steps(ctx, db, p); err != nil {
			return err
		}
	}
	return operation.SaveJournal(directory, j)
}

// CheckProgress verifies format preconditions before the next domain write.
// Preparing alone still requires every original row and mutable value intact.
func CheckProgress(ctx context.Context, db *sql.DB, p *operation.Plan, steps []string) error {
	if contains(steps, "schema") {
		return VerifyShape(ctx, db, p)
	}
	probe, err := ProbeOperation(ctx, db, p.Source.Driver, p, "")
	if err != nil || probe.Format != "v015" || probe.Mode != p.Source.Mode {
		return operation.Fail(operation.UnsupportedSource, "preparing database no longer has the complete original format")
	}
	original := p.Baseline
	current, err := CaptureBaseline(ctx, db, p)
	p.Baseline = original
	if err != nil {
		return err
	}
	if err := CaptureAllocationBaseline(ctx, db, p, current); err != nil {
		return err
	}
	if operation.Digest(current.Tables) != operation.Digest(original.Tables) || len(current.Changes) != len(original.Changes) {
		return operation.Fail(operation.BaselineChanged, "original business rows changed before schema conversion")
	}
	if original.Facts["allocation"] != current.Facts["allocation"] {
		return operation.Fail(operation.BaselineChanged, "original allocation changed before schema conversion")
	}
	for i, c := range current.Changes {
		o := original.Changes[i]
		if c.Table != o.Table || c.ID != o.ID || operation.Digest(c.Before) != operation.Digest(o.Before) {
			return operation.Fail(operation.BaselineChanged, "original mutable metadata changed before schema conversion")
		}
	}
	return nil
}

func loadMaterials(ctx context.Context, directory string, p *operation.Plan, j *operation.Journal) (*BackupReceipt, error) {
	if err := operation.LoadBaseline(directory, p, j); err != nil {
		return nil, err
	}
	bound, err := operation.LoadProtectionBinding(directory, p, j)
	if err != nil {
		return nil, err
	}
	if !bound {
		return nil, operation.Fail(operation.BackupIncomplete, "durable original protection binding is missing")
	}
	return LoadBackupReceipt(ctx, j.BackupDirectory, p)
}
