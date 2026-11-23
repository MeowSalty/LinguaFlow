package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
)

type joinedRollbackKey struct{}
type joinedCleanupDeadlineKey struct{}

const joinedCleanupTimeout = 5 * time.Second

var ErrTransactionCleanupTimeout = errors.New("transaction cleanup did not finish within its independent budget; connection remains reserved until cleanup finishes")

// WithJoinedRollback asks BeginTx to reserve a connection until rollback has
// actually finished. database/sql may otherwise return ErrTxDone while its
// cancellation goroutine is still rolling the transaction back in the driver.
func WithJoinedRollback(ctx context.Context) context.Context {
	return context.WithValue(ctx, joinedRollbackKey{}, true)
}

// WithJoinedCleanupDeadline caps cleanup by a caller's overall wall-clock
// budget. It does not replace the SQL execution deadline, and nested callers
// can only shorten it. Cleanup still runs independently of cancellation.
func WithJoinedCleanupDeadline(ctx context.Context, at time.Time) context.Context {
	if previous, ok := ctx.Value(joinedCleanupDeadlineKey{}).(time.Time); ok && !previous.IsZero() && (at.IsZero() || previous.Before(at)) {
		at = previous
	}
	return context.WithValue(ctx, joinedCleanupDeadlineKey{}, at)
}

func JoinedCleanupDeadline(ctx context.Context) (time.Time, bool) {
	at, ok := ctx.Value(joinedCleanupDeadlineKey{}).(time.Time)
	return at, ok && !at.IsZero()
}

func beginJoinedTransaction(ctx context.Context, db *sql.DB, opts *sql.TxOptions) (dialect.Tx, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := conn.BeginTx(ctx, opts)
	if err != nil {
		return nil, errors.Join(err, conn.Close())
	}
	var discard func()
	var driverFinished <-chan struct{}
	var driverResult func() error
	var physicalClosed <-chan struct{}
	var physicalResult func() error
	var isDiscarded func() bool
	retainsOnCancellation := false
	_ = conn.Raw(func(raw any) error {
		if marker, ok := raw.(interface{ discard() }); ok {
			discard = marker.discard
		}
		_, resets := raw.(driver.SessionResetter)
		_, validates := raw.(driver.Validator)
		retainsOnCancellation = resets && validates
		if status, ok := raw.(interface {
			transactionCleanup() (<-chan struct{}, func() error)
		}); ok {
			driverFinished, driverResult = status.transactionCleanup()
		}
		if status, ok := raw.(interface {
			physicalCleanup() (<-chan struct{}, func() error, func() bool)
		}); ok {
			physicalClosed, physicalResult, isDiscarded = status.physicalCleanup()
		}
		return nil
	})
	wallAt, _ := ctx.Value(joinedCleanupDeadlineKey{}).(time.Time)
	return &joinedTransaction{Tx: &entsql.Tx{Conn: entsql.Conn{ExecQuerier: tx}, Tx: tx}, conn: conn, discard: discard, done: make(chan struct{}), wallDeadline: wallAt, retainsOnCancellation: retainsOnCancellation, driverFinished: driverFinished, driverResult: driverResult, physicalClosed: physicalClosed, physicalResult: physicalResult, isDiscarded: isDiscarded}, nil
}

type joinedTransaction struct {
	*entsql.Tx
	conn                  *sql.Conn
	once                  sync.Once
	closeErr              error
	discard               func()
	done                  chan struct{}
	cleanupDeadline       time.Time
	wallDeadline          time.Time
	discardRequested      atomic.Bool
	retainsOnCancellation bool
	driverFinished        <-chan struct{}
	driverResult          func() error
	physicalClosed        <-chan struct{}
	physicalResult        func() error
	isDiscarded           func() bool
}

func (t *joinedTransaction) finish(action func() error) error {
	t.once.Do(func() {
		t.cleanupDeadline = time.Now().Add(joinedCleanupTimeout)
		if !t.wallDeadline.IsZero() && t.wallDeadline.Before(t.cleanupDeadline) {
			t.cleanupDeadline = t.wallDeadline
		}
		go func() {
			err := action()
			uncertain := err != nil
			if t.driverFinished != nil {
				<-t.driverFinished
				actual := t.driverResult()
				if errors.Is(err, sql.ErrTxDone) {
					uncertain = actual != nil
				} else {
					uncertain = err != nil || actual != nil
				}
				err = errors.Join(err, actual)
			}
			if uncertain || t.discardRequested.Load() {
				if t.discard != nil {
					t.discard()
				}
				// Once the driver's rollback releases its mutex, discard the
				// physical connection even for unwrapped test/maintenance pools.
				// ErrTxDone means database/sql's cancellation rollback owns
				// release. Let Conn.Close join it directly; a competing Raw
				// discard can mark Conn done before its pool release finishes.
				if !errors.Is(err, sql.ErrTxDone) || t.retainsOnCancellation {
					_ = t.conn.Raw(func(any) error { return driver.ErrBadConn })
				}
			}
			t.closeErr = errors.Join(err, t.conn.Close())
			if t.physicalClosed != nil && t.isDiscarded != nil && t.isDiscarded() {
				<-t.physicalClosed
				t.closeErr = errors.Join(t.closeErr, t.physicalResult())
			}
			close(t.done)
		}()
	})
	// Conn.Close joins database/sql's cancellation rollback. A stuck driver
	// cannot block the caller forever or return its uncertain connection to
	// the pool: the cleanup retains ownership until discard actually finishes.
	select {
	case <-t.done:
		return t.closeErr
	default:
	}
	remaining := time.Until(t.cleanupDeadline)
	if remaining <= 0 {
		t.requestDiscardIfUncertain()
		return ErrTransactionCleanupTimeout
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-t.done:
		return t.closeErr
	case <-timer.C:
		t.requestDiscardIfUncertain()
		return ErrTransactionCleanupTimeout
	}
}

func (t *joinedTransaction) requestDiscardIfUncertain() {
	if t.driverFinished != nil {
		select {
		case <-t.driverFinished:
			if t.driverResult() == nil {
				return
			}
		default:
		}
	}
	t.discardRequested.Store(true)
	if t.discard != nil {
		t.discard()
	}
}
func (t *joinedTransaction) Commit() error { return t.finish(t.Tx.Commit) }
func (t *joinedTransaction) Rollback() error {
	return t.finish(t.Tx.Rollback)
}
