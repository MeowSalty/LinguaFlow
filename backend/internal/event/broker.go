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
}

// Broker is an in-process pub/sub hub for translation job events.
// Each job ID has its own set of subscriber channels.
type Broker struct {
	mu          sync.RWMutex
	subscribers map[int]map[chan Event]struct{}
}

// NewBroker creates a new Broker instance.
func NewBroker() *Broker {
	return &Broker{
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
// Non-blocking: if a subscriber's buffer is full, the event is dropped for that subscriber.
func (b *Broker) Publish(jobID int, evt Event) {
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
