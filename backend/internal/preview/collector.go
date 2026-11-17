package preview

import (
	"context"
	"sync"
	"unsafe"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
)

// MemoryCollector is a concurrency-safe in-memory progress.Reporter + BatchObserver
// that collects BatchEvents emitted by a preview run for response diagnostics.
const MaxCollectorBytes int64 = 64 << 20

type CollectorBudget struct {
	Reserve         func(int64) error
	Release         func(int64) error
	RunBuild        func(context.Context, func() error) error
	AdoptAllocation func(context.Context, *backend.BuildBounds, int64) error
	Cancel          context.CancelFunc
}
type MemoryCollector struct {
	mu               sync.Mutex
	events           []progress.BatchEvent
	ctx              context.Context
	budget           CollectorBudget
	bytes, copyBytes int64
	retireBacking    func()
	retireClones     func()
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
	return c.buildContext(c.ctx, fn)
}
func (c *MemoryCollector) buildContext(ctx context.Context, fn func() error) error {
	if c.budget.RunBuild != nil {
		return c.budget.RunBuild(ctx, fn)
	}
	return fn()
}

// OnBatchEvent appends a copy of the event. Concurrency-safe.
func (c *MemoryCollector) OnBatchEvent(event progress.BatchEvent) {
	c.OnBatchEventContext(c.ctx, event)
}

// OnBatchEventContext can consume the producer's live, transferable capacity.
// Its private event fields remain owned by the collector after producer close.
func (c *MemoryCollector) OnBatchEventContext(ctx context.Context, event progress.BatchEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.err != nil {
		return
	}
	buildCtx := backend.WithBuildReservation(c.ctx, c.reserve, c.release)
	shared := c.budget.AdoptAllocation != nil && backend.HasActiveBuildAllocationSource(ctx)
	if shared {
		buildCtx = ctx
	}
	err := c.buildContext(buildCtx, func() error {
		b, err := backend.NewBuildBoundsFromContext(buildCtx, MaxCollectorBytes, 0)
		if err != nil {
			return err
		}
		defer b.CloseRetired()
		var clone progress.BatchEvent
		var events []progress.BatchEvent
		var retireClone, retireBacking func()
		installed := false
		defer func() {
			if !installed {
				clear(events)
				events = nil
				clone = progress.BatchEvent{}
				if retireBacking != nil {
					retireBacking()
				}
				if retireClone != nil {
					retireClone()
				}
			}
			clone = progress.BatchEvent{}
			events = nil
		}()
		n, err := b.MeasureJSONLimited(event, MaxCollectorBytes-c.bytes)
		if err != nil {
			return err
		}
		if n < 0 || n > MaxCollectorBytes-c.bytes {
			return backend.ErrBuildCapacity
		}
		if err = b.CloneInto(event, &clone); err != nil {
			return err
		}
		cloneBytes, err := backend.ValueAllocationBytesFromContext(buildCtx, clone)
		if err != nil {
			return err
		}
		backingBytes, err := backend.AllocationBytes(int64(len(c.events))+1, int64(unsafe.Sizeof(progress.BatchEvent{})))
		if err != nil {
			return err
		}
		if err = backend.ReserveSlice[progress.BatchEvent](b, int64(len(c.events))+1); err != nil {
			return err
		}
		events = make([]progress.BatchEvent, len(c.events)+1)
		copy(events, c.events)
		events[len(c.events)] = clone
		if shared {
			retainedBytes, err := backend.CheckedAdd(cloneBytes, backingBytes)
			if err != nil {
				return err
			}
			// Both newly created outputs move together. A failed Root transfer
			// leaves every original receipt intact until the defer clears them.
			if err = c.budget.AdoptAllocation(buildCtx, b, retainedBytes); err != nil {
				return err
			}
			retireClone = func() { c.release(cloneBytes) }
			retireBacking = func() { c.release(backingBytes) }
		} else {
			retireClone, err = c.retainOutput(b, cloneBytes)
			if err != nil {
				return err
			}
			retireBacking, err = c.retainOutput(b, backingBytes)
			if err != nil {
				return err
			}
		}
		oldBacking := c.retireBacking
		c.events = events
		c.retireBacking = retireBacking
		retire := c.retireClones
		c.retireClones = func() {
			retireClone()
			if retire != nil {
				retire()
			}
		}
		installed = true
		if oldBacking != nil {
			oldBacking()
		}
		c.bytes += n
		return nil
	})
	if err != nil {
		c.failLocked(err)
	}
}

func (c *MemoryCollector) retainOutput(b *backend.BuildBounds, bytes int64) (func(), error) {
	allocation, err := b.RetainedAllocation(bytes)
	if err != nil {
		return nil, err
	}
	if err = allocation.DetachBuild(); err != nil {
		allocation.Release()
		return nil, err
	}
	return allocation.Release, nil
}
func (c *MemoryCollector) Err() error { c.mu.Lock(); defer c.mu.Unlock(); return c.err }

// Events returns a snapshot copy of collected events.
func (c *MemoryCollector) Events() []progress.BatchEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.err != nil {
		return nil
	}
	n, err := backend.AllocationBytes(int64(len(c.events)), int64(unsafe.Sizeof(progress.BatchEvent{})))
	if err != nil {
		c.failLocked(err)
		return nil
	}
	if n > MaxCollectorBytes-c.copyBytes {
		c.failLocked(backend.ErrBuildCapacity)
		return nil
	}
	var out []progress.BatchEvent
	err = c.build(func() error {
		if err := c.reserve(n); err != nil {
			return err
		}
		b, err := backend.NewBuildBoundsFromContext(c.ctx, 0, 0)
		if err != nil {
			c.release(n)
			return err
		}
		defer b.CloseRetired()
		if err = backend.ReserveSlice[progress.BatchEvent](b, int64(len(c.events))); err != nil {
			c.release(n)
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
	if c.retireBacking != nil {
		c.retireBacking()
		c.retireBacking = nil
	}
	if c.retireClones != nil {
		c.retireClones()
		c.retireClones = nil
	}
	c.closed = true
	return c.err
}
