// Package persistence owns bounded local saving, independently of model retries.
package persistence

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

type Class uint8

const (
	Normal Class = iota
	Reliable
)

type Metadata struct {
	Operation         string
	Class             Class
	JobID, ResourceID int
	RequestID         string
	CandidateID       string
}
type Policy struct {
	NormalQueue, ReliableQueue, ReliableBurst, MaxAttempts int
	ExecutionTimeout, RetryBudget, RetryDelay              time.Duration
}

func DefaultPolicy() Policy {
	return Policy{16, 32, 4, 4, 30 * time.Second, 120 * time.Second, 100 * time.Millisecond}
}

type Saver interface {
	Save(context.Context, Metadata, func(context.Context) error) error
}
type retryOwnerKey struct{}

func WithRetryOwner(ctx context.Context) context.Context {
	return context.WithValue(ctx, retryOwnerKey{}, true)
}
func RetryOwned(ctx context.Context) bool { v, _ := ctx.Value(retryOwnerKey{}).(bool); return v }

type deadlineKey struct{}

func WithDeadline(ctx context.Context, at time.Time) context.Context {
	if previous := Deadline(ctx); !previous.IsZero() && (at.IsZero() || previous.Before(at)) {
		at = previous
	}
	return context.WithValue(ctx, deadlineKey{}, at)
}
func Deadline(ctx context.Context) time.Time {
	at, _ := ctx.Value(deadlineKey{}).(time.Time)
	return at
}

type lifetimeKey struct{}

func WithLifetime(ctx, lifetime context.Context) context.Context {
	return context.WithValue(ctx, lifetimeKey{}, lifetime)
}
func Lifetime(ctx context.Context) context.Context {
	v, _ := ctx.Value(lifetimeKey{}).(context.Context)
	if v != nil {
		return v
	}
	return context.WithoutCancel(ctx)
}

type executionTimeoutKey struct{}

func WithExecutionTimeout(ctx context.Context, d time.Duration) context.Context {
	return context.WithValue(ctx, executionTimeoutKey{}, d)
}
func ExecutionTimeout(ctx context.Context) time.Duration {
	d, _ := ctx.Value(executionTimeoutKey{}).(time.Duration)
	if d <= 0 {
		d = 30 * time.Second
	}
	return d
}
func ExecutionOwned(ctx context.Context) bool {
	d, _ := ctx.Value(executionTimeoutKey{}).(time.Duration)
	return d > 0
}

// Resolver verifies a fixed identity after an uncertain commit. It must not
// interpret an expired-context lookup as evidence that the write did not happen.
type Resolver interface{ Resolve(context.Context) error }
type permanent interface{ Permanent() bool }
type waiter struct {
	ready   chan struct{}
	granted bool
}
type Snapshot struct {
	Aggregates                           []Aggregate
	NormalQueued, ReliableQueued         int
	PeakNormalQueued, PeakReliableQueued int
	Executing                            bool
	Saves, Retries, Failures             uint64
	QueueDuration, ExecutionDuration     time.Duration
}
type Scheduler struct {
	aggregates        map[aggregateKey]Aggregate
	mu                sync.Mutex
	policy            Policy
	logger            *slog.Logger
	normal, reliable  []*waiter
	executing, closed bool
	burst             int
	changed, done     chan struct{}
	stats             Snapshot
}

var ErrClosed = errors.New("save scheduler closed")

type aggregateKey struct {
	operation        string
	class            Class
	dialect, outcome string
}
type Aggregate struct {
	Operation                                     string
	Class                                         Class
	Dialect, Outcome                              string
	Attempts, SQLCalls                            uint64
	QueueDuration, ExecutionDuration, SQLDuration time.Duration
}

