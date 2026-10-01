package database

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"entgo.io/ent/dialect"

	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

// TimeDriver applies the application's timestamp encoding to writes and query
// parameters alike. Use NewDriver for every ent client, including test clients.
type TimeDriver struct{ dialect.Driver }

func NewDriver(driver dialect.Driver) *TimeDriver { return &TimeDriver{Driver: driver} }

func (d *TimeDriver) Exec(ctx context.Context, query string, args, v any) error {
	return d.Driver.Exec(ctx, query, timeArgs(d.Dialect(), args), v)
}

func (d *TimeDriver) Query(ctx context.Context, query string, args, v any) error {
	return d.Driver.Query(ctx, query, timeArgs(d.Dialect(), args), v)
}

func (d *TimeDriver) Tx(ctx context.Context) (dialect.Tx, error) {
	tx, err := d.Driver.Tx(ctx)
	if err != nil {
		return nil, err
	}
	return &timeTx{Tx: tx, dialect: d.Dialect()}, nil
}

func (d *TimeDriver) BeginTx(ctx context.Context, opts *sql.TxOptions) (dialect.Tx, error) {
	opener, ok := d.Driver.(interface {
		BeginTx(context.Context, *sql.TxOptions) (dialect.Tx, error)
	})
	if !ok {
		return nil, fmt.Errorf("database driver does not support BeginTx")
	}
	tx, err := opener.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &timeTx{Tx: tx, dialect: d.Dialect()}, nil
}

type timeTx struct {
	dialect.Tx
	dialect string
}

func (t *timeTx) Exec(ctx context.Context, query string, args, v any) error {
	return t.Tx.Exec(ctx, query, timeArgs(t.dialect, args), v)
}

func (t *timeTx) Query(ctx context.Context, query string, args, v any) error {
	return t.Tx.Query(ctx, query, timeArgs(t.dialect, args), v)
}

func timeArgs(driver string, args any) any {
	values, ok := args.([]any)
	if !ok {
		return args
	}
	var normalized []any
	for i, value := range values {
		t, changed := timeArg(driver, value)
		if !changed {
			continue
		}
		if normalized == nil {
			normalized = append([]any(nil), values...)
		}
		normalized[i] = t
	}
	if normalized == nil {
		return args
	}
	return normalized
}

func timeArg(driver string, value any) (any, bool) {
	var instant time.Time
	switch v := value.(type) {
	case time.Time:
		instant = v
	case *time.Time:
		if v == nil {
			return nil, true
		}
		instant = *v
	case sql.NullTime:
		if !v.Valid {
			return nil, true
		}
		instant = v.Time
	case *sql.NullTime:
		if v == nil || !v.Valid {
			return nil, true
		}
		instant = v.Time
	default:
		return value, false
	}
	instant = instant.UTC()
	if driver == dialect.SQLite {
		return instant.Format(timeutil.SQLiteLayout), true
	}
	if driver == dialect.Postgres {
		instant = instant.Truncate(time.Microsecond)
	}
	return instant, true
}
