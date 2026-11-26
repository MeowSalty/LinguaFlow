package service

import (
	"context"
	stdsql "database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

// This connector keeps the real database/sql BeginTx, cancellation goroutine,
// connection pool and joined rollback path. Blocking is inside driver calls,
// where an unbounded context would reproduce the lock-wait failure.
type syncBudgetConnector struct {
	mu                                sync.Mutex
	block                             string
	active                            bool
	conflictOnce                      bool
	commitError                       error
	begins                            []syncBudgetBegin
	queries                           []time.Time
	rollbackEntered, rollbackRelease  chan struct{}
	rollbackFinished                  chan struct{}
	queryEntered                      chan struct{}
	queryOnce, rollbackOnce, doneOnce sync.Once
}

type syncBudgetBegin struct {
	deadline  time.Time
	isolation driver.IsolationLevel
}

type syncBudgetSQLDriver struct{}
type syncBudgetConn struct{ owner *syncBudgetConnector }
type syncBudgetTx struct{ owner *syncBudgetConnector }
type syncBudgetRows struct{}

func (syncBudgetSQLDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }
func (c *syncBudgetConnector) Driver() driver.Driver         { return syncBudgetSQLDriver{} }
func (c *syncBudgetConnector) Connect(context.Context) (driver.Conn, error) {
	return &syncBudgetConn{owner: c}, nil
}
func (*syncBudgetConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected Prepare")
}
func (*syncBudgetConn) Close() error { return nil }
func (c *syncBudgetConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *syncBudgetConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	deadline, _ := ctx.Deadline()
	c.owner.mu.Lock()
	c.owner.begins = append(c.owner.begins, syncBudgetBegin{deadline: deadline, isolation: opts.Isolation})
	block := c.owner.block == "begin"
	if !block {
		c.owner.active = true
	}
	c.owner.mu.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &syncBudgetTx{owner: c.owner}, nil
}
func (c *syncBudgetConn) QueryContext(ctx context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	deadline, _ := ctx.Deadline()
	c.owner.mu.Lock()
	active := c.owner.active
	if active {
		c.owner.queries = append(c.owner.queries, deadline)
	}
	conflict := active && c.owner.conflictOnce
	if conflict {
		c.owner.conflictOnce = false
	}
	block := c.owner.block == "discovery" || active && c.owner.block == "query"
	c.owner.mu.Unlock()
	if active && c.owner.queryEntered != nil {
		c.owner.queryOnce.Do(func() { close(c.owner.queryEntered) })
	}
	if conflict {
		return nil, &pgconn.PgError{Code: "40001", Message: "injected known pre-commit conflict"}
	}
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return syncBudgetRows{}, nil
}
func (syncBudgetRows) Columns() []string         { return []string{"id"} }
func (syncBudgetRows) Close() error              { return nil }
func (syncBudgetRows) Next([]driver.Value) error { return io.EOF }
func (t *syncBudgetTx) Commit() error {
	t.owner.mu.Lock()
	defer t.owner.mu.Unlock()
	t.owner.active = false
	return t.owner.commitError
}
func (t *syncBudgetTx) Rollback() error {
	t.owner.mu.Lock()
	t.owner.active = false
	t.owner.mu.Unlock()
	if t.owner.rollbackEntered != nil {
		t.owner.rollbackOnce.Do(func() { close(t.owner.rollbackEntered) })
	}
	if t.owner.rollbackRelease != nil {
		<-t.owner.rollbackRelease
	}
	if t.owner.rollbackFinished != nil {
		t.owner.doneOnce.Do(func() { close(t.owner.rollbackFinished) })
	}
	return nil
}

func syncBudgetService(t *testing.T, connector *syncBudgetConnector) (*GlossarySyncService, *stdsql.DB) {
	t.Helper()
	db := stdsql.OpenDB(database.ObserveConnector(connector, dialect.Postgres))
	db.SetMaxOpenConns(1)
	client := ent.NewClient(ent.Driver(database.NewDriver(entsql.OpenDB(dialect.Postgres, db))))
	t.Cleanup(func() { _ = client.Close() })
	return NewGlossarySyncService(client, nil, nil, nil, discardLogger()), db
}

func TestGlossarySyncSnapshotDriverPolicyInterruptsBlockedQuery(t *testing.T) {
	connector := &syncBudgetConnector{block: "query", rollbackFinished: make(chan struct{})}
	svc, _ := syncBudgetService(t, connector)
	started := time.Now()
	attempts := 0
	err := svc.withSyncSnapshotTransaction(context.Background(), func(tx *ent.Client) error {
		attempts++
		_, err := tx.User.Query().IDs(context.Background())
		return err
	})
	if !errors.Is(err, context.DeadlineExceeded) || attempts != 1 || time.Since(started) > organizationTransactionRetryBudget+time.Second {
		t.Fatalf("driver lock wait escaped policy budget: attempts=%d elapsed=%v err=%v", attempts, time.Since(started), err)
	}
	select {
	case <-connector.rollbackFinished:
	case <-time.After(time.Second):
		t.Fatal("owned rollback did not finish after deadline")
	}
}

func TestGlossarySyncSnapshotDriverCallerDeadlineReachesDiscoveryBeginAndSQL(t *testing.T) {
	for _, phase := range []string{"discovery", "begin", "query"} {
		t.Run(phase, func(t *testing.T) {
			connector := &syncBudgetConnector{block: phase}
			svc, _ := syncBudgetService(t, connector)
			ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
			defer cancel()
			deadline, _ := ctx.Deadline()
			started := time.Now()
			err := svc.withSyncSnapshotTransaction(ctx, func(tx *ent.Client) error {
				_, err := tx.User.Query().IDs(context.Background())
				return err
			})
			if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
				t.Fatalf("blocked %s escaped caller deadline: elapsed=%v err=%v", phase, time.Since(started), err)
			}
			connector.mu.Lock()
			defer connector.mu.Unlock()
			for _, begin := range connector.begins {
				if !begin.deadline.Equal(deadline) || begin.isolation != driver.IsolationLevel(stdsql.LevelRepeatableRead) {
					t.Fatalf("BeginTx deadline/options changed: %+v want=%v", begin, deadline)
				}
			}
			for _, observed := range connector.queries {
				if !observed.Equal(deadline) {
					t.Fatalf("captured callback context lost caller deadline: %v want=%v", observed, deadline)
				}
			}
		})
	}
}

