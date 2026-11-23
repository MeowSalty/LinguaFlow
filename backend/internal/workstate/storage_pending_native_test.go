package workstate

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
	"modernc.org/sqlite"
)

type storageNativeCommitGate struct {
	shorten, block atomic.Bool
	entered        chan struct{}
	release        chan struct{}
	returned       chan error
	closed         chan struct{}
	releaseOnce    sync.Once
}

func (g *storageNativeCommitGate) unblock() { g.releaseOnce.Do(func() { close(g.release) }) }

type storageNativeCommitConnector struct {
	driver.Connector
	gate *storageNativeCommitGate
}

func (c storageNativeCommitConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &storageNativeCommitConn{Conn: conn, gate: c.gate}, nil
}

type storageNativeCommitConn struct {
	driver.Conn
	gate  *storageNativeCommitGate
	owner bool
}

func (c *storageNativeCommitConn) ResetSession(ctx context.Context) error {
	return c.Conn.(driver.SessionResetter).ResetSession(ctx)
}
func (c *storageNativeCommitConn) IsValid() bool { return c.Conn.(driver.Validator).IsValid() }
func (c *storageNativeCommitConn) Ping(ctx context.Context) error {
	return c.Conn.(driver.Pinger).Ping(ctx)
}
func (c *storageNativeCommitConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	return c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, query)
}
func (c *storageNativeCommitConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}
func (c *storageNativeCommitConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}
func (c *storageNativeCommitConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	tx, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return storageNativeCommitTx{Tx: tx, conn: c}, nil
}
func (c *storageNativeCommitConn) Close() error {
	err := c.Conn.Close()
	if c.owner {
		close(c.gate.closed)
	}
	return err
}

type storageNativeCommitTx struct {
	driver.Tx
	conn *storageNativeCommitConn
}

func (tx storageNativeCommitTx) Commit() error {
	if !tx.conn.gate.block.CompareAndSwap(true, false) {
		return tx.Tx.Commit()
	}
	tx.conn.owner = true
	close(tx.conn.gate.entered)
	<-tx.conn.gate.release
	err := tx.Tx.Commit()
	tx.conn.gate.returned <- err
	return err
}

// Only the original candidate transaction receives a short cleanup wall. The
// caller and its authoritative verification retain their own longer budgets.
type storageNativeDeadlineDriver struct {
	*database.TimeDriver
	gate *storageNativeCommitGate
}

func (d storageNativeDeadlineDriver) Tx(ctx context.Context) (dialect.Tx, error) {
	if d.gate.shorten.CompareAndSwap(true, false) {
		ctx = database.WithJoinedCleanupDeadline(ctx, time.Now().Add(time.Second))
	}
	return d.TimeDriver.Tx(ctx)
}

func TestStoragePendingNativeCandidateCommitRetainsChargeUntilWriterBarrier(t *testing.T) {
	ctx := t.Context()
	gate := &storageNativeCommitGate{
		entered: make(chan struct{}), release: make(chan struct{}),
		returned: make(chan error, 1), closed: make(chan struct{}),
	}
	dsn := filepath.ToSlash(filepath.Join(t.TempDir(), "pending-storage-native.db")) +
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	connector, err := sqlite.NewConnector(dsn)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(database.ObserveConnector(storageNativeCommitConnector{Connector: connector, gate: gate}, dialect.SQLite))
	db.SetMaxOpenConns(0)
	client := ent.NewClient(ent.Driver(storageNativeDeadlineDriver{
		TimeDriver: database.NewDriver(entsql.OpenDB(dialect.SQLite, db)), gate: gate,
	}))
	t.Cleanup(func() { gate.unblock(); _ = client.Close(); _ = db.Close() })
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	_, _, store, scope, segment := seedFixture(t, ctx, client)
	budget, owned := memberDeltaBudget(t, ctx, client, scope)
	credit := &storagePendingCreditProbe{budget: budget, remaining: 8 << 20}
	budget.Credit = credit
	owned = persistence.WithExecutionTimeout(owned, 5*time.Second)
	before := budget.Root.Snapshot(budget.Scope.ActorID).Instance.LogicalStorageBytes
	candidate := ready(scope, segment)
	candidate.ParentRequestID = "pending-native-parent"
	candidate.Payload = []byte(`{"version":1,"value":"` + strings.Repeat("p", 256) + `"}`)
	gate.shorten.Store(true)
	gate.block.Store(true)
	result, done := make(chan error, 1), make(chan struct{})
	go func() {
		defer close(done)
		result <- store.SaveCandidate(owned, candidate)
	}()
	t.Cleanup(func() {
		gate.unblock()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("pending candidate invocation did not join fixture cleanup")
		}
	})
	select {
	case <-gate.entered:
	case <-done:
		t.Fatal("candidate never reached native Commit", <-result)
	case <-time.After(3 * time.Second):
		t.Fatal("candidate never entered native Commit")
	}
	consumed := credit.consumed
	if consumed <= 0 {
		t.Fatal("candidate had no pre-dispatch storage charge")
	}
	// The original joined cleanup wall expires while native Commit is held.
	// WAL permits this independent unlimited-pool reader to see old inventory.
	time.Sleep(1100 * time.Millisecond)
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM work_candidates").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 || db.Stats().MaxOpenConnections != 0 {
		t.Fatalf("fixture did not expose pending WAL absence: candidates=%d max_open=%d", count, db.Stats().MaxOpenConnections)
	}
	select {
	case err := <-gate.returned:
		t.Fatal("native Commit was released before the pending audit", err)
	default:
	}
	charged, attached := budget.Root.Usage(budget.Scope, StorageIdentity(scope.JobID))
	if !attached || charged.LogicalStorageBytes <= 0 {
		t.Fatalf("pending native Commit released durable storage from old inventory: attached=%t charge=%+v", attached, charged)
	}
	if current := budget.Root.Snapshot(budget.Scope.ActorID).Instance.LogicalStorageBytes; current != before {
		t.Fatalf("pending native Commit released instance capacity: before=%d current=%d", before, current)
	}
	gate.unblock()
	select {
	case err := <-gate.returned:
		if err != nil {
			t.Fatal("late native Commit failed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("late native Commit did not acknowledge completion")
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal("committed candidate did not resolve its unknown acknowledgement", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("candidate verification did not finish after native acknowledgement")
	}
	select {
	case <-gate.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("quarantined native candidate connection did not finish cleanup")
	}
	actual := memberDeltaAudit(t, ctx, budget)
	if actual <= 0 || budget.pending || budget.pendingConfirmation != nil {
		t.Fatalf("acknowledged noncanonical candidate did not settle: actual=%d pending=%t confirmation=%t", actual, budget.pending, budget.pendingConfirmation != nil)
	}
	if credit.consumed != consumed || credit.refunded != 0 {
		t.Fatalf("late committed candidate changed request credit: consumed=%d expected=%d refunded=%d", credit.consumed, consumed, credit.refunded)
	}
	if err := budget.Settle(ctx); err != nil {
		t.Fatal(err)
	}
	if duplicate := memberDeltaAudit(t, ctx, budget); duplicate != actual {
		t.Fatal("repeated settlement changed acknowledged storage")
	}
	if credit.consumed != consumed || credit.refunded != 0 {
		t.Fatal("repeated settlement duplicated receipt movement")
	}
}
