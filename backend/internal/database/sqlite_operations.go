package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// ErrSQLiteOperationPending means cancellation returned before the native
// operation acknowledged completion. Its result is unknown to the caller; the
// connection is quarantined, and neither database/sql nor callers may replay it.
var ErrSQLiteOperationPending = errors.New("SQLite operation cancellation returned before native completion; result unknown and connection quarantined")
var ErrSQLiteConnectionDiscarded = errors.New("SQLite connection quarantined; transaction cannot commit")

type sqliteOperation struct {
	mu                                 sync.Mutex
	finished, abandoned, cleanup, stop bool
	work                               func() (any, error)
	discard                            func(any)
	value                              any
	err                                error
	done                               chan struct{}
}

type sqliteOperations struct {
	jobs           chan *sqliteOperation
	closed         chan struct{}
	closeOnce      sync.Once
	closeErr       error
	nativeCloseErr error
	closing        atomic.Bool
	rows           map[*observedRows]struct{}
	statements     map[*observedStmt]struct{}
}

// A logical database/sql discard must not release the configured physical
// connection limit while native work still owns that connection.
type sqlitePoolOperations struct {
	db               *sql.DB
	mu               sync.Mutex
	changed          chan struct{}
	live             int
	owners           map[*observedConn]struct{}
	err              error
	logicalCloseOnce sync.Once
	logicalCloseDone chan struct{}
	logicalCloseErr  error
}

var sqlitePools sync.Map // *sql.DB -> *sqlitePoolOperations

func (p *sqlitePoolOperations) acquire(ctx context.Context) error {
	for {
		p.mu.Lock()
		maximum := p.db.Stats().MaxOpenConnections
		if maximum == 0 || p.live < maximum {
			p.live++
			p.mu.Unlock()
			return nil
		}
		changed := p.changed
		p.mu.Unlock()
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-changed:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		timer.Stop()
	}
}

func (p *sqlitePoolOperations) own(c *observedConn) {
	p.mu.Lock()
	p.owners[c] = struct{}{}
	p.mu.Unlock()
}

func (p *sqlitePoolOperations) release(c *observedConn, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = errors.Join(p.err, err)
	if c.sqlite.nativeCloseErr == nil {
		delete(p.owners, c)
		p.live--
	}
	close(p.changed)
	p.changed = make(chan struct{})
}

// CloseSQLitePool preserves database/sql's ownership of healthy borrowed
// connections, and joins closing/quarantined native work. A timeout reports
// unfinished ownership rather than treating InUse=0 as physical cleanup. It is
// safe to call again to finish an incomplete join.
func CloseSQLitePool(ctx context.Context, db *sql.DB) error {
	value, ok := sqlitePools.Load(db)
	if !ok {
		return db.Close()
	}
	p := value.(*sqlitePoolOperations)
	p.logicalCloseOnce.Do(func() {
		p.logicalCloseDone = make(chan struct{})
		go func() {
			p.logicalCloseErr = db.Close()
			close(p.logicalCloseDone)
		}()
	})
	select {
	case <-p.logicalCloseDone:
	case <-ctx.Done():
		return errors.Join(ErrTransactionCleanupTimeout, ctx.Err())
	}
	err := p.logicalCloseErr
	for {
		p.mu.Lock()
		cleanupErr, changed := p.err, p.changed
		pending, failed := 0, 0
		var forceClose []*observedConn
		for owner := range p.owners {
			if owner.bad.Load() && !owner.sqlite.closing.Load() {
				forceClose = append(forceClose, owner)
			}
			if owner.bad.Load() || owner.sqlite.closing.Load() {
				pending++
				select {
				case <-owner.sqlite.closed:
					failed++
				default:
				}
			}
		}
		p.mu.Unlock()
		for _, owner := range forceClose {
			_ = owner.Close()
		}
		if pending == 0 {
			sqlitePools.Delete(db)
			return errors.Join(err, cleanupErr)
		}
		if pending == failed {
			return errors.Join(err, cleanupErr, ErrSQLiteConnectionDiscarded)
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return errors.Join(err, cleanupErr, ErrTransactionCleanupTimeout, ctx.Err())
		}
	}
}

