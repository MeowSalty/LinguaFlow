package snapshot

import (
	"context"
	"errors"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
)

// LockDatabase is acquired before rechecking the stopped source and capturing
// its baseline. PostgreSQL advisory locks use a dedicated read-only connection.
func LockDatabase(ctx context.Context, cfg *config.ServerConfig) (func() error, error) {
	if cfg.Database.Driver == "sqlite" {
		path, err := SQLitePath(cfg.DatabaseDSN())
		if err != nil {
			return nil, err
		}
		return operation.LockPath(path + ".migration.lock")
	}
	db, _, err := Open(ctx, cfg, true)
	if err != nil {
		return nil, err
	}
	unlock, err := database.AcquireMigrationLock(ctx, db, cfg.Database.Driver)
	if err != nil {
		db.Close()
		return nil, err
	}
	return func() error { return errors.Join(unlock(), db.Close()) }, nil
}
