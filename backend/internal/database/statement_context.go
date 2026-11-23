package database

import (
	"context"
	"sync"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
)

// A statement's successful Close releases timers and cancellation bridges. It
// must not emit a synthetic cancellation to database/sql's Rows.awaitDone.
type statementContext struct {
	context.Context
	mu          sync.Mutex
	err         error
	finished    bool
	at          time.Time
	hasDeadline bool
	cancel      context.CancelCauseFunc
}

func (c *statementContext) Deadline() (time.Time, bool) { return c.at, c.hasDeadline }
func (c *statementContext) Err() error {
	if c.Context.Err() == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *statementContext) cancelFrom(source context.Context) {
	if err := source.Err(); err != nil {
		c.fail(err, context.Cause(source))
	}
}
func (c *statementContext) fail(err, cause error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finished || c.err != nil {
		return
	}
	c.err = err
	c.cancel(cause)
}

func newStatementContext(parent, caller context.Context) (context.Context, context.CancelFunc) {
	at, limited := parent.Deadline()
	if callerAt, ok := caller.Deadline(); ok && (!limited || callerAt.Before(at)) {
		at, limited = callerAt, true
	}
	if persistence.ExecutionOwned(parent) || persistence.ExecutionOwned(caller) {
		budget := persistence.ExecutionTimeout(parent)
		if persistence.ExecutionOwned(caller) {
			if persistence.ExecutionOwned(parent) {
				budget = min(budget, persistence.ExecutionTimeout(caller))
			} else {
				budget = persistence.ExecutionTimeout(caller)
			}
		}
		policyAt := time.Now().Add(budget)
		if !limited || policyAt.Before(at) {
			at, limited = policyAt, true
		}
	}
	base := context.WithoutCancel(transactionQueryContext{parent, caller})
	ctx, cancel := context.WithCancelCause(base)
	statement := &statementContext{Context: ctx, cancel: cancel, at: at, hasDeadline: limited}
	stopParent := context.AfterFunc(parent, func() { statement.cancelFrom(parent) })
	stopCaller := context.AfterFunc(caller, func() { statement.cancelFrom(caller) })
	statement.cancelFrom(parent)
	statement.cancelFrom(caller)
	var timer *time.Timer
	if limited {
		remaining := time.Until(at)
		if remaining <= 0 {
			statement.fail(context.DeadlineExceeded, context.DeadlineExceeded)
		} else {
			timer = time.AfterFunc(remaining, func() { statement.fail(context.DeadlineExceeded, context.DeadlineExceeded) })
		}
	}
	finish := func() {
		// Preserve a real caller cancellation/deadline even if its timer or
		// AfterFunc has not yet been scheduled when physical Close finishes.
		statement.cancelFrom(parent)
		statement.cancelFrom(caller)
		if limited && !time.Now().Before(at) {
			statement.fail(context.DeadlineExceeded, context.DeadlineExceeded)
		}
		statement.mu.Lock()
		statement.finished = true
		statement.mu.Unlock()
		if timer != nil {
			timer.Stop()
		}
		stopParent()
		stopCaller()
	}
	return statement, finish
}
