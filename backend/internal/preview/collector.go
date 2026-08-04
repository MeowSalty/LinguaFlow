package preview

import (
	"sync"

	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
)

// MemoryCollector is a concurrency-safe in-memory progress.Reporter + BatchObserver
// that collects BatchEvents emitted by a preview run for response diagnostics.
type MemoryCollector struct {
	mu     sync.Mutex
	events []progress.BatchEvent
}

func NewMemoryCollector() *MemoryCollector {
	return &MemoryCollector{}
}

// OnBatchEvent appends a copy of the event. Concurrency-safe.
func (c *MemoryCollector) OnBatchEvent(event progress.BatchEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
}

// Events returns a snapshot copy of collected events.
func (c *MemoryCollector) Events() []progress.BatchEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]progress.BatchEvent, len(c.events))
	copy(out, c.events)
	return out
}

// Compile-time check: *MemoryCollector implements progress.Reporter and progress.BatchObserver.
var _ progress.Reporter = (*MemoryCollector)(nil)
var _ progress.BatchObserver = (*MemoryCollector)(nil)

func (c *MemoryCollector) StageStart(name string, total int) {}
func (c *MemoryCollector) SegmentDone()                      {}
func (c *MemoryCollector) BatchComplete()                    {}
func (c *MemoryCollector) StageDone()                        {}
func (c *MemoryCollector) Close() error                      { return nil }
