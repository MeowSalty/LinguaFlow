package worker

import (
	"context"
	"sync"
)

type Queue struct {
	ch     chan int
	mu     sync.Mutex
	queued map[int]struct{}
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
	q.mu.Unlock()
}
