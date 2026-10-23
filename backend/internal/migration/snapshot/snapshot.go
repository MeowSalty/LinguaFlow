// Package snapshot opens only explicit offline databases and creates consistent
// independent database artifacts. It never prepares runtime schema or markers.
package snapshot

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"modernc.org/sqlite"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
)

func Open(ctx context.Context, cfg *config.ServerConfig, readOnly bool) (*sql.DB, *ent.Client, error) {
	dsn := cfg.DatabaseDSN()
	driver := cfg.Database.Driver
	var err error
	var db *sql.DB
	if driver == "sqlite" {
		path, err := SQLitePath(dsn)
		if err != nil {
			return nil, nil, err
		}
		if err := operation.ValidatePath(path, false); err != nil {
			return nil, nil, err
		}
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			if _, err := os.Lstat(path + suffix); err == nil {
				if err := operation.ValidatePath(path+suffix, false); err != nil {
					return nil, nil, err
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, nil, err
			}
		}
		if readOnly {
			dsn, err = ReadOnlyDSN(driver, dsn)
			if err != nil {
				return nil, nil, err
			}
		}
		db, err = sql.Open("sqlite", dsn)
	} else if driver == "postgres" {
		settings, parseErr := pgx.ParseConfig(dsn)
		if parseErr != nil {
			return nil, nil, operation.Fail(operation.UnsupportedSource, "invalid PostgreSQL connection configuration")
		}
		settings.RuntimeParams["timezone"] = "UTC"
		if readOnly {
			settings.RuntimeParams["default_transaction_read_only"] = "on"
		}
		db = stdlib.OpenDB(*settings)
	} else {
		return nil, nil, operation.Fail(operation.UnsupportedSource, "unsupported database driver")
	}
	if err != nil {
		return nil, nil, operation.Fail(operation.UnsupportedSource, "cannot open the explicit offline database")
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(1)
	ping, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := db.PingContext(ping); err != nil {
		db.Close()
		return nil, nil, operation.Fail(operation.NeedsAction, "offline database is unavailable")
	}
	return db, ent.NewClient(ent.Driver(database.NewDriver(entsql.OpenDB(database.DialectFor(driver), db)))), nil
}

func SQLitePath(dsn string) (string, error) {
	path, _, _ := strings.Cut(dsn, "?")
	if strings.HasPrefix(path, "file:") {
		path = strings.TrimPrefix(path, "file:")
		unescaped, err := url.PathUnescape(path)
		if err != nil {
			return "", operation.Fail(operation.UnsupportedSource, "invalid SQLite path")
		}
		path = unescaped
	}
	if len(path) > 3 && path[0] == '/' && path[2] == ':' {
		path = path[1:]
	}
	path = filepath.FromSlash(path)
	if !filepath.IsAbs(path) || path == ":memory:" || strings.HasPrefix(path, "//") {
		return "", operation.Fail(operation.UnsupportedSource, "offline tools require an ordinary absolute disk database path")
	}
	return filepath.Clean(path), nil
}

func ReadOnlyDSN(driver, dsn string) (string, error) {
	if driver == "postgres" {
		if _, err := pgx.ParseConfig(dsn); err != nil {
			return "", operation.Fail(operation.UnsupportedSource, "invalid PostgreSQL connection input")
		}
		parsed, err := url.Parse(dsn)
		if err == nil && (parsed.Scheme == "postgres" || parsed.Scheme == "postgresql") {
			params := parsed.Query()
			params.Set("default_transaction_read_only", "on")
			params.Set("timezone", "UTC")
			parsed.RawQuery = params.Encode()
			return parsed.String(), nil
		}
		// pgx accepts libpq keyword input; the final explicit values win.
		return dsn + " default_transaction_read_only=on timezone=UTC", nil
	}
	if driver != "sqlite" {
		return "", operation.Fail(operation.UnsupportedSource, "unsupported database driver")
	}
	path, err := SQLitePath(dsn)
	if err != nil {
		return "", err
	}
	_, query, _ := strings.Cut(dsn, "?")
	params, err := url.ParseQuery(query)
	if err != nil {
		return "", operation.Fail(operation.UnsupportedSource, "invalid SQLite connection input")
	}
	for key := range params {
		switch key {
		case "cache", "vfs":
		default:
			params.Del(key)
		}
	}
	params.Set("mode", "ro")
	params.Add("_pragma", "query_only(1)")
	params.Add("_pragma", "busy_timeout(5000)")
	return sqliteURI(path, params), nil
}

func sqliteURI(path string, params url.Values) string {
	path = filepath.ToSlash(path)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	uri := url.URL{Scheme: "file", Path: path, RawQuery: params.Encode()}
	return uri.String()
}

func SQLite(ctx context.Context, db *sql.DB, target string) (err error) {
	if err := operation.ValidatePath(filepath.Dir(target), true); err != nil {
		return err
	}
	created, err := credential.PublishPrivateFile(target, nil)
	if err != nil {
		return err
	}
	if !created {
		return operation.Fail(operation.TargetNotEmpty, "database snapshot path already exists")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, conn.Close()) }()
	err = conn.Raw(func(raw any) (err error) {
		provider, ok := raw.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		})
		if !ok {
			return operation.Fail(operation.UnsupportedSource, "SQLite consistent backup is unavailable")
		}
		backup, err := provider.NewBackup(sqliteURI(target, url.Values{"mode": {"rw"}}))
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, backup.Finish()) }()
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			more, err := backup.Step(256)
			if err != nil || !more {
				return err
			}
		}
	})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(target, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

// EmptyPostgres requires a separately provisioned, entirely empty application
// database. It never drops or truncates preexisting objects.
func EmptyPostgres(ctx context.Context, db *sql.DB) error {
	if err := CheckPostgresCatalog(ctx, db); err != nil {
		if errors.Is(err, ErrPostgresCatalogNotEmpty) {
			return operation.Fail(operation.TargetNotEmpty, "PostgreSQL target contains unsupported catalog objects")
		}
		return operation.Fail(operation.NeedsAction, "cannot inspect the independent PostgreSQL target")
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname NOT IN ('pg_catalog','information_schema') AND n.nspname NOT LIKE 'pg_toast%' AND c.relkind IN ('r','p','v','m','S','f')`).Scan(&count); err != nil {
		return operation.Fail(operation.NeedsAction, "cannot inspect the independent PostgreSQL target")
	}
	if count != 0 {
		return operation.Fail(operation.TargetNotEmpty, "PostgreSQL target contains application objects")
	}
	return nil
}

func PostgresIdentity(ctx context.Context, db *sql.DB) (string, error) {
	var name, schemaName, host string
	var oid, port int64
	if err := db.QueryRowContext(ctx, `SELECT current_database(),current_schema(),COALESCE(inet_server_addr()::text,'local'),COALESCE(inet_server_port(),0),(SELECT oid FROM pg_database WHERE datname=current_database())`).Scan(&name, &schemaName, &host, &port, &oid); err != nil {
		return "", operation.Fail(operation.NeedsAction, "cannot read PostgreSQL database identity")
	}
	if schemaName != "public" {
		return "", operation.Fail(operation.UnsupportedTarget, "this finite PostgreSQL route requires the public schema")
	}
	return operation.Digest([]any{name, schemaName, host, port, oid}), nil
}
