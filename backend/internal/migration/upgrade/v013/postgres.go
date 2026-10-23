package v013

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

// RunPostgres holds the migration advisory lock on one connection. Schema DDL
// and converted rows share its SERIALIZABLE transaction. Rehearsals roll back;
// apply publishes the exact encryption keys through beforeCommit before commit.
func RunPostgres(ctx context.Context, db *sql.DB, keys *credential.Keyring, apply bool, beforeCommit func() error) (report Report, err error) {
	if err = checkTargetCompatibility(); err != nil {
		return
	}
	err = database.WithMigrationLock(ctx, db, config.DatabaseDriverPostgres, func(pinned *ent.Client) error {
		tx, err := pinned.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return err
		}
		defer tx.Rollback()
		client := tx.Client()
		// ent's transactional driver suppresses Atlas' inner commit/rollback.
		if err := client.Schema.Create(ctx); err != nil {
			return fmt.Errorf("prepare migration schema: %w", err)
		}
		report, err = Convert(ctx, client, keys, config.ModeServer)
		if err != nil {
			return err
		}
		if !apply {
			return tx.Rollback()
		}
		if beforeCommit != nil {
			if err := beforeCommit(); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
	return
}
