package worker

import (
	"context"
	"errors"
	"sync"
)

var ErrQueueClosed = errors.New("worker queue is closed")
var ErrInvalidTaskID = errors.New("task ID must be positive")

// Execution identifies one claim. Old completions cannot release another run.
type Execution struct {
	TaskID     int
	Generation uint64
	ctx        context.Context
}

type workerLifetimeKey struct{}

// workerLifetime separates a user's per-execution cancellation from shutdown.
// Cleanup may outlive the former, but must never outlive the latter.
func workerLifetime(ctx context.Context) context.Context {
	if lifetime, ok := ctx.Value(workerLifetimeKey{}).(context.Context); ok {
		return lifetime
	}
	return ctx
}

// Context is cancelled when this execution, its worker, or the queue stops.
func (e Execution) Context() context.Context { return e.ctx }

type QueueInfo struct {
	Position int
	Size     int
}

type QueueSnapshot struct {
	Capacity       int `json:"queue_capacity"`
	Waiting        int `json:"queue_waiting"`
	EnqueueWaiters int `json:"enqueue_waiters"`
}

type queueState uint8

const (
	queueEnqueuing queueState = iota
	queueWaiting
	queueRunning
	queueRetired
)

type queueEntry struct {
	execution Execution
	state     queueState
	cancel    context.CancelFunc
}

// Queue bounds accepted, unclaimed work. Running work retains its identity for
// deduplication until Done; cancelled waiting work immediately frees capacity.
type Queue struct {
	mu       sync.Mutex
	capacity int
	next     uint64
	entries  map[int]*queueEntry
	waiting  []*queueEntry
	waiters  int
	changed  chan struct{}
	closed   bool
}

func NewQueue(size int) *Queue {
	if size < 1 {
		size = 1
	}
	return &Queue{capacity: size, entries: make(map[int]*queueEntry), changed: make(chan struct{})}
}

func (q *Queue) Enqueue(ctx context.Context, taskID int) error {
	_, err := q.enqueue(ctx, taskID)
	return err
}

// enqueue additionally reports whether this call accepted new work.
func (q *Queue) enqueue(ctx context.Context, taskID int) (bool, error) {
	return q.enqueueChecked(ctx, taskID, nil)
}

// enqueueChecked registers the generation before checking durable state. This
// lets cancellation invalidate an in-progress admission, while stale discovery
// pages cannot add cancelled tasks after an earlier cancellation found no entry.
// The check runs without q.mu and before the entry is counted as waiting.
func (q *Queue) enqueueChecked(ctx context.Context, taskID int, check func(context.Context, int) (bool, error)) (bool, error) {
	if taskID <= 0 {
		return false, ErrInvalidTaskID
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if q.closed {
		return false, ErrQueueClosed
	}
	if _, exists := q.entries[taskID]; exists {
		return false, nil
	}
	q.next++
	entry := &queueEntry{execution: Execution{TaskID: taskID, Generation: q.next}, state: queueEnqueuing}
	q.entries[taskID] = entry
	blocked := false
	validated := check == nil
	defer func() {
		if blocked {
			q.waiters--
		}
		if entry.state == queueEnqueuing && q.entries[taskID] == entry {
			delete(q.entries, taskID)
			entry.state = queueRetired
		}
		q.signalLocked()
	}()
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if q.closed {
			return false, ErrQueueClosed
		}
		if entry.state == queueRetired {
			return false, context.Canceled
		}
		if len(q.waiting) < q.capacity {
			if blocked {
				q.waiters--
				blocked = false
				q.signalLocked()
			}
			if !validated {
				q.mu.Unlock()
				allowed, err := check(ctx, taskID)
				q.mu.Lock()
				if err != nil {
					return false, err
				}
				if !allowed {
					return false, nil
				}
				validated = true
				continue
			}
			entry.state = queueWaiting
			q.waiting = append(q.waiting, entry)
			return true, nil
		}
		// If capacity changed during validation, check again once it becomes
		// available instead of relying on a potentially old database result.
		validated = check == nil
		if !blocked {
			q.waiters++
			blocked = true
			q.signalLocked()
		}
		changed := q.changed
		q.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-changed:
		}
		q.mu.Lock()
	}
}

