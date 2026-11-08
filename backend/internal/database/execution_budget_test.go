package database

import (
	"context"
	"database/sql"
	"entgo.io/ent/dialect"
	"errors"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
	"testing"
	"time"
)

type executionDeadlineProbe struct {
	dialect.Driver
	deadline time.Time
}

func (*executionDeadlineProbe) Dialect() string { return dialect.SQLite }
func (d *executionDeadlineProbe) Exec(ctx context.Context, _ string, _, _ any) error {
	d.deadline, _ = ctx.Deadline()
	return nil
}
func (d *executionDeadlineProbe) ExecContext(ctx context.Context, _ string, _ ...any) (sql.Result, error) {
	d.deadline, _ = ctx.Deadline()
	return nil, nil
}
func (d *executionDeadlineProbe) QueryContext(ctx context.Context, _ string, _ ...any) (*sql.Rows, error) {
	d.deadline, _ = ctx.Deadline()
	return nil, nil
}

func TestTimeDriverExecutionBudgetRequiresOwner(t *testing.T) {
	for _, raw := range []bool{false, true} {
		probe := &executionDeadlineProbe{}
		d := NewDriver(probe)
		deadline := time.Now().Add(time.Minute)
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		call := func(ctx context.Context) error {
			if raw {
				_, err := d.ExecContext(ctx, "noop")
				return err
			}
			return d.Exec(ctx, "noop", nil, nil)
		}
		if err := call(ctx); err != nil {
			t.Fatal(err)
		}
		if !probe.deadline.Equal(deadline) {
			t.Fatalf("unowned SQL caller deadline changed: %v", probe.deadline)
		}
		start := time.Now()
		if err := call(persistence.WithExecutionTimeout(ctx, 20*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		if delta := probe.deadline.Sub(start); delta <= 0 || delta > 25*time.Millisecond {
			t.Fatalf("owned SQL budget missing: %v", delta)
		}
		cancel()
	}
}

type executionCancelProbe struct{ dialect.Tx }

func (*executionCancelProbe) Exec(ctx context.Context, _ string, _, _ any) error {
	<-ctx.Done()
	return context.Canceled
}
func (*executionCancelProbe) Query(ctx context.Context, _ string, _, _ any) error {
	<-ctx.Done()
	return context.Canceled
}
func TestTimeTransactionKeepsCallerDeadlineCause(t *testing.T) {
	for _, query := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		ctx = persistence.WithExecutionTimeout(ctx, time.Second)
		tx := &timeTx{Tx: &executionCancelProbe{}, ctx: context.Background(), dialect: dialect.SQLite}
		var err error
		if query {
			err = tx.Query(ctx, "noop", nil, nil)
		} else {
			err = tx.Exec(ctx, "noop", nil, nil)
		}
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("driver cancellation lost caller deadline: %v", err)
		}
	}
}
