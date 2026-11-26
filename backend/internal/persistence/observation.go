package persistence

import (
	"context"
	"log/slog"
	"reflect"
	"sort"
	"sync"
	"time"
)

type Observation struct {
	Metadata       Metadata
	Dialect        string
	Phase, Outcome string
	Duration       time.Duration
	Err            error
	Rows           uint64
	WaitCount      int64
	WaitDuration   time.Duration
}
type Observer interface{ Observe(Observation) }
type observerKey struct{}
type metadataKey struct{}
type dialectKey struct{}

func WithDialect(ctx context.Context, dialect string) context.Context {
	return context.WithValue(ctx, dialectKey{}, dialect)
}
func ObserverFromContext(ctx context.Context) Observer {
	o, _ := ctx.Value(observerKey{}).(Observer)
	return o
}

type fanoutObserver struct{ first, second Observer }

func (o fanoutObserver) Observe(v Observation) {
	if o.first != nil {
		o.first.Observe(v)
	}
	if o.second != nil {
		o.second.Observe(v)
	}
}
func Fanout(first, second Observer) Observer {
	if first == nil {
		return second
	}
	if second == nil || containsObserver(first, second) {
		return first
	}
	return fanoutObserver{first, second}
}
func containsObserver(first, target Observer) bool {
	if f, ok := first.(fanoutObserver); ok {
		return containsObserver(f.first, target) || containsObserver(f.second, target)
	}
	return first != nil && reflect.TypeOf(first) == reflect.TypeOf(target) && reflect.TypeOf(first).Comparable() && first == target
}

func WithMetadata(ctx context.Context, m Metadata) context.Context {
	return context.WithValue(ctx, metadataKey{}, m)
}

func WithObserver(ctx context.Context, o Observer) context.Context {
	return context.WithValue(ctx, observerKey{}, o)
}

// ObserveDatabase reports only bounded driver facts. SQL, parameters and error
// text are intentionally absent from logs; collectors may inspect Err in tests.
func ObserveDatabase(ctx context.Context, phase, outcome, dialect string, duration time.Duration, err error, rows uint64) {
	if o := ObserverFromContext(ctx); o != nil {
		o.Observe(Observation{Metadata: MetadataFromContext(ctx), Dialect: dialect, Phase: phase, Outcome: outcome, Duration: duration, Err: err, Rows: rows})
	}
}

func ObservePoolWait(ctx context.Context, dialect string, count int64, duration time.Duration) {
	if o := ObserverFromContext(ctx); o != nil {
		o.Observe(Observation{Metadata: MetadataFromContext(ctx), Dialect: dialect, Phase: "pool_wait", WaitCount: count, WaitDuration: duration})
	}
}
func ObserveTransaction(ctx context.Context, phase, outcome string, duration time.Duration, err error) {
	o, _ := ctx.Value(observerKey{}).(Observer)
	meta, _ := ctx.Value(metadataKey{}).(Metadata)
	dialect, _ := ctx.Value(dialectKey{}).(string)
	if o != nil {
		o.Observe(Observation{Metadata: meta, Dialect: dialect, Phase: phase, Outcome: outcome, Duration: duration, Err: err})
	}
	slog.Default().Debug("persistence transaction", "operation", meta.Operation, "class", meta.Class, "job_id", meta.JobID, "resource_id", meta.ResourceID, "request_id", meta.RequestID, "candidate_id", meta.CandidateID, "phase", phase, "outcome", outcome, "duration_ms", duration.Milliseconds(), "error", err)
}

type SQLSnapshot struct {
	Dialect               string
	Calls                 uint64
	Duration              time.Duration
	Transactions, Unknown uint64
	Operations            []OperationSnapshot
	// DriverCalls counts application SQL, including implicit ORM reads.
	// Connection-policy SQL is separate: total SQL is DriverCalls+PolicyCalls;
	// transaction boundaries remain separately counted in DriverTransactions.
	DriverCalls, PolicyCalls, Rows, RowSets, DriverTransactions             uint64
	DriverDuration, PolicyDuration, RowsDuration, DriverTransactionDuration time.Duration
	PoolWaitCount                                                           int64
	PoolWaitDuration                                                        time.Duration
	SQLDurationBuckets, RowsDurationBuckets, TransactionDurationBuckets     [8]uint64
}
type OperationSnapshot struct {
	Operation                                       string
	Queue, Executions, Calls, Transactions, Unknown uint64
}
type Collector struct {
	mu         sync.Mutex
	stats      SQLSnapshot
	operations map[string]OperationSnapshot
}

func (c *Collector) Observe(o Observation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	operation := aggregateOperation(o.Metadata.Operation)
	if c.operations == nil {
		c.operations = make(map[string]OperationSnapshot)
	}
	row := c.operations[operation]
	row.Operation = operation
	switch o.Phase {
	case "save_queue":
		row.Queue++
	case "save_execution":
		row.Executions++
	case "query", "exec":
		row.Calls++
	case "transaction":
		row.Transactions++
		if o.Outcome == "unknown" {
			row.Unknown++
		}
	}
	c.operations[operation] = row
	if o.Phase == "query" || o.Phase == "exec" {
		if o.Dialect != "" {
			c.stats.Dialect = o.Dialect
		}
		c.stats.Calls++
		c.stats.Duration += o.Duration
	}
	if o.Phase == "transaction" {
		c.stats.Transactions++
		if o.Outcome == "unknown" {
			c.stats.Unknown++
		}
	}
	switch o.Phase {
	case "driver_query", "driver_exec":
		c.stats.DriverCalls++
		c.stats.DriverDuration += o.Duration
		c.stats.SQLDurationBuckets[durationBucket(o.Duration)]++
	case "driver_policy_query", "driver_policy_exec":
		c.stats.PolicyCalls++
		c.stats.PolicyDuration += o.Duration
	case "driver_rows":
		c.stats.RowSets++
		c.stats.Rows += o.Rows
		c.stats.RowsDuration += o.Duration
		c.stats.RowsDurationBuckets[durationBucket(o.Duration)]++
	case "driver_transaction":
		c.stats.DriverTransactions++
		c.stats.DriverTransactionDuration += o.Duration
		c.stats.TransactionDurationBuckets[durationBucket(o.Duration)]++
	case "pool_wait":
		c.stats.PoolWaitCount += o.WaitCount
		c.stats.PoolWaitDuration += o.WaitDuration
	}
}

// Fixed inclusive upper bounds: 100us, 1ms, 10ms, 100ms, 1s, 5s, 30s, +Inf.
// Histograms retain distributions without per-statement samples or SQL labels.
func durationBucket(duration time.Duration) int {
	for i, upper := range [...]time.Duration{100 * time.Microsecond, time.Millisecond, 10 * time.Millisecond, 100 * time.Millisecond, time.Second, 5 * time.Second, 30 * time.Second} {
		if duration <= upper {
			return i
		}
	}
	return 7
}
func (c *Collector) Snapshot() SQLSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.stats
	out.Operations = make([]OperationSnapshot, 0, len(c.operations))
	for _, row := range c.operations {
		out.Operations = append(out.Operations, row)
	}
	sort.Slice(out.Operations, func(i, j int) bool { return out.Operations[i].Operation < out.Operations[j].Operation })
	return out
}