func (c *observedConn) startSQLiteOperations() {
	c.sqlite = &sqliteOperations{jobs: make(chan *sqliteOperation), closed: make(chan struct{}), rows: make(map[*observedRows]struct{}), statements: make(map[*observedStmt]struct{})}
	go func() {
		for job := range c.sqlite.jobs {
			var value any
			var err error
			if c.bad.Load() && !job.cleanup {
				err = ErrSQLiteConnectionDiscarded
			} else {
				value, err = job.work()
			}
			job.mu.Lock()
			if job.abandoned && job.discard != nil && value != nil {
				job.discard(value)
			}
			job.value, job.err, job.finished = value, err, true
			close(job.done)
			job.mu.Unlock()
			if job.stop {
				c.sqlite.closeErr = err
				if c.pool != nil {
					c.pool.release(c, err)
				}
				close(c.sqlite.closed)
				return
			}
		}
	}()
}

func sqliteCall[T any](c *observedConn, ctx context.Context, cleanup bool, work func() (T, error), discard func(T)) (T, error) {
	var zero T
	if c.sqlite == nil {
		return work()
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	job := &sqliteOperation{done: make(chan struct{}), cleanup: cleanup, work: func() (any, error) { return work() }}
	if discard != nil {
		job.discard = func(value any) { discard(value.(T)) }
	}
	select {
	case c.sqlite.jobs <- job:
	case <-ctx.Done():
		return zero, ctx.Err()
	case <-c.sqlite.closed:
		return zero, ErrSQLiteConnectionDiscarded
	}
	select {
	case <-job.done:
	case <-ctx.Done():
		job.mu.Lock()
		if !job.finished {
			job.abandoned = true
			c.discard()
			job.mu.Unlock()
			return zero, errors.Join(ctx.Err(), ErrSQLiteOperationPending)
		}
		job.mu.Unlock()
	}
	if job.value == nil {
		return zero, job.err
	}
	return job.value.(T), job.err
}

func (c *observedConn) cleanupSQLite(work func() error) error {
	if c.sqlite == nil {
		return work()
	}
	if c.bad.Load() {
		// database/sql closes rows and prepared statements synchronously after
		// Next fails. Waiting here would undo the caller's bounded return.
		go func() {
			_, _ = sqliteCall(c, context.Background(), true, func() (struct{}, error) { return struct{}{}, work() }, nil)
		}()
		return nil
	}
	_, err := sqliteCall(c, context.Background(), true, func() (struct{}, error) { return struct{}{}, work() }, nil)
	return err
}

func (c *observedConn) Close() error {
	if c.sqlite == nil {
		return c.Conn.Close()
	}
	c.sqlite.closeOnce.Do(func() {
		c.sqlite.closing.Store(true)
		go func() {
			job := &sqliteOperation{cleanup: true, stop: true, done: make(chan struct{}), work: func() (any, error) {
				var err error
				for rows := range c.sqlite.rows {
					err = errors.Join(err, rows.closeNative())
				}
				for stmt := range c.sqlite.statements {
					err = errors.Join(err, stmt.closeNative())
				}
				if c.activeTx != nil {
					err = errors.Join(err, c.activeTx.rollbackNative())
				}
				if c.Conn != nil {
					c.sqlite.nativeCloseErr = c.Conn.Close()
					err = errors.Join(err, c.sqlite.nativeCloseErr)
				}
				return nil, err
			}}
			c.sqlite.jobs <- job
		}()
	})
	if c.bad.Load() {
		return nil
	}
	<-c.sqlite.closed
	return c.sqlite.closeErr
}

func cloneNamedValues(args []driver.NamedValue) []driver.NamedValue {
	copyArgs := append([]driver.NamedValue(nil), args...)
	for i := range copyArgs {
		if value, ok := copyArgs[i].Value.([]byte); ok {
			copyArgs[i].Value = append([]byte(nil), value...)
		}
	}
	return copyArgs
}

func cloneDriverValues(args []driver.Value) []driver.Value {
	copyArgs := append([]driver.Value(nil), args...)
	for i := range copyArgs {
		if value, ok := copyArgs[i].([]byte); ok {
			copyArgs[i] = append([]byte(nil), value...)
		}
	}
	return copyArgs
}
