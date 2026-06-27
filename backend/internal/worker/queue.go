package worker

import (
	"context"
	"sync"
)

// QueueInfo contains queue position information for a job.
type QueueInfo struct {
	Position int // 1-based position, -1 means not in queue
	Size     int // total number of jobs in queue
}

type Queue struct {
	ch     chan int
	mu     sync.Mutex
	queued map[int]struct{}
	order  []int // maintains insertion order for position tracking
}

func NewQueue(size int) *Queue {
	if size < 1 {
		size = 1
	}
	return &Queue{ch: make(chan int, size), queued: map[int]struct{}{}}
}

func (q *Queue) Enqueue(ctx context.Context, jobID int) error {
	if jobID <= 0 {
		return nil
	}
	q.mu.Lock()
	if _, exists := q.queued[jobID]; exists {
		q.mu.Unlock()
		return nil
	}
	q.queued[jobID] = struct{}{}
	q.order = append(q.order, jobID)
	q.mu.Unlock()
	select {
	case <-ctx.Done():
		q.Done(jobID)
		return ctx.Err()
	case q.ch <- jobID:
		return nil
	}
}

func (q *Queue) Dequeue(ctx context.Context) (int, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case jobID := <-q.ch:
		return jobID, nil
	}
}

func (q *Queue) Done(jobID int) {
	q.mu.Lock()
	delete(q.queued, jobID)
	// Remove from order slice
	for i, id := range q.order {
		if id == jobID {
			q.order = append(q.order[:i], q.order[i+1:]...)
			break
		}
	}
	q.mu.Unlock()
}

// Position returns the queue position info for the given jobID.
func (q *Queue) Position(jobID int) QueueInfo {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, id := range q.order {
		if id == jobID {
			return QueueInfo{Position: i + 1, Size: len(q.order)}
		}
	}
	return QueueInfo{Position: -1, Size: len(q.order)}
}
