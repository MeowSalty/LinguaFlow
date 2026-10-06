package event

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
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
	historian   Historian
	mu          sync.RWMutex
	subscribers map[int]map[chan Event]struct{}
}

// Historian provides paginated reads of the full persisted event history,
// used by REST endpoints that need durable, DB-backed listings (unlike Replay,
// which only serves the in-memory reconnection window). Implementations store
// the events durably (e.g. EntEventStore).
type Historian interface {
	// ListPage returns up to limit events with seq > afterSeq, ascending.
	ListPage(ctx context.Context, jobID int, afterSeq int64, limit int) (events []Event, nextAfterSeq int64, hasMore bool, err error)
	// ListPageBefore returns up to limit events with seq < beforeSeq,
	// ascending, for backward (toward older) pagination. beforeSeq <= 0
	// returns the most recent events (initial latest page).
	ListPageBefore(ctx context.Context, jobID int, beforeSeq int64, limit int) (events []Event, nextBeforeSeq int64, hasMore bool, err error)
}

var ErrHistoryUnavailable = errors.New("durable event history unavailable")

// NewBroker creates a new Broker instance.
// If store is nil, events are broadcast without persistence (no replay support).
func NewBroker(store EventStore) *Broker {
	return &Broker{
		store:       store,
		subscribers: make(map[int]map[chan Event]struct{}),
	}
}

// WithHistorian attaches a Historian so the broker can serve paginated history
// reads (e.g. for REST endpoints). Returns the broker for chaining.
func (b *Broker) WithHistorian(h Historian) *Broker {
	b.historian = h
	return b
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
		if _, exists := subs[ch]; !exists {
			return
		}
		delete(subs, ch)
		close(ch)
		if len(subs) == 0 {
			delete(b.subscribers, jobID)
		}
	}
}

// CloseJob ends every subscription after history deletion and drops the replay
// window. The caller holds the task lifecycle guard through this cleanup so
// registration cannot escape between the database commit and this operation.
// Concurrent disconnects and repeated cleanup are safe.
func (b *Broker) CloseJob(jobID int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subscribers[jobID] {
		close(ch)
	}
	delete(b.subscribers, jobID)
	b.Purge(jobID)
}

// Publish sends an event to all subscribers of the given job ID.
// If a store is configured, the event is persisted first and assigned a global Seq.
// If persistence fails, the event is still broadcast in degraded mode (memory-only).
// Non-blocking: if a subscriber's buffer is full, the event is dropped for that subscriber.
func (b *Broker) Publish(jobID int, evt Event) {
	evt.CreatedAt = timeutil.Normalize(evt.CreatedAt)
	if normalizer, ok := b.store.(interface{ NormalizeTime(time.Time) time.Time }); ok {
		evt.CreatedAt = normalizer.NormalizeTime(evt.CreatedAt)
	}
	if b.store != nil {
		seq, err := b.store.Append(jobID, evt)
		if err != nil {
			slog.Warn("broker: store append failed, event broadcasted in degraded mode",
				"job_id", jobID, "error", err)
		}
		evt.Seq = seq
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

// Replay returns up to limit persisted events with seq > afterSeq for the
// given job, ordered ascending by seq. If limit <= 0, all matching events are
// returned. Returns nil if no store is configured or no events match.
// The ctx cancels the underlying query on client disconnect.
func (b *Broker) Replay(ctx context.Context, jobID int, afterSeq int64, limit int) ([]Event, error) {
	if b.store == nil {
		return nil, nil
	}
	return b.store.Replay(ctx, jobID, afterSeq, limit)
}

// ListHistory returns a page of the durable event history for a job. It reads
// the backing DB directly (not the reconnection ring buffer), so it is suitable
// for REST endpoints that must list complete historical events. Returns
// hasMore=false when no Historian is configured.
func (b *Broker) ListHistory(ctx context.Context, jobID int, afterSeq int64, limit int) ([]Event, int64, bool, error) {
	if b.historian == nil {
		return nil, 0, false, ErrHistoryUnavailable
	}
	return b.historian.ListPage(ctx, jobID, afterSeq, limit)
}

// ListHistoryBefore returns a backward page (toward older events) from the
// durable history. beforeSeq <= 0 returns the most recent events. Returns
// hasMore=false when no Historian is configured.
func (b *Broker) ListHistoryBefore(ctx context.Context, jobID int, beforeSeq int64, limit int) ([]Event, int64, bool, error) {
	if b.historian == nil {
		return nil, 0, false, ErrHistoryUnavailable
	}
	return b.historian.ListPageBefore(ctx, jobID, beforeSeq, limit)
}

// LatestSeq returns the highest seq stored for the given job, and false when
// no store is configured or the job has no events. Used to compute the
// recent-window replay start for fresh SSE connections.
func (b *Broker) LatestSeq(ctx context.Context, jobID int) (int64, bool, error) {
	if b.store == nil {
		return 0, false, nil
	}
	return b.store.LatestSeq(ctx, jobID)
}

// Purge releases the store's replay window. HybridStore retains durable history;
// deleting persistent events belongs to the task history transaction.
func (b *Broker) Purge(jobID int) {
	if b.store != nil {
		b.store.Purge(jobID)
	}
}
