package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"modernc.org/sqlite"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/migrate"
)

const (
	sqliteTimeFormatVersion  = 1
	sqliteFormatProbeTimeout = 5 * time.Second
)

func isSQLiteContention(err error) bool {
	var sqliteErr *sqlite.Error
	return errors.As(err, &sqliteErr) && (sqliteErr.Code()&0xff == 5 || sqliteErr.Code()&0xff == 6)
}

// 仅重试不持有应用事务的启动读取和连接初始化。
// 在写事务中重试语句可能继续持有锁或快照，阻碍竞争方继续执行。
func retrySQLiteContention(ctx context.Context, attempt func() error) error {
	var lastErr error
	for delay := 10 * time.Millisecond; ; delay = min(2*delay, 100*time.Millisecond) {
		if err := ctx.Err(); err != nil {
			return errors.Join(lastErr, err)
		}
		err := attempt()
		if err != nil && ctx.Err() != nil {
			return errors.Join(err, ctx.Err())
		}
		if err == nil || !isSQLiteContention(err) {
			return err
		}
		lastErr = err
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(lastErr, ctx.Err())
		case <-timer.C:
		}
	}
}

// 即使设置了 busy_timeout，切换日志模式时 SQLite 仍可能立即返回 BUSY。
// 仅在应用事务开始前重试连接初始化；Ping 返回前驱动会关闭失败的物理连接。
func pingSQLite(ctx context.Context, db *sql.DB) error {
	return retrySQLiteContention(ctx, func() error { return db.PingContext(ctx) })
}

// 探测时不执行调用方提供的 SQL，也不设置持久化 PRAGMA。
// 只读检查确认格式有效后，运行时连接池才使用原始 DSN，
// 以保留驱动的参数顺序、别名及逐连接配置。
func sqliteDSNBeforeFormatCheck(dsn string) (string, error) {
	path, query, _ := strings.Cut(dsn, "?")
	params, err := url.ParseQuery(query)
	if err != nil {
		return "", err
	}
	for _, key := range []string{"_pragma", "_journal_mode", "_journal", "_auto_vacuum", "_vacuum"} {
		params.Del(key)
	}
	// SQLite 原生 busy handler 的等待时间可能超过调用方上下文期限。
	// 让探测立即返回，再由有界且可响应上下文的读取重试负责等待。
	params.Del("_timeout")
	params.Set("_busy_timeout", "0")
	return path + "?" + params.Encode(), nil
}

func openSQLiteFormatProbe(ctx context.Context, dsn string, autoMigrate bool) (*sql.DB, error) {
	probeCtx, cancel := context.WithTimeout(ctx, sqliteFormatProbeTimeout)
	defer cancel()
	var probe *sql.DB
	err := retrySQLiteContention(probeCtx, func() error {
		var err error
		probe, err = openSQLiteFormatProbeOnce(probeCtx, dsn, autoMigrate)
		return err
	})
	return probe, err
}

// 每次尝试失败后都会先关闭连接再等待，释放读取锁，
// 避免阻碍其他初始化流程切换日志模式或提交事务。
func openSQLiteFormatProbeOnce(ctx context.Context, dsn string, autoMigrate bool) (_ *sql.DB, err error) {
	probeDSN, err := sqliteDSNBeforeFormatCheck(dsn)
	if err != nil {
		return nil, err
	}
	readOnlyDSN, err := sqliteReadOnlyProbeDSN(probeDSN)
	if err != nil {
		return nil, err
	}
	db, conn, err := openSQLiteProbeConnection(ctx, readOnlyDSN)
	var sqliteErr *sqlite.Error
	if err != nil && readOnlyDSN != probeDSN && errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == 14 {
		// A new database does not exist yet. Fall back to the original open
		// mode, still without any persistent configuration or marker writes.
		db, conn, err = openSQLiteProbeConnection(ctx, probeDSN)
	}
	if err != nil {
		return nil, databaseError(config.DatabaseDriverSQLite, "open format probe connection", err)
	}
	keepAlive := false
	defer func() {
		if err != nil || !keepAlive {
			_ = db.Close()
		}
	}()
	defer conn.Close()
	version, objects, err := sqliteFormatState(ctx, conn)
	if err == nil {
		err = validateSQLiteFormatState(version, objects, autoMigrate)
	}
	if err != nil {
		return nil, databaseError(config.DatabaseDriverSQLite, "probe timestamp format", err)
	}
	if err := CheckMigrationState(ctx, conn, config.DatabaseDriverSQLite); err != nil {
		return nil, databaseError(config.DatabaseDriverSQLite, "probe migration state", err)
	}
	present, err := inspectAuthFormat(ctx, conn, config.DatabaseDriverSQLite)
	if err != nil {
		return nil, fmt.Errorf("probe account database format: %w", err)
	}
	if !present && !autoMigrate {
		return nil, fmt.Errorf("account database is uninitialized and auto_migrate=false; initialize a new database with auto_migrate=true first")
	}
	if !autoMigrate {
		if err := checkSQLiteSchema(ctx, conn); err != nil {
			return nil, databaseError(config.DatabaseDriverSQLite, "probe schema", err)
		}
	}
	var filename string
	if err := conn.QueryRowContext(ctx, "SELECT file FROM pragma_database_list WHERE name='main'").Scan(&filename); err != nil {
		return nil, databaseError(config.DatabaseDriverSQLite, "probe database location", err)
	}
	if filename != "" {
		// Even an idle WAL connection can prevent a journal-mode transition on
		// the runtime connection. Only memory databases need a keepalive.
		return nil, nil
	}
	keepAlive = true
	return db, nil
}

// Existing files are probed with SQLite's read-only open flag so closing the
// probe cannot checkpoint an old WAL into the database file. Memory databases
// keep their original URI and sharing semantics.
func sqliteReadOnlyProbeDSN(dsn string) (string, error) {
	path, query, _ := strings.Cut(dsn, "?")
	params, err := url.ParseQuery(query)
	if err != nil {
		return "", err
	}
	uriPath := path
	if strings.HasPrefix(path, "file:") {
		uriPath, err = url.PathUnescape(strings.TrimPrefix(path, "file:"))
		if err != nil {
			return "", err
		}
	}
	if path == "" || path == ":memory:" || (strings.HasPrefix(path, "file:") && (uriPath == ":memory:" || params.Get("mode") == "memory")) {
		return dsn, nil
	}
	if !strings.HasPrefix(path, "file:") {
		path = "file:" + url.PathEscape(filepath.ToSlash(path))
	}
	params.Set("mode", "ro")
	return path + "?" + params.Encode(), nil
}

func openSQLiteProbeConnection(ctx context.Context, dsn string) (*sql.DB, *sql.Conn, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	return db, conn, nil
}

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
	if err := CheckMigrationState(ctx, conn, config.DatabaseDriverSQLite); err != nil {
		return err
	}
	version, objects, err := sqliteFormatState(ctx, conn)
	if err != nil {
		return err
	}
	if err := validateSQLiteFormatState(version, objects, autoMigrate); err != nil {
		return err
	}
	if version == sqliteTimeFormatVersion {
		return nil
	}

	// Serialize only the empty-database check and marker. Do not hold this
	// connection's write lock while ent migrates using another pool connection.
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(cleanupCtx, "ROLLBACK")
	}()
	if err := CheckMigrationState(ctx, conn, config.DatabaseDriverSQLite); err != nil {
		return err
	}
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
	committed = err == nil
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
