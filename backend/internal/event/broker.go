package event

import (
	"sync"
	"time"
)

// Event represents a single SSE event published to subscribers.
type Event struct {
	Type      string         `json:"type"`
	JobID     int            `json:"job_id"`
	Level     string         `json:"level"`
	Stage     string         `json:"stage,omitempty"`
	Message   string         `json:"message"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	Seq       int64          `json:"seq"`
}

// Broker is an in-process pub/sub hub for translation job events.
// Each job ID has its own set of subscriber channels.
type Broker struct {
	store       EventStore
	mu          sync.RWMutex
	subscribers map[int]map[chan Event]struct{}
}

// NewBroker creates a new Broker instance.
// If store is nil, events are broadcast without persistence (no replay support).
func NewBroker(store EventStore) *Broker {
	return &Broker{
		store:       store,
		subscribers: make(map[int]map[chan Event]struct{}),
	}
}

// Subscribe registers a new subscriber for the given job ID.
// Returns a buffered channel (capacity 64) that will receive events.
func (b *Broker) Subscribe(jobID int) chan Event {
	ch := make(chan Event, 64)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.subscribers[jobID] == nil {
		b.subscribers[jobID] = make(map[chan Event]struct{})
	}
	b.subscribers[jobID][ch] = struct{}{}
	return ch
}

// Unsubscribe removes a subscriber channel for the given job ID and closes it.
func (b *Broker) Unsubscribe(jobID int, ch chan Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if subs, ok := b.subscribers[jobID]; ok {
		delete(subs, ch)
		if len(subs) == 0 {
			delete(b.subscribers, jobID)
		}
	}
	close(ch)
}

// Publish sends an event to all subscribers of the given job ID.
// If a store is configured, the event is persisted first and assigned a global Seq.
// Non-blocking: if a subscriber's buffer is full, the event is dropped for that subscriber.
func (b *Broker) Publish(jobID int, evt Event) {
	if b.store != nil {
		evt.Seq = b.store.Append(jobID, evt)
	}

	b.mu.RLock()
	defer b.mu.RUnlock()
	if subs, ok := b.subscribers[jobID]; ok {
		for ch := range subs {
			select {
			case ch <- evt:
			default:
			}
		}
	}
}

// Replay returns persisted events with seq > afterSeq for the given job.
// Returns nil if no store is configured or no events are available.
func (b *Broker) Replay(jobID int, afterSeq int64) []Event {
	if b.store == nil {
		return nil
	}
	return b.store.Replay(jobID, afterSeq)
}

// Purge removes all persisted events for the given job from the underlying store.
// Should be called when a job reaches a terminal state to prevent unbounded memory growth.
func (b *Broker) Purge(jobID int) {
	if b.store != nil {
		b.store.Purge(jobID)
	}
}
