package service

import (
	"context"
	stdsql "database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

type organizationBudgetDriver struct {
	dialect.Driver
	observe func(context.Context)
	block   bool
}

func (d *organizationBudgetDriver) Tx(ctx context.Context) (dialect.Tx, error) {
	tx, err := d.Driver.Tx(ctx)
	if err != nil {
		return nil, err
	}
	return &organizationBudgetTx{Tx: tx, owner: d}, nil
}

type organizationBudgetTx struct {
	dialect.Tx
	owner *organizationBudgetDriver
}

var _ interface {
	ExecContext(context.Context, string, ...any) (stdsql.Result, error)
	QueryContext(context.Context, string, ...any) (*stdsql.Rows, error)
} = (*organizationBudgetTx)(nil)

func (tx *organizationBudgetTx) ExecContext(ctx context.Context, query string, args ...any) (stdsql.Result, error) {
	if tx.owner.observe != nil {
		tx.owner.observe(ctx)
	}
	if tx.owner.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return tx.Tx.(interface {
		ExecContext(context.Context, string, ...any) (stdsql.Result, error)
	}).ExecContext(ctx, query, args...)
}

func (tx *organizationBudgetTx) Query(ctx context.Context, query string, args, value any) error {
	if tx.owner.observe != nil {
		tx.owner.observe(ctx)
	}
	if tx.owner.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return tx.Tx.Query(ctx, query, args, value)
}

func (tx *organizationBudgetTx) QueryContext(ctx context.Context, query string, args ...any) (*stdsql.Rows, error) {
	if tx.owner.observe != nil {
		tx.owner.observe(ctx)
	}
	if tx.owner.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return tx.Tx.(interface {
		QueryContext(context.Context, string, ...any) (*stdsql.Rows, error)
	}).QueryContext(ctx, query, args...)
}

func organizationBudgetClient(t *testing.T, observe func(context.Context), block bool) *ent.Client {
	t.Helper()
	db, err := stdsql.Open("sqlite", ":memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	driver := &organizationBudgetDriver{Driver: entsql.OpenDB(dialect.SQLite, db)}
	client := ent.NewClient(ent.Driver(database.NewDriver(driver)))
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Schema.Create(t.Context()); err != nil {
		t.Fatal(err)
	}
	driver.observe, driver.block = observe, block
	return client
}

func TestOrganizationPolicyDeadlineReachesRawAndEntQueries(t *testing.T) {
	started := time.Now()
	queries := 0
	client := organizationBudgetClient(t, func(ctx context.Context) {
		queries++
		deadline, ok := ctx.Deadline()
		if !ok || deadline.After(time.Now().Add(organizationTransactionRetryBudget)) || deadline.Before(started) {
			t.Errorf("query has no total policy deadline: %v, %v", deadline, ok)
		}
	}, false)
	err := withOrganizationTransaction(context.Background(), client, func(tx *ent.Client) error {
		if _, err := tx.User.Query().IDs(context.Background()); err != nil {
			return err
		}
		rows, err := tx.QueryContext(context.Background(), "SELECT 1")
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return rows.Err()
		}
		var value int
		return rows.Scan(&value)
	})
	if err != nil || queries != 2 {
		t.Fatalf("raw/ent policy propagation: queries=%d err=%v", queries, err)
	}
}

func TestOrganizationCallerDeadlineInterruptsRawAndEntQueries(t *testing.T) {
	for _, raw := range []bool{false, true} {
		name := "ent"
		if raw {
			name = "raw"
		}
		t.Run(name, func(t *testing.T) {
			var callerDeadline time.Time
			client := organizationBudgetClient(t, func(queryCtx context.Context) {
				deadline, ok := queryCtx.Deadline()
				if !ok || !deadline.Equal(callerDeadline) {
					t.Errorf("caller deadline was extended: %v", deadline)
				}
			}, true)
			ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
			defer cancel()
			callerDeadline, _ = ctx.Deadline()
			attempts := 0
			err := withOrganizationTransaction(ctx, client, func(tx *ent.Client) error {
				attempts++
				// The callback's captured context can be unbounded; transaction
				// statements must still receive the earlier caller/policy deadline.
				if raw {
					_, err := tx.QueryContext(context.Background(), "SELECT 1")
					return err
				}
				_, err := tx.User.Query().IDs(context.Background())
				return err
			})
			if !errors.Is(err, context.DeadlineExceeded) || attempts != 1 {
				t.Fatalf("blocked query outlived deadline/retried: attempts=%d err=%v", attempts, err)
			}
		})
	}
}

func TestOrganizationSQLiteBusyWaitRespectsCallerWallDeadline(t *testing.T) {
	// Exercise the runtime connector; a bare sql.Open pool has no physical
	// busy deadline enforcement. Independent pools still contend on SQLite.
	dsn := filepath.ToSlash(filepath.Join(t.TempDir(), "organization.db")) + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	var clients [2]*ent.Client
	for i := range clients {
		db, err := database.OpenSQLitePool(dsn)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		client := ent.NewClient(ent.Driver(database.NewDriver(entsql.OpenDB(dialect.SQLite, db))))
		t.Cleanup(func() { _ = client.Close() })
		clients[i] = client
	}
	if err := clients[0].Schema.Create(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := initializePasswordConnection(t.Context(), clients[0]); err != nil {
		t.Fatal(err)
	}
	clients[0].SystemSetting.Create().SetKey(SettingRegistrationEnabled).SetValue("true").SaveX(t.Context())
	held, err := clients[0].Tx(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback()
	if err := lockPasswordConnection(context.Background(), held.Client()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = withOrganizationTransaction(ctx, clients[1], func(tx *ent.Client) error { return lockPasswordConnection(ctx, tx) })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SQLite busy wait lost caller deadline: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("SQLite busy_timeout overrode caller wall deadline: %v", elapsed)
	}
}
