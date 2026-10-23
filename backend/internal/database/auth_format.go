package database

import (
	"context"
	"database/sql"
	sqldriver "database/sql/driver"
	"errors"
	"fmt"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/migrate"
)

// AuthFormatVersion describes the account contract, independently of timestamp
// encoding and automatically upgraded business data. Older accounts require an
// explicitly reviewed offline conversion; this release only prepares empty DBs.
const AuthFormatVersion = 1

const authFormatTable = "linguaflow_auth_format"

var ErrAuthFormatUnsupported = errors.New("unsupported account database format; preserve the existing database and use a new database or data directory; this release does not convert old accounts")

// ensureAuthFormat commits a format reservation before ent creates its schema.
// A failed schema creation can therefore be retried without mistaking the
// partially prepared new database for an old account database. This is metadata,
// not an instance initialization marker, and never establishes an identity.
func ensureAuthFormat(ctx context.Context, db *sql.DB, driver string, autoMigrate bool) error {
	if !autoMigrate {
		conn, err := db.Conn(ctx)
		if err != nil {
			return err
		}
		defer conn.Close()
		present, err := inspectAuthFormat(ctx, conn, driver)
		if err != nil {
			return err
		}
		if !present {
			return fmt.Errorf("account database is uninitialized and auto_migrate=false; initialize a new database with auto_migrate=true first")
		}
		if driver == config.DatabaseDriverPostgres {
			return checkPostgresSchema(ctx, conn)
		}
		return checkSQLiteSchema(ctx, conn)
	}
	return withMigrationConnection(ctx, db, driver, func(conn *sql.Conn) (err error) {
		begin := "BEGIN"
		if driver == config.DatabaseDriverSQLite {
			// Acquire the writer before reading metadata to serialize independent
			// processes without a deferred SQLite read-to-write upgrade.
			begin = "BEGIN IMMEDIATE"
		}
		if _, err := conn.ExecContext(ctx, begin); err != nil {
			return err
		}
		if err := CheckMigrationState(ctx, conn, driver); err != nil {
			return err
		}
		committed := false
		defer func() {
			if committed {
				return
			}
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if _, rollbackErr := conn.ExecContext(cleanupCtx, "ROLLBACK"); rollbackErr != nil {
				// database/sql does not own this explicit transaction. Discard
				// the connection rather than pooling an uncertain open writer.
				_ = conn.Raw(func(any) error { return sqldriver.ErrBadConn })
				err = errors.Join(err, fmt.Errorf("rollback account format reservation: %w", rollbackErr))
			}
		}()
		present, err := inspectAuthFormat(ctx, conn, driver)
		if err != nil {
			return err
		}
		if !present {
			if _, err := conn.ExecContext(ctx, "CREATE TABLE "+authFormatTable+" (id INTEGER NOT NULL PRIMARY KEY CHECK (id = 1), version INTEGER NOT NULL CHECK (version > 0))"); err != nil {
				return err
			}
			if _, err := conn.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s (id, version) VALUES (1, %d)", authFormatTable, AuthFormatVersion)); err != nil {
				return err
			}
		}
		_, err = conn.ExecContext(ctx, "COMMIT")
		committed = err == nil
		return err
	})
}

// inspectAuthFormat only reads schema metadata and the singleton reservation.
// An absent marker is accepted solely when no application objects exist.
func inspectAuthFormat(ctx context.Context, conn *sql.Conn, driver string) (bool, error) {
	query := "SELECT name, type FROM sqlite_schema WHERE name NOT GLOB 'sqlite_*'"
	if driver == config.DatabaseDriverPostgres {
		query = "SELECT c.relname, CASE WHEN c.relkind IN ('r', 'p') THEN 'table' ELSE 'other' END FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND c.relkind IN ('r', 'p', 'v', 'm', 'S', 'f')"
	}
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		return false, err
	}
	objects, marked := 0, false
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			rows.Close()
			return false, err
		}
		objects++
		if name == authFormatTable {
			if kind != "table" {
				rows.Close()
				return false, ErrAuthFormatUnsupported
			}
			marked = true
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return false, err
	}
	if !marked {
		if objects != 0 {
			return false, ErrAuthFormatUnsupported
		}
		return false, nil
	}
	rows, err = conn.QueryContext(ctx, "SELECT id, version FROM "+authFormatTable+" LIMIT 2")
	if err != nil {
		if driver == config.DatabaseDriverSQLite && (isSQLiteContention(err) || ctx.Err() != nil) {
			// 读取架构与标记之间出现锁竞争，并不代表账户格式无效。
			// 保留该错误，以便只读探测稍后重试。
			return false, err
		}
		return false, fmt.Errorf("%w: invalid format marker", ErrAuthFormatUnsupported)
	}
	defer rows.Close()
	var count int
	for rows.Next() {
		var id, version int
		if err := rows.Scan(&id, &version); err != nil || id != 1 || version != AuthFormatVersion {
			return false, ErrAuthFormatUnsupported
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	if count != 1 {
		return false, ErrAuthFormatUnsupported
	}
	return true, nil
}

func checkPostgresSchema(ctx context.Context, conn *sql.Conn) error {
	for _, table := range migrate.Tables {
		rows, err := conn.QueryContext(ctx, "SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1", table.Name)
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
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		for _, column := range table.Columns {
			if !columns[column.Name] {
				return fmt.Errorf("PostgreSQL schema is incomplete (missing %s.%s) and auto_migrate=false; enable auto_migrate to finish schema initialization", table.Name, column.Name)
			}
		}
	}
	return nil
}