func (q *Queue) Dequeue(ctx context.Context) (Execution, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for {
		if err := ctx.Err(); err != nil {
			return Execution{}, err
		}
		if q.closed {
			return Execution{}, ErrQueueClosed
		}
		if len(q.waiting) > 0 {
			entry := q.waiting[0]
			q.waiting[0] = nil
			q.waiting = q.waiting[1:]
			entry.state = queueRunning
			entry.execution.ctx, entry.cancel = context.WithCancel(ctx)
			entry.execution.ctx = context.WithValue(entry.execution.ctx, workerLifetimeKey{}, ctx)
			q.signalLocked()
			return entry.execution, nil
		}
		changed := q.changed
		q.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-changed:
		}
		q.mu.Lock()
	}
}

func (q *Queue) Done(execution Execution) {
	q.mu.Lock()
	defer q.mu.Unlock()
	entry := q.entries[execution.TaskID]
	if entry == nil || entry.execution.Generation != execution.Generation || entry.state != queueRunning {
		return
	}
	entry.cancel()
	entry.state = queueRetired
	delete(q.entries, execution.TaskID)
	q.signalLocked()
}

// Cancel preserves a running claim until Done while promptly cancelling its
// context. It never permits two ProcessOne calls for the same ID to overlap.
func (q *Queue) Cancel(taskID int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cancelLocked(q.entries[taskID])
}

// Capture returns the current admission/claim identity without changing it.
// Capture must precede any external state check used to cancel that identity.
func (q *Queue) Capture(taskID int) (Execution, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	entry := q.entries[taskID]
	if entry == nil {
		return Execution{}, false
	}
	return entry.execution, true
}

func (q *Queue) CancelExecution(execution Execution) {
	q.mu.Lock()
	defer q.mu.Unlock()
	entry := q.entries[execution.TaskID]
	if entry != nil && entry.execution.Generation == execution.Generation {
		q.cancelLocked(entry)
	}
}

func (q *Queue) cancelLocked(entry *queueEntry) {
	if entry == nil {
		return
	}
	if entry.state == queueRunning {
		entry.cancel()
		return
	}
	if entry.state == queueWaiting {
		for i, queued := range q.waiting {
			if queued == entry {
				copy(q.waiting[i:], q.waiting[i+1:])
				q.waiting[len(q.waiting)-1] = nil
				q.waiting = q.waiting[:len(q.waiting)-1]
				break
			}
		}
	}
	entry.state = queueRetired
	delete(q.entries, entry.execution.TaskID)
	q.signalLocked()
}

func (q *Queue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.closed = true
	for id, entry := range q.entries {
		if entry.state == queueRunning {
			entry.cancel()
		} else {
			entry.state = queueRetired
			delete(q.entries, id)
		}
	}
	q.waiting = nil
	q.signalLocked()
}

func (q *Queue) signalLocked() {
	close(q.changed)
	q.changed = make(chan struct{})
}

func (q *Queue) Cap() int { return q.capacity }

func (q *Queue) Snapshot() QueueSnapshot {
	q.mu.Lock()
	defer q.mu.Unlock()
	return QueueSnapshot{Capacity: q.capacity, Waiting: len(q.waiting), EnqueueWaiters: q.waiters}
}

// Position is retained only for internal compatibility. Public task responses
// must not expose an instance-wide queue position or size.
func (q *Queue) Position(taskID int) QueueInfo {
	q.mu.Lock()
	defer q.mu.Unlock()
	info := QueueInfo{Position: -1, Size: len(q.waiting)}
	for i, entry := range q.waiting {
		if entry.execution.TaskID == taskID {
			info.Position = i + 1
			break
		}
	}
	return info
}
