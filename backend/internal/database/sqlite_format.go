package database

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/migrate"
)

const sqliteTimeFormatVersion = 1

// validateSQLiteTimeDSN prevents driver options from bypassing canonical storage
// or returning something other than a time.Time for datetime columns.
func validateSQLiteTimeDSN(dsn string) error {
	_, query, found := strings.Cut(dsn, "?")
	if !found {
		return nil
	}
	params, err := url.ParseQuery(query)
	if err != nil {
		return fmt.Errorf("sqlite database configure failed: invalid DSN query")
	}
	for _, key := range []string{"_time_format", "_time_integer_format", "_timezone", "_inttotime", "_texttotime"} {
		if params.Has(key) {
			return fmt.Errorf("sqlite database configure failed: %s is managed by LinguaFlow; remove this DSN parameter", key)
		}
	}
	return nil
}

// ensureSQLiteTimeFormat runs before any schema or business initialization.
// The marker describes encoding, not schema completion: failed schema creation
// can be retried through the normal ent migration path.
func ensureSQLiteTimeFormat(ctx context.Context, db *sql.DB, autoMigrate bool) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	version, objects, err := sqliteFormatState(ctx, conn)
	if err != nil {
		return err
	}
	if err := validateSQLiteFormatState(version, objects, autoMigrate); err != nil {
		return err
	}
	if version == sqliteTimeFormatVersion {
		if !autoMigrate {
			return checkSQLiteSchema(ctx, conn)
		}
		return nil
	}

	// Serialize only the empty-database check and marker. Do not hold this
	// connection's write lock while ent migrates using another pool connection.
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(cleanupCtx, "ROLLBACK")
	}()
	version, objects, err = sqliteFormatState(ctx, conn)
	if err != nil {
		return err
	}
	if err := validateSQLiteFormatState(version, objects, autoMigrate); err != nil {
		return err
	}
	if version == 0 {
		if _, err := conn.ExecContext(ctx, "PRAGMA user_version = 1"); err != nil {
			return err
		}
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	return err
}

func sqliteFormatState(ctx context.Context, conn *sql.Conn) (version, objects int, err error) {
	// Read both values in one snapshot; another initializer may mark and build
	// the schema between two separate statements.
	err = conn.QueryRowContext(ctx, "SELECT user_version, (SELECT count(*) FROM sqlite_schema WHERE name NOT GLOB 'sqlite_*') FROM pragma_user_version").Scan(&version, &objects)
	return
}

func validateSQLiteFormatState(version, objects int, autoMigrate bool) error {
	switch version {
	case sqliteTimeFormatVersion:
		return nil
	case 0:
		if objects != 0 {
			return fmt.Errorf("legacy SQLite timestamp format: preserve the existing database and configure a new database path or data directory; automatic conversion is not supported")
		}
		if !autoMigrate {
			return fmt.Errorf("SQLite database is uninitialized and auto_migrate=false; initialize a new database with auto_migrate=true first")
		}
		return nil
	default:
		return fmt.Errorf("unsupported SQLite timestamp format version %d (supported: %d); database has not been converted", version, sqliteTimeFormatVersion)
	}
}

func checkSQLiteSchema(ctx context.Context, conn *sql.Conn) error {
	// Schema metadata only: never scan business rows during startup.
	for _, table := range migrate.Tables {
		rows, err := conn.QueryContext(ctx, "SELECT name FROM pragma_table_info(?)", table.Name)
		if err != nil {
			return err
		}
		columns := make(map[string]bool, len(table.Columns))
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				return err
			}
			columns[name] = true
		}
		err = rows.Err()
		closeErr := rows.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		for _, column := range table.Columns {
			if !columns[column.Name] {
				return fmt.Errorf("SQLite schema is incomplete (missing %s.%s) and auto_migrate=false; enable auto_migrate to finish schema initialization", table.Name, column.Name)
			}
		}
	}
	return nil
}
