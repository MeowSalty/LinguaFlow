package preview

import (
	"context"
	"sync"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
)

// MemoryCollector is a concurrency-safe in-memory progress.Reporter + BatchObserver
// that collects BatchEvents emitted by a preview run for response diagnostics.
const MaxCollectorBytes int64 = 64 << 20

type CollectorBudget struct {
	Reserve  func(int64) error
	Release  func(int64) error
	RunBuild func(context.Context, func() error) error
	Cancel   context.CancelFunc
}
type MemoryCollector struct {
	mu               sync.Mutex
	events           []progress.BatchEvent
	ctx              context.Context
	budget           CollectorBudget
	bytes, copyBytes int64
	nodes            int64
	err              error
	closed           bool
}

func NewMemoryCollector() *MemoryCollector {
	return NewBoundedMemoryCollector(context.Background(), CollectorBudget{})
}
func NewBoundedMemoryCollector(ctx context.Context, b CollectorBudget) *MemoryCollector {
	return &MemoryCollector{ctx: ctx, budget: b}
}
func (c *MemoryCollector) failLocked(err error) {
	if c.err == nil {
		c.err = err
		if c.budget.Cancel != nil {
			c.budget.Cancel()
		}
	}
}
func (c *MemoryCollector) reserve(n int64) error {
	if c.budget.Reserve != nil {
		return c.budget.Reserve(n)
	}
	return nil
}
func (c *MemoryCollector) release(n int64) {
	if c.budget.Release != nil {
		_ = c.budget.Release(n)
	}
}
func (c *MemoryCollector) build(fn func() error) error {
	if c.budget.RunBuild != nil {
		return c.budget.RunBuild(c.ctx, fn)
	}
	return fn()
}

// OnBatchEvent appends a copy of the event. Concurrency-safe.
func (c *MemoryCollector) OnBatchEvent(event progress.BatchEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.err != nil {
		return
	}
	if len(c.events) >= 65536 {
		c.failLocked(backend.ErrBuildCapacity)
		return
	}
	err := c.build(func() error {
		measure, err := backend.NewBuildBounds(0, MaxCollectorBytes)
		if err != nil {
			return err
		}
		n, err := measure.MeasureJSON(event)
		if err != nil {
			return err
		}
		if n < 0 || n > MaxCollectorBytes-c.bytes {
			return backend.ErrBuildCapacity
		}
		input := max(int64(4096), 4*n)
		b, err := backend.NewBuildBounds(0, input)
		if err != nil {
			return err
		}
		limits := b.Snapshot()
		credit := 2*n + limits.TextLimit + limits.NodeLimit*backend.MaxNodeAllocationBytes + 4*backend.MaxNodeAllocationBytes
		if err = c.reserve(credit); err != nil {
			return err
		}
		keep := int64(0)
		defer func() { c.release(credit - keep) }()
		raw, err := b.EncodeJSON(event, n)
		if err != nil {
			return err
		}
		defer clear(raw)
		var clone progress.BatchEvent
		if err = b.DecodeJSONLimited(raw, &clone, n); err != nil {
			return err
		}
		used := b.Snapshot()
		if used.Nodes > 65536-c.nodes {
			return backend.ErrBuildCapacity
		}
		keep = used.TextBytes + used.ObjectBytes + 4*backend.MaxNodeAllocationBytes
		if keep > credit {
			keep = 0
			return backend.ErrBuildCapacity
		}
		c.events = append(c.events, clone)
		c.bytes += n
		c.nodes += used.Nodes
		return nil
	})
	if err != nil {
		c.failLocked(err)
	}
}
func (c *MemoryCollector) Err() error { c.mu.Lock(); defer c.mu.Unlock(); return c.err }

// Events returns a snapshot copy of collected events.
func (c *MemoryCollector) Events() []progress.BatchEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.err != nil {
		return nil
	}
	n := int64(len(c.events)) * backend.MaxNodeAllocationBytes
	if n > MaxCollectorBytes-c.copyBytes {
		c.failLocked(backend.ErrBuildCapacity)
		return nil
	}
	var out []progress.BatchEvent
	err := c.build(func() error {
		if err := c.reserve(n); err != nil {
			return err
		}
		out = make([]progress.BatchEvent, len(c.events))
		copy(out, c.events)
		return nil
	})
	if err != nil {
		c.failLocked(err)
		return nil
	}
	c.copyBytes += n
	return out
}

// Compile-time check: *MemoryCollector implements progress.Reporter and progress.BatchObserver.
var _ progress.Reporter = (*MemoryCollector)(nil)
var _ progress.BatchObserver = (*MemoryCollector)(nil)

func (c *MemoryCollector) StageStart(name string, total int) {}
func (c *MemoryCollector) SegmentDone()                      {}
func (c *MemoryCollector) BatchComplete()                    {}
func (c *MemoryCollector) StageDone()                        {}
func (c *MemoryCollector) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	clear(c.events)
	c.events = nil
	c.closed = true
	return c.err
}
