package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/activitylog"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/sseevent"
)

// The SQL transaction is real: only its commit acknowledgement is lost.
// Keeping the time/rollback driver underneath also exercises native lock SQL.
type historyLostAckDriver struct {
	dialect.Driver
	tracking      atomic.Bool
	loseNextAck   atomic.Bool
	transactions  atomic.Int64
	lostAcks      atomic.Int64
	secondTaskSQL atomic.Int64
	secondTaskID  int
	afterCommit   func(context.Context)
	ackError      error
}

func (d *historyLostAckDriver) observe(query string, args any) {
	if !d.tracking.Load() || d.secondTaskID == 0 || !strings.Contains(query, "jobs") {
		return
	}
	values, ok := args.([]any)
	if !ok {
		return
	}
	for _, value := range values {
		if value == d.secondTaskID || value == int64(d.secondTaskID) {
			d.secondTaskSQL.Add(1)
			return
		}
	}
}

func (d *historyLostAckDriver) Exec(ctx context.Context, query string, args, value any) error {
	d.observe(query, args)
	return d.Driver.Exec(ctx, query, args, value)
}

func (d *historyLostAckDriver) Query(ctx context.Context, query string, args, value any) error {
	d.observe(query, args)
	return d.Driver.Query(ctx, query, args, value)
}

func (d *historyLostAckDriver) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	d.observe(query, args)
	return d.Driver.(interface {
		ExecContext(context.Context, string, ...any) (sql.Result, error)
	}).ExecContext(ctx, query, args...)
}

func (d *historyLostAckDriver) Tx(ctx context.Context) (dialect.Tx, error) {
	return d.BeginTx(ctx, nil)
}

func (d *historyLostAckDriver) BeginTx(ctx context.Context, opts *sql.TxOptions) (dialect.Tx, error) {
	if d.tracking.Load() {
		d.transactions.Add(1)
	}
	tx, err := d.Driver.(interface {
		BeginTx(context.Context, *sql.TxOptions) (dialect.Tx, error)
	}).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &historyLostAckTx{Tx: tx, driver: d, ctx: ctx}, nil
}

type historyLostAckTx struct {
	dialect.Tx
	driver *historyLostAckDriver
	ctx    context.Context
}

func (tx *historyLostAckTx) Exec(ctx context.Context, query string, args, value any) error {
	tx.driver.observe(query, args)
	return tx.Tx.Exec(ctx, query, args, value)
}

func (tx *historyLostAckTx) Query(ctx context.Context, query string, args, value any) error {
	tx.driver.observe(query, args)
	return tx.Tx.Query(ctx, query, args, value)
}

func (tx *historyLostAckTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	tx.driver.observe(query, args)
	return tx.Tx.(interface {
		ExecContext(context.Context, string, ...any) (sql.Result, error)
	}).ExecContext(ctx, query, args...)
}

func (tx *historyLostAckTx) Commit() error {
	if err := tx.Tx.Commit(); err != nil {
		return err
	}
	if tx.driver.loseNextAck.CompareAndSwap(true, false) {
		tx.driver.lostAcks.Add(1)
		if tx.driver.afterCommit != nil {
			tx.driver.afterCommit(tx.ctx)
		}
		if tx.driver.ackError != nil {
			return tx.driver.ackError
		}
		return context.DeadlineExceeded
	}
	return nil
}

func historyLostAckFixture(t *testing.T) (taskHistoryFixture, *historyLostAckDriver) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	driver := &historyLostAckDriver{Driver: database.NewDriver(entsql.OpenDB(dialect.SQLite, db))}
	client := ent.NewClient(ent.Driver(driver))
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Schema.Create(t.Context()); err != nil {
		t.Fatal(err)
	}
	return historyFixtureWithClient(t, client), driver
}

