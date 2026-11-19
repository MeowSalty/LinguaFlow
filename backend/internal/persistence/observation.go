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
