package database

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

const (
	MigrationStateTable   = "linguaflow_migration_state"
	MigrationStateVersion = 1
)

var ErrMigrationIncomplete = errors.New("database migration is incomplete; preserve the database and resume its migration operation")

// CheckMigrationState allows databases without the migration contract and
// rejects every present state except the one durable state safe for startup.
// The digest is an operation receipt; it is deliberately not compared with
// the runtime schema because startup must remain independent of migration.
// COMPAT(migration-journal-readers): retain the durable startup barrier while
// supported migration operations or restored backups can contain this state.
// Tracking: backend/internal/migration/catalog/compatibility.md
func CheckMigrationState(ctx context.Context, conn *sql.Conn, driver string) error {
	query := "SELECT table_type FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = $1"
	if driver == config.DatabaseDriverSQLite {
		query = "SELECT type FROM sqlite_schema WHERE name=?"
	} else if driver != config.DatabaseDriverPostgres {
		return fmt.Errorf("check migration state: unsupported database driver %q", driver)
	}
	var objectType string
	if err := conn.QueryRowContext(ctx, query, MigrationStateTable).Scan(&objectType); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("check migration state table: %w", err)
	}
	if (driver == config.DatabaseDriverSQLite && objectType != "table") ||
		(driver == config.DatabaseDriverPostgres && objectType != "BASE TABLE") {
		return ErrMigrationIncomplete
	}

	rows, err := conn.QueryContext(ctx, migrationStateSelect(driver))
	if err != nil {
		return errors.Join(ErrMigrationIncomplete, fmt.Errorf("read migration state: %w", err))
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, version int
		var operationID, planDigest, targetSignature, state string
		if err := rows.Scan(&id, &version, &operationID, &planDigest, &targetSignature, &state); err != nil {
			return errors.Join(ErrMigrationIncomplete, fmt.Errorf("read migration state row: %w", err))
		}
		count++
		if id != 1 || version != MigrationStateVersion ||
			!validMigrationHex(operationID, 32) || !validMigrationHex(planDigest, 64) ||
			!validMigrationHex(targetSignature, 64) || state != "ready" {
			return ErrMigrationIncomplete
		}
	}
	if err := rows.Err(); err != nil {
		return errors.Join(ErrMigrationIncomplete, fmt.Errorf("read migration state rows: %w", err))
	}
	if count != 1 {
		return ErrMigrationIncomplete
	}
	return nil
}

func validMigrationHex(value string, hexLength int) bool {
	if len(value) != hexLength {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func checkMigrationState(ctx context.Context, db *sql.DB, driver string) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return CheckMigrationState(ctx, conn, driver)
}

func migrationStateSelect(driver string) string {
	if driver == config.DatabaseDriverPostgres {
		return "SELECT id, version, operation_id, plan_digest, target_signature, state FROM " + MigrationStateTable
	}
	return "SELECT id, version, operation_id, plan_digest, target_signature, state FROM " + MigrationStateTable
}