func TestGlossarySyncSnapshotDriverRetriesShareAbsoluteDeadline(t *testing.T) {
	connector := &syncBudgetConnector{block: "query", conflictOnce: true}
	svc, _ := syncBudgetService(t, connector)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	deadline, _ := ctx.Deadline()
	attempts := 0
	err := svc.withSyncSnapshotTransaction(ctx, func(tx *ent.Client) error {
		attempts++
		_, err := tx.User.Query().IDs(context.Background())
		return err
	})
	if !errors.Is(err, context.DeadlineExceeded) || attempts != 2 {
		t.Fatalf("retry/cutoff: attempts=%d err=%v", attempts, err)
	}
	connector.mu.Lock()
	defer connector.mu.Unlock()
	for _, begin := range connector.begins {
		if !begin.deadline.Equal(deadline) {
			t.Fatalf("retry reset total budget: %v want=%v", begin.deadline, deadline)
		}
	}
}

func TestGlossarySyncSnapshotDriverCancellationJoinsRollback(t *testing.T) {
	connector := &syncBudgetConnector{block: "query", queryEntered: make(chan struct{}), rollbackEntered: make(chan struct{}), rollbackRelease: make(chan struct{}), rollbackFinished: make(chan struct{})}
	svc, db := syncBudgetService(t, connector)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(connector.rollbackRelease) }) }
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- svc.withSyncSnapshotTransaction(ctx, func(tx *ent.Client) error { _, err := tx.User.Query().IDs(context.Background()); return err })
	}()
	select {
	case <-connector.queryEntered:
	case <-time.After(time.Second):
		t.Fatal("query did not reach driver")
	}
	cancel()
	select {
	case <-connector.rollbackEntered:
	case <-time.After(time.Second):
		t.Fatal("driver cancellation rollback did not start")
	}
	select {
	case err := <-result:
		t.Fatalf("returned with rollback still blocked: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if db.Stats().InUse != 1 {
		t.Fatal("uncertain rollback connection escaped ownership")
	}
	release()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error lost: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("joined rollback did not settle after release")
	}
	if db.Stats().InUse != 0 {
		t.Fatal("settled rollback retained connection")
	}
}

func TestGlossarySyncSnapshotDriverUnknownCommitIsNotRetried(t *testing.T) {
	connector := &syncBudgetConnector{commitError: &pgconn.PgError{Code: "40001", Message: "commit acknowledgement unavailable"}}
	svc, _ := syncBudgetService(t, connector)
	attempts := 0
	err := svc.withSyncSnapshotTransaction(context.Background(), func(*ent.Client) error { attempts++; return nil })
	var commitErr *organizationCommitError
	if !errors.As(err, &commitErr) || attempts != 1 {
		t.Fatalf("unknown commit replayed: attempts=%d err=%v", attempts, err)
	}
}
