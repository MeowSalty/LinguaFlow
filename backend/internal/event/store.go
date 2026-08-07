package event

import (
	"context"
	"sync"
	"sync/atomic"
)

// EventStore defines the interface for event persistence and replay.
type EventStore interface {
	// Append stores an event and returns the assigned global sequence number.
	// Returns an error if the event could not be persisted.
	Append(jobID int, evt Event) (int64, error)

	// Replay returns up to limit events with seq > afterSeq for the given job,
	// ordered ascending by seq. If limit <= 0, all matching events are returned.
	// If the requested range is older than the buffer, returns nil to signal
	// the caller (e.g. HybridStore) to fall back to the DB.
	// The ctx cancels the underlying query on client disconnect.
	Replay(ctx context.Context, jobID int, afterSeq int64, limit int) []Event

	// LatestSeq returns the highest seq currently stored for the given job,
	// and false when the job has no events. Used to compute the recent-window
	// replay start for fresh SSE connections.
	LatestSeq(ctx context.Context, jobID int) (int64, bool)

	// Purge removes all stored events for the given job.
	Purge(jobID int)
}

// RingBufferConfig configures the ring buffer behavior.
type RingBufferConfig struct {
	Capacity int // Maximum events per job buffer. Default: 256.
}

// DefaultRingBufferConfig returns the default configuration.
func DefaultRingBufferConfig() RingBufferConfig {
	return RingBufferConfig{
		Capacity: 256,
	}
}

type ringBuffer struct {
	events []Event
	head   int
	count  int
	mu     sync.RWMutex
}

// RingBufferStore is an in-memory EventStore using per-job ring buffers.
type RingBufferStore struct {
	buffers map[int]*ringBuffer
	nextSeq atomic.Int64
	config  RingBufferConfig
	mu      sync.RWMutex
}

// NewRingBufferStore creates a new RingBufferStore with the given config.
func NewRingBufferStore(config RingBufferConfig) *RingBufferStore {
	return &RingBufferStore{
		buffers: make(map[int]*ringBuffer),
		config:  config,
	}
}

func (s *RingBufferStore) getOrCreateBuffer(jobID int) *ringBuffer {
	s.mu.RLock()
	buf, ok := s.buffers[jobID]
	s.mu.RUnlock()
	if ok {
		return buf
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	buf, ok = s.buffers[jobID]
	if ok {
		return buf
	}
	buf = &ringBuffer{
		events: make([]Event, s.config.Capacity),
	}
	s.buffers[jobID] = buf
	return buf
}

// Append stores an event and returns the assigned global sequence number.
func (s *RingBufferStore) Append(jobID int, evt Event) (int64, error) {
	seq := s.nextSeq.Add(1)
	evt.Seq = seq

	buf := s.getOrCreateBuffer(jobID)
	buf.mu.Lock()
	buf.events[buf.head%len(buf.events)] = evt
	buf.head++
	buf.count++
	buf.mu.Unlock()

	return seq, nil
}

// AppendWithSeq stores an event with a pre-assigned seq (no internal seq assignment).
func (s *RingBufferStore) AppendWithSeq(jobID int, evt Event) {
	buf := s.getOrCreateBuffer(jobID)
	buf.mu.Lock()
	buf.events[buf.head%len(buf.events)] = evt
	buf.head++
	buf.count++
	buf.mu.Unlock()
}

// Replay returns up to limit events with seq > afterSeq for the given job,
// ordered ascending by seq. If limit <= 0, all matching events are returned.
// ctx is accepted for interface symmetry but unused (in-memory scan).
func (s *RingBufferStore) Replay(ctx context.Context, jobID int, afterSeq int64, limit int) []Event {
	s.mu.RLock()
	buf, ok := s.buffers[jobID]
	s.mu.RUnlock()
	if !ok {
		return nil
	}

	buf.mu.RLock()
	defer buf.mu.RUnlock()

	if buf.count == 0 {
		return nil
	}

	capacity := len(buf.events)
	start := 0
	n := buf.count
	if n > capacity {
		start = buf.head - capacity
		n = capacity
	} else {
		start = 0
	}

	// 当 buffer 溢出时，检查 afterSeq 是否指向被淘汰的事件
	if buf.count > capacity {
		oldestSeq := buf.events[start%capacity].Seq
		if afterSeq < oldestSeq {
			return nil
		}
	}

	var result []Event
	for i := 0; i < n; i++ {
		idx := (start + i) % capacity
		evt := buf.events[idx]
		if evt.Seq > afterSeq {
			result = append(result, evt)
			if limit > 0 && len(result) >= limit {
				break
			}
		}
	}
	return result
}

// LatestSeq returns the highest seq in the job's ring buffer, and false when
// the buffer is empty or absent.
func (s *RingBufferStore) LatestSeq(ctx context.Context, jobID int) (int64, bool) {
	s.mu.RLock()
	buf, ok := s.buffers[jobID]
	s.mu.RUnlock()
	if !ok {
		return 0, false
	}
	buf.mu.RLock()
	defer buf.mu.RUnlock()
	if buf.count == 0 {
		return 0, false
	}
	return buf.events[(buf.head-1)%len(buf.events)].Seq, true
}

// Purge removes all stored events for the given job.
func (s *RingBufferStore) Purge(jobID int) {
	s.mu.Lock()
	delete(s.buffers, jobID)
	s.mu.Unlock()
}
