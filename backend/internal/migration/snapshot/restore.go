package snapshot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
)

// PrepareSQLiteRestore preserves the failed committed database, raw main file
// and present WAL before connections close. It also prepares the verified
// replacement on the database volume. Publication needs a durable route intent.
func PrepareSQLiteRestore(ctx context.Context, db *sql.DB, cfg *config.ServerConfig, p *operation.Plan, backupDir string, b *ProtectionReceipt) (*operation.Restore, error) {
	if err := VerifyProtection(ctx, backupDir, p, b); err != nil {
		return nil, err
	}
	path, err := SQLitePath(cfg.DatabaseDSN())
	if err != nil {
		return nil, err
	}
	parent := filepath.Dir(path)
	directory := filepath.Join(parent, ".linguaflow-restore-"+p.ID)
	if _, err := os.Lstat(directory); !errors.Is(err, os.ErrNotExist) {
		id, err := operation.NewID()
		if err != nil {
			return nil, err
		}
		directory += "-" + id
	}
	if err := credential.CreatePrivateDirectory(directory); err != nil {
		return nil, err
	}
	dirID, err := operation.Identity(directory)
	if err != nil {
		return nil, err
	}
	parentID, err := operation.Identity(parent)
	if err != nil {
		return nil, err
	}
	fileID, err := operation.Identity(path)
	if err != nil {
		return nil, err
	}
	r := &operation.Restore{Version: operation.Version, ID: p.ID, PlanDigest: operation.Digest(p), BaselineDigest: operation.BaselineDigest(p), Phase: "prepared", Directory: directory, DirectoryIdentity: dirID, DatabasePath: path, DatabaseDirectoryIdentity: parentID, DatabaseIdentity: fileID, OriginalIdentity: p.Source.Identity}
	failed := filepath.Join(directory, "failed.sqlite")
	if err := SQLite(ctx, db, failed); err != nil {
		return nil, err
	}
	r.FailedDatabase, err = operation.Hash(ctx, failed)
	if err != nil {
		return nil, err
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(path + suffix); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		name := "failed-main.sqlite" + suffix
		if _, err := operation.Copy(ctx, path+suffix, filepath.Join(directory, name)); err != nil {
			return nil, err
		}
	}
	r.Replacement, err = operation.Copy(ctx, filepath.Join(backupDir, b.Database.Name), filepath.Join(directory, "replacement.sqlite"))
	if err != nil {
		return nil, err
	}
	if r.Replacement.SHA256 != b.Database.SHA256 || r.Replacement.Size != b.Database.Size {
		return nil, operation.Fail(operation.BackupIncomplete, "prepared SQLite restoration differs from original snapshot")
	}
	r.ReplacementIdentity, err = operation.Identity(filepath.Join(directory, r.Replacement.Name))
	return r, err
}

func VerifyRestoreIntent(ctx context.Context, cfg *config.ServerConfig, p *operation.Plan, r *operation.Restore, b *ProtectionReceipt) error {
	path, err := SQLitePath(cfg.DatabaseDSN())
	if err != nil {
		return err
	}
	id, err := operation.Identity(r.Directory)
	parentID, parentErr := operation.Identity(filepath.Dir(path))
	if r.Version != operation.Version || r.ID != p.ID || r.PlanDigest != operation.Digest(p) || r.BaselineDigest != operation.BaselineDigest(p) || r.OriginalIdentity != p.Source.Identity || r.DatabasePath != path || filepath.Dir(r.Directory) != filepath.Dir(path) || !strings.HasPrefix(filepath.Base(r.Directory), ".linguaflow-restore-"+p.ID) || err != nil || id != r.DirectoryIdentity || parentErr != nil || parentID != r.DatabaseDirectoryIdentity {
		return operation.Fail(operation.Incompatible, "SQLite restore intent or volume identity changed")
	}
	if r.FailedDatabase.Name != "failed.sqlite" || r.Replacement.Name != "replacement.sqlite" || r.Replacement.SHA256 != b.Database.SHA256 || r.Replacement.Size != b.Database.Size || r.DatabaseIdentity == "" || r.ReplacementIdentity == "" {
		return operation.Fail(operation.BackupIncomplete, "SQLite recovery evidence is incomplete")
	}
	proof, err := operation.Hash(ctx, filepath.Join(r.Directory, r.FailedDatabase.Name))
	if err != nil || proof != r.FailedDatabase {
		return operation.Fail(operation.BackupIncomplete, "failed database preservation changed")
	}
	return nil
}

// PublishSQLiteRestore resumes across the two no-replace moves. The caller has
// closed all database handles and persists each phase before the next boundary.
func PublishSQLiteRestore(ctx context.Context, cfg *config.ServerConfig, p *operation.Plan, r *operation.Restore, b *ProtectionReceipt, save func() error, fault func(string) error) error {
	if err := VerifyRestoreIntent(ctx, cfg, p, r, b); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	callFault := func(phase string) error {
		if fault != nil {
			return fault(phase)
		}
		return nil
	}
	path := r.DatabasePath
	displaced := filepath.Join(r.Directory, "displaced.sqlite")
	if _, err := os.Lstat(path); err == nil {
		id, err := operation.Identity(path)
		if err != nil {
			return err
		}
		if id == r.ReplacementIdentity {
			r.Phase = "published"
			return save()
		}
		if id != r.DatabaseIdentity {
			return operation.Fail(operation.BaselineChanged, "SQLite file was replaced during recovery")
		}
		if err := callFault("before_restore_displace"); err != nil {
			return err
		}
		if err := operation.MoveOwnedFile(path, displaced, r.DatabaseIdentity); err != nil {
			return fmt.Errorf("preserve SQLite main file before restoration: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	id, err := operation.Identity(displaced)
	if err != nil || id != r.DatabaseIdentity {
		return operation.Fail(operation.Incompatible, "displaced original file evidence is missing")
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(path + suffix); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		id, err := operation.Identity(path + suffix)
		if err != nil {
			return err
		}
		if err := operation.MoveOwnedFile(path+suffix, filepath.Join(r.Directory, "displaced"+suffix), id); err != nil {
			return fmt.Errorf("preserve SQLite %s before restoration: %w", suffix, err)
		}
	}
	r.Phase = "displaced"
	if err := save(); err != nil {
		return err
	}
	if err := callFault("restore_displaced"); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	replacement := filepath.Join(r.Directory, r.Replacement.Name)
	proof, err := operation.Hash(ctx, replacement)
	if err != nil || proof != r.Replacement {
		return operation.Fail(operation.BackupIncomplete, "SQLite recovery replacement changed")
	}
	if err := operation.MoveOwnedFile(replacement, path, r.ReplacementIdentity); err != nil {
		return fmt.Errorf("publish SQLite restoration: %w", err)
	}
	r.Phase = "published"
	if err := save(); err != nil {
		return err
	}
	return callFault("restore_published")
}