func aggregateOperation(op string) string {
	switch op {
	case "reserve", "seal", "handoff", "checkpoint", "stage_initialize", "pool_advance", "abort", "sent", "received", "failed", "completed", "candidate", "commit", "retire":
		return op
	default:
		return "other"
	}
}
func aggregateOutcome(err error) string {
	if err == nil {
		return "success"
	}
	var u interface{ Uncertain() bool }
	if errors.As(err, &u) && u.Uncertain() {
		return "unknown"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	return "failed"
}

func New(policy Policy, logger *slog.Logger) *Scheduler {
	d := DefaultPolicy()
	if policy.NormalQueue <= 0 {
		policy.NormalQueue = d.NormalQueue
	}
	if policy.ReliableQueue <= 0 {
		policy.ReliableQueue = d.ReliableQueue
	}
	if policy.ReliableBurst <= 0 {
		policy.ReliableBurst = d.ReliableBurst
	}
	if policy.MaxAttempts <= 0 {
		policy.MaxAttempts = d.MaxAttempts
	}
	if policy.ExecutionTimeout <= 0 {
		policy.ExecutionTimeout = d.ExecutionTimeout
	}
	if policy.RetryBudget <= 0 {
		policy.RetryBudget = d.RetryBudget
	}
	if policy.RetryDelay <= 0 {
		policy.RetryDelay = d.RetryDelay
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{policy: policy, logger: logger, changed: make(chan struct{}), done: make(chan struct{}), aggregates: map[aggregateKey]Aggregate{}}
}
func (s *Scheduler) signal() { close(s.changed); s.changed = make(chan struct{}) }
func (s *Scheduler) grant() {
	if s.executing || s.closed {
		return
	}
	var w *waiter
	if len(s.reliable) > 0 && (s.burst < s.policy.ReliableBurst || len(s.normal) == 0) {
		w = s.reliable[0]
		s.reliable = s.reliable[1:]
		s.burst++
	} else if len(s.normal) > 0 {
		w = s.normal[0]
		s.normal = s.normal[1:]
		s.burst = 0
	}
	if w != nil {
		s.executing = true
		w.granted = true
		close(w.ready)
	}
	s.signal()
}
func (s *Scheduler) acquire(ctx context.Context, class Class) (func(), error) {
	w := &waiter{ready: make(chan struct{})}
	s.mu.Lock()
	for {
		if s.closed {
			s.mu.Unlock()
			return nil, ErrClosed
		}
		if err := ctx.Err(); err != nil {
			s.mu.Unlock()
			return nil, err
		}
		q := &s.normal
		cap := s.policy.NormalQueue
		if class == Reliable {
			q = &s.reliable
			cap = s.policy.ReliableQueue
		}
		if len(*q) < cap {
			*q = append(*q, w)
			s.stats.PeakNormalQueued = max(s.stats.PeakNormalQueued, len(s.normal))
			s.stats.PeakReliableQueued = max(s.stats.PeakReliableQueued, len(s.reliable))
			s.grant()
			break
		}
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
		s.mu.Lock()
	}
	s.mu.Unlock()
	select {
	case <-w.ready:
	case <-ctx.Done():
	case <-s.done:
	}
	s.mu.Lock()
	if !w.granted {
		for _, q := range []*[]*waiter{&s.normal, &s.reliable} {
			for i, v := range *q {
				if v == w {
					*q = append((*q)[:i], (*q)[i+1:]...)
					break
				}
			}
		}
		s.signal()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return nil, ErrClosed
		}
		return nil, ctx.Err()
	}
	s.mu.Unlock()
	var once sync.Once
	release := func() { once.Do(func() { s.mu.Lock(); s.executing = false; s.grant(); s.mu.Unlock() }) }
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	return release, nil
}
func (s *Scheduler) Save(ctx context.Context, meta Metadata, fn func(context.Context) error) error {
	ctx = WithMetadata(ctx, meta)
	deadline := Deadline(ctx)
	var last error
	for attempt := 0; attempt < s.policy.MaxAttempts; attempt++ {
		queuedAt := time.Now()
		waitCtx := ctx
		cancel := func() {}
		if !deadline.IsZero() {
			waitCtx, cancel = context.WithDeadline(ctx, deadline)
		}
		release, err := s.acquire(waitCtx, meta.Class)
		cancel()
		if err != nil {
			return errors.Join(last, err)
		}
		granted := time.Now()
		if deadline.IsZero() {
			deadline = granted.Add(s.policy.RetryBudget)
		}
		execCtx, cancelExec := context.WithDeadline(WithExecutionTimeout(WithRetryOwner(WithDeadline(ctx, deadline)), s.policy.ExecutionTimeout), deadline)
		collector := &Collector{}
		execCtx = WithObserver(execCtx, Fanout(ObserverFromContext(execCtx), collector))
		ObserveTransaction(ctx, "save_queue", "", granted.Sub(queuedAt), nil)
		err = fn(execCtx)
		cancelExec()
		duration := time.Since(granted)
		release()
		ObserveTransaction(ctx, "save_execution", "", duration, err)
		s.mu.Lock()
		s.stats.Saves++
		s.stats.QueueDuration += granted.Sub(queuedAt)
		s.stats.ExecutionDuration += duration
		if attempt > 0 {
			s.stats.Retries++
		}
		sql := collector.Snapshot()
		dialect := sql.Dialect
		if dialect == "" {
			dialect = "none"
		}
		if dialect != "sqlite3" && dialect != "postgres" && dialect != "none" {
			dialect = "other"
		}
		key := aggregateKey{aggregateOperation(meta.Operation), meta.Class, dialect, aggregateOutcome(err)}
		agg := s.aggregates[key]
		agg.Operation, agg.Class, agg.Dialect, agg.Outcome = key.operation, key.class, key.dialect, key.outcome
		agg.Attempts++
		agg.SQLCalls += sql.Calls
		agg.SQLDuration += sql.Duration
		agg.QueueDuration += granted.Sub(queuedAt)
		agg.ExecutionDuration += duration
		s.aggregates[key] = agg
		s.mu.Unlock()
		s.logger.Debug("persistence attempt", "operation", meta.Operation, "class", meta.Class, "job_id", meta.JobID, "resource_id", meta.ResourceID, "request_id", meta.RequestID, "candidate_id", meta.CandidateID, "queued_at", queuedAt, "granted_at", granted, "queue_ms", granted.Sub(queuedAt).Milliseconds(), "execution_ms", duration.Milliseconds(), "sql_calls", sql.Calls, "sql_ms", sql.Duration.Milliseconds(), "dialect", dialect, "attempt", attempt+1, "error", err)
		if err == nil {
			return nil
		}
		last = err
		var resolver Resolver
		if errors.As(err, &resolver) {
			verifyAt := time.Now().Add(5 * time.Second)
			if deadline.Before(verifyAt) {
				verifyAt = deadline
			}
			verifyCtx, cancelVerify := context.WithDeadline(WithDeadline(ctx, deadline), verifyAt)
			err = resolver.Resolve(verifyCtx)
			cancelVerify()
			if err == nil {
				return nil
			}
			last = err
		}
		var p permanent
		if errors.As(last, &p) && p.Permanent() || ctx.Err() != nil || !time.Now().Before(deadline) {
			break
		}
		if attempt+1 == s.policy.MaxAttempts {
			break
		}
		timer := time.NewTimer(s.policy.RetryDelay * time.Duration(1<<attempt))
		retryCtx, cancelRetry := context.WithDeadline(ctx, deadline)
		select {
		case <-retryCtx.Done():
			timer.Stop()
			cancelRetry()
			return errors.Join(last, retryCtx.Err())
		case <-timer.C:
		}
		cancelRetry()
	}
	s.mu.Lock()
	s.stats.Failures++
	s.mu.Unlock()
	return last
}
func (s *Scheduler) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.stats
	for _, row := range s.aggregates {
		v.Aggregates = append(v.Aggregates, row)
	}
	v.NormalQueued = len(s.normal)
	v.ReliableQueued = len(s.reliable)
	v.Executing = s.executing
	return v
}

// Close is called after producers and their saves have joined.
func (s *Scheduler) Close() {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.done)
		s.signal()
		for _, row := range s.aggregates {
			s.logger.Debug("persistence summary", "operation", row.Operation, "class", row.Class, "dialect", row.Dialect, "outcome", row.Outcome, "attempts", row.Attempts, "sql_calls", row.SQLCalls, "sql_ms", row.SQLDuration.Milliseconds(), "queue_ms", row.QueueDuration.Milliseconds(), "execution_ms", row.ExecutionDuration.Milliseconds())
		}
	}
	s.mu.Unlock()
}
