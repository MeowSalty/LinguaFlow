package database

import (
	"context"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

const (
	pingTimeout                  = 5 * time.Second
	migrationLockTimeout         = 30 * time.Second
	migrationUnlockTimeout       = 5 * time.Second
	migrationLockID        int64 = 0x4c696e677561466c
)

// Open 创建并验证共享的 database/sql 连接池及 ent client。
func Open(ctx context.Context, cfg *config.ServerConfig) (*sql.DB, *ent.Client, error) {
	sqlDriver, entDialect, err := driverSettings(cfg.Database.Driver)
	if err != nil {
		return nil, nil, err
	}

	db, err := sql.Open(sqlDriver, cfg.DatabaseDSN())
	if err != nil {
		return nil, nil, databaseError(cfg.Database.Driver, "open", err)
	}
	db.SetMaxOpenConns(cfg.Database.MaxOpenConns)
	db.SetMaxIdleConns(cfg.Database.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.Database.ConnMaxLifetime)

	pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, nil, databaseError(cfg.Database.Driver, "ping", err)
	}

	driver := entsql.OpenDB(entDialect, db)
	client := ent.NewClient(ent.Driver(driver))
	return db, client, nil
}

// AcquireMigrationLock serializes PostgreSQL schema migration across instances.
func AcquireMigrationLock(ctx context.Context, db *sql.DB, driver string) (func() error, error) {
	if driver != config.DatabaseDriverPostgres {
		return func() error { return nil }, nil
	}

	lockCtx, cancel := context.WithTimeout(ctx, migrationLockTimeout)
	defer cancel()
	conn, err := db.Conn(lockCtx)
	if err != nil {
		return nil, databaseError(driver, "acquire migration connection", err)
	}
	if _, err := conn.ExecContext(lockCtx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		_ = conn.Close()
		return nil, databaseError(driver, "acquire migration lock", err)
	}

	return func() error {
		unlockCtx, cancel := context.WithTimeout(context.Background(), migrationUnlockTimeout)
		defer cancel()
		var unlocked bool
		unlockErr := conn.QueryRowContext(unlockCtx, "SELECT pg_advisory_unlock($1)", migrationLockID).Scan(&unlocked)
		closeErr := conn.Close()
		if unlockErr != nil {
			return errors.Join(databaseError(driver, "release migration lock", unlockErr), closeErr)
		}
		if !unlocked {
			return errors.Join(fmt.Errorf("postgres database release migration lock failed: lock was not held"), closeErr)
		}
		return closeErr
	}, nil
}

func driverSettings(driver string) (string, string, error) {
	switch driver {
	case config.DatabaseDriverSQLite:
		return "sqlite", dialect.SQLite, nil
	case config.DatabaseDriverPostgres:
		return "pgx", dialect.Postgres, nil
	default:
		return "", "", fmt.Errorf("database configure failed: unsupported driver")
	}
}

// DialectFor 返回与配置驱动对应的 ent 方言字符串。未知驱动安全降级为 SQLite，
// 供需要按方言分支生成 SQL 的服务层使用（例如 SegmentService 的 JSON 谓词）。
func DialectFor(driver string) string {
	_, entDialect, err := driverSettings(driver)
	if err != nil {
		return dialect.SQLite
	}
	return entDialect
}

func databaseError(driver, stage string, cause error) error {
	detail := cause.Error()
	if driver == config.DatabaseDriverPostgres {
		detail = postgresErrorDetail(cause)
	}
	return fmt.Errorf("%s database %s failed: %s", driver, stage, detail)
}

func postgresErrorDetail(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "context canceled"
	case errors.Is(err, context.DeadlineExceeded), pgconn.Timeout(err):
		return "connection timeout"
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return fmt.Sprintf("%s (SQLSTATE %s)", pgErr.Message, pgErr.Code)
	}
	var parseErr *pgconn.ParseConfigError
	if errors.As(err, &parseErr) {
		return "invalid connection string"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return dnsErr.Error()
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return opErr.Error()
	}
	var unknownAuthority x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthority) {
		return unknownAuthority.Error()
	}
	var hostnameError x509.HostnameError
	if errors.As(err, &hostnameError) {
		return hostnameError.Error()
	}
	var certificateError x509.CertificateInvalidError
	if errors.As(err, &certificateError) {
		return certificateError.Error()
	}
	return fmt.Sprintf("connection failed (%T)", err)
}