func TestTaskHistoryBatchStopsAfterLostCommitAcknowledgement(t *testing.T) {
	f, driver := historyLostAckFixture(t)
	first := f.job(t, "completed", 40)
	second := f.job(t, "failed", 40)
	for _, j := range []*ent.Job{first, second} {
		f.c.SSEEvent.Create().SetJobID(j.ID).SetSeq(int64(j.ID)).SetType("batch").SetLevel("info").SetMessage("retained evidence").SaveX(t.Context())
	}
	driver.secondTaskID = second.ID
	subscription := f.s.broker.Subscribe(first.ID)
	defer f.s.broker.Unsubscribe(first.ID, subscription)
	driver.tracking.Store(true)
	driver.loseNextAck.Store(true)
	results := f.s.BatchDelete(t.Context(), f.owner, []HistoryTarget{f.target(first), f.target(second)})
	driver.tracking.Store(false)
	if len(results) != 2 || results[0].Status != "failed" || results[1].Status != "deferred" {
		t.Fatalf("lost acknowledgement results = %+v; uncertain first item must not be deferred", results)
	}
	if driver.lostAcks.Load() != 1 || driver.transactions.Load() != 1 || driver.secondTaskSQL.Load() != 0 {
		t.Fatalf("batch continued after uncertainty: lost=%d transactions=%d second-task SQL=%d", driver.lostAcks.Load(), driver.transactions.Load(), driver.secondTaskSQL.Load())
	}
	select {
	case _, open := <-subscription:
		if open {
			t.Fatal("subscription remained open after task absence was confirmed")
		}
	default:
		t.Fatal("uncertain commit did not recheck absence and close subscriptions")
	}
	if _, err := f.c.Job.Get(t.Context(), first.ID); !ent.IsNotFound(err) {
		t.Fatalf("first SQL commit did not actually delete task: %v", err)
	}
	if f.c.SSEEvent.Query().Where(sseevent.JobIDEQ(first.ID)).CountX(t.Context()) != 0 {
		t.Fatal("first SQL commit did not delete its dependencies")
	}
	if got := f.c.Job.GetX(t.Context(), second.ID); got.Status != second.Status || !got.UpdatedAt.Equal(second.UpdatedAt) {
		t.Fatalf("unprocessed second task changed: %+v", got)
	}
	if f.c.SSEEvent.Query().Where(sseevent.JobIDEQ(second.ID)).CountX(t.Context()) != 1 {
		t.Fatal("unprocessed second task lost history")
	}
	logs := f.c.ActivityLog.Query().Where(activitylog.ActionEQ("job.history_deleted")).AllX(t.Context())
	if len(logs) != 1 || logs[0].ResourceID == nil || *logs[0].ResourceID != first.ID {
		t.Fatalf("commit acknowledgement loss changed atomic audit facts: %+v", logs)
	}
}

// Expire the request exactly after the real commit, without timing sleeps. The
// driver waits for the derived transaction context to observe that deadline.
type historyDeadlineAfterCommit struct {
	context.Context
	done    chan struct{}
	expired atomic.Bool
	once    sync.Once
}

func (ctx *historyDeadlineAfterCommit) Done() <-chan struct{} { return ctx.done }
func (ctx *historyDeadlineAfterCommit) Err() error {
	if ctx.expired.Load() {
		return context.DeadlineExceeded
	}
	return nil
}
func (ctx *historyDeadlineAfterCommit) expire() {
	ctx.once.Do(func() { ctx.expired.Store(true); close(ctx.done) })
}

func TestTaskHistoryUncertainCommitRemainsFailedAfterRequestDeadline(t *testing.T) {
	f, driver := historyLostAckFixture(t)
	j := f.job(t, "cancelled", 40)
	ctx := &historyDeadlineAfterCommit{Context: context.Background(), done: make(chan struct{})}
	defer ctx.expire()
	driver.afterCommit = func(transactionCtx context.Context) {
		ctx.expire()
		<-transactionCtx.Done()
	}
	driver.loseNextAck.Store(true)
	err := f.s.Delete(ctx, f.owner, f.target(j))
	var uncertain *historyUncertainError
	if !errors.As(err, &uncertain) || errors.Is(err, ErrTaskCleanupDeferred) || historyDeleteStatus(err) != "failed" {
		t.Fatalf("request deadline disguised uncertain commit: %T %v", err, err)
	}
	if !errors.Is(uncertain.cause, context.DeadlineExceeded) || driver.lostAcks.Load() != 1 {
		t.Fatalf("lost acknowledgement was not injected: %v", uncertain.cause)
	}
	if _, err := f.c.Job.Get(t.Context(), j.ID); !ent.IsNotFound(err) {
		t.Fatalf("commit did not persist before request deadline: %v", err)
	}
}

func TestTaskHistoryCommitConflictRemainsUncertain(t *testing.T) {
	f, driver := historyLostAckFixture(t)
	j := f.job(t, "completed", 40)
	driver.ackError = &pgconn.PgError{Code: "40001", Message: "injected commit acknowledgement failure"}
	driver.tracking.Store(true)
	driver.loseNextAck.Store(true)
	err := f.s.Delete(t.Context(), f.owner, f.target(j))
	var uncertain *historyUncertainError
	if !errors.As(err, &uncertain) || driver.transactions.Load() != 1 || historyDeleteStatus(err) != "failed" {
		t.Fatalf("retried an uncertain commit conflict: error=%v transactions=%d", err, driver.transactions.Load())
	}
	if _, err := f.c.Job.Get(t.Context(), j.ID); !ent.IsNotFound(err) {
		t.Fatalf("commit did not persist before acknowledgement error: %v", err)
	}
}

func TestTaskHistoryBudgetClassificationPreservesUncertainty(t *testing.T) {
	uncertain := &historyUncertainError{cause: context.DeadlineExceeded}
	wrapped := fmt.Errorf("commit acknowledgement: %w", uncertain)
	classified := historyBudgetError(wrapped)
	var retained *historyUncertainError
	if !errors.As(classified, &retained) || retained != uncertain || errors.Is(classified, ErrTaskCleanupDeferred) || historyDeleteStatus(classified) != "failed" {
		t.Fatalf("uncertainty became a safe retry: %T %v", classified, classified)
	}
	if confirmed := historyBudgetError(context.DeadlineExceeded); !errors.Is(confirmed, ErrTaskCleanupDeferred) {
		t.Fatalf("confirmed rollback deadline was not deferred: %v", confirmed)
	}
}
