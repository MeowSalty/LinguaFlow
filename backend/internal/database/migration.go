package database

import (
	"context"
	"database/sql"
	sqldriver "database/sql/driver"
	"errors"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

// WithMigrationLock runs schema preparation on the same connection that owns
// PostgreSQL's advisory lock. It therefore also works with a one-connection pool.
// The callback must use the supplied client and must not close it.
func WithMigrationLock(ctx context.Context, db *sql.DB, driver string, migrate func(*ent.Client) error) (err error) {
	_, name, err := driverSettings(driver)
	if err != nil {
		return err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return databaseError(driver, "acquire migration connection", err)
	}
	defer func() { err = errors.Join(err, conn.Close()) }()
	if driver == config.DatabaseDriverPostgres {
		lockCtx, cancel := context.WithTimeout(ctx, migrationLockTimeout)
		_, lockErr := conn.ExecContext(lockCtx, "SELECT pg_advisory_lock($1)", migrationLockID)
		cancel()
		if lockErr != nil {
			// Cancellation can race acquisition; discard the session rather than
			// return a possibly locked connection to the pool.
			_ = conn.Raw(func(any) error { return sqldriver.ErrBadConn })
			return databaseError(driver, "acquire migration lock", lockErr)
		}
		defer func() {
			unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var unlocked bool
			unlockErr := conn.QueryRowContext(unlockCtx, "SELECT pg_advisory_unlock($1)", migrationLockID).Scan(&unlocked)
			if unlockErr != nil {
				// A pooled connection must never retain a session-level advisory lock.
				_ = conn.Raw(func(any) error { return sqldriver.ErrBadConn })
				err = errors.Join(err, databaseError(driver, "release migration lock", unlockErr))
			} else if !unlocked {
				err = errors.Join(err, errors.New("migration advisory lock was not held"))
			}
		}()
	}
	drv := &migrationConnection{Driver: entsql.NewDriver(name, entsql.Conn{ExecQuerier: conn}), conn: conn}
	client := ent.NewClient(ent.Driver(NewDriver(drv)))
	return migrate(client)
}

type migrationConnection struct {
	*entsql.Driver
	conn *sql.Conn
}

func (d *migrationConnection) Tx(ctx context.Context) (dialect.Tx, error) { return d.BeginTx(ctx, nil) }
func (d *migrationConnection) BeginTx(ctx context.Context, options *sql.TxOptions) (dialect.Tx, error) {
	tx, err := d.conn.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &entsql.Tx{Conn: entsql.Conn{ExecQuerier: tx}, Tx: tx}, nil
}
func (*migrationConnection) Close() error { return nil }
