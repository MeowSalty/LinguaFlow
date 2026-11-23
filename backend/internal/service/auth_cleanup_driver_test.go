package service

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/externalidentity"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/systemsetting"
	"modernc.org/sqlite"
)

// Wrap the real SQLite driver: rollback releases the database writer lock but
// its acknowledgement is held back. This isolates slow cleanup from lock busy.
type authenticationCleanupConnector struct {
	dsn                         string
	armed                       atomic.Bool
	entered, release, discarded chan struct{}
	releaseOnce, discardOnce    sync.Once
}

func (c *authenticationCleanupConnector) Driver() driver.Driver { return &sqlite.Driver{} }
func (c *authenticationCleanupConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.Driver().Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &authenticationCleanupConn{Conn: conn, owner: c}, nil
}

type authenticationCleanupConn struct {
	driver.Conn
	owner *authenticationCleanupConnector
	held  atomic.Bool
}

func (c *authenticationCleanupConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	var tx driver.Tx
	var err error
	if beginner, ok := c.Conn.(driver.ConnBeginTx); ok {
		tx, err = beginner.BeginTx(ctx, opts)
	} else {
		tx, err = c.Conn.Begin()
	}
	if err != nil {
		return nil, err
	}
	return &authenticationCleanupTx{Tx: tx, conn: c}, nil
}
func (c *authenticationCleanupConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if exec, ok := c.Conn.(driver.ExecerContext); ok {
		return exec.ExecContext(ctx, query, args)
	}
	return nil, driver.ErrSkip
}
func (c *authenticationCleanupConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if reader, ok := c.Conn.(driver.QueryerContext); ok {
		return reader.QueryContext(ctx, query, args)
	}
	return nil, driver.ErrSkip
}
func (c *authenticationCleanupConn) Close() error {
	err := c.Conn.Close()
	if c.held.Load() {
		c.owner.discardOnce.Do(func() { close(c.owner.discarded) })
	}
	return err
}

type authenticationCleanupTx struct {
	driver.Tx
	conn *authenticationCleanupConn
}

func (tx *authenticationCleanupTx) Rollback() error {
	err := tx.Tx.Rollback()
	if tx.conn.owner.armed.CompareAndSwap(true, false) {
		tx.conn.held.Store(true)
		close(tx.conn.owner.entered)
		<-tx.conn.owner.release
	}
	return err
}

func TestAuthenticationCleanupSlowDriverRollbackReservesOtherCategories(t *testing.T) {
	ctx, now := context.Background(), time.Now().UTC()
	connector := &authenticationCleanupConnector{
		dsn:     filepath.ToSlash(filepath.Join(t.TempDir(), "auth-cleanup.db")) + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(0)",
		entered: make(chan struct{}), release: make(chan struct{}), discarded: make(chan struct{}),
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(2)
	c := ent.NewClient(ent.Driver(database.NewDriver(entsql.OpenDB(dialect.SQLite, db))))
	t.Cleanup(func() { _ = c.Close() })
	t.Cleanup(func() { connector.releaseOnce.Do(func() { close(connector.release) }) })
	if err := c.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	if err := initializePasswordConnection(ctx, c); err != nil {
		t.Fatal(err)
	}
	if !c.SystemSetting.Query().Where(systemsetting.KeyEQ(SettingRegistrationEnabled)).ExistX(ctx) {
		c.SystemSetting.Create().SetKey(SettingRegistrationEnabled).SetValue("true").SaveX(ctx)
	}
	seedExternalFlowConnection(t, c)
	owner := createTestUser(t, c, "slow-cleanup-owner")
	identity := c.ExternalIdentity.Create().SetUserID(owner.ID).SetProtocol(externalidentity.ProtocolOidc).
		SetAuthority("https://issuer.example.test").SetSubject("slow-cleanup").SaveX(ctx)
	c.LoginBinding.Create().SetUserID(owner.ID).SetConnectionID("flow-oidc").SetExternalIdentityID(identity.ID).
		SetEvidence(authrules.Evidence{Subject: "expired", ExpiresAt: now.Add(-time.Second)}).SaveX(ctx)
	proofID := cleanupTestProof(t, c, owner, now)
	cause := errors.New("abort binding cleanup")
	var fail atomic.Bool
	fail.Store(true)
	c.LoginBinding.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
			if fail.Swap(false) {
				return nil, cause
			}
			return next.Mutate(ctx, mutation)
		})
	})
	connector.armed.Store(true)
	s := &ExternalAuthService{client: c, now: func() time.Time { return now }}
	started := time.Now()
	err := s.CleanupExpired(ctx)
	var uncertain *organizationRollbackError
	if !errors.As(err, &uncertain) || !errors.Is(uncertain.rollback, database.ErrTransactionCleanupTimeout) {
		t.Fatalf("slow cleanup lost uncertainty: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("one category exceeded its wall quota: %v", elapsed)
	}
	select {
	case <-connector.entered:
	default:
		t.Fatal("test did not enter real driver rollback")
	}
	if _, err := c.ReauthenticationProof.Get(ctx, proofID); !ent.IsNotFound(err) {
		t.Fatalf("slow driver cleanup starved proof category: %v", err)
	}
	if stats := db.Stats(); stats.InUse != 1 {
		t.Fatalf("uncertain connection returned to pool before cleanup finished: %+v", stats)
	}
	connector.releaseOnce.Do(func() { close(connector.release) })
	select {
	case <-connector.discarded:
	case <-time.After(time.Second):
		t.Fatal("timed-out cleanup connection was not physically discarded after acknowledgement")
	}
}
