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
	ch      chan int
	mu      sync.Mutex
	queued  map[int]struct{}
	order   []int // maintains insertion order for position tracking
	removed map[int]struct{}
	doneOps int // counter for periodic compaction
}

func NewQueue(size int) *Queue {
	if size < 1 {
		size = 1
	}
	return &Queue{
		ch:      make(chan int, size),
		queued:  map[int]struct{}{},
		removed: map[int]struct{}{},
	}
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
	delete(q.removed, jobID)
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
	defer q.mu.Unlock()
	if _, exists := q.queued[jobID]; !exists {
		return
	}
	delete(q.queued, jobID)
	q.removed[jobID] = struct{}{}
	q.doneOps++
	if q.doneOps >= 100 {
		q.compact()
	}
}

// compact removes deleted entries from order slice.
func (q *Queue) compact() {
	newOrder := make([]int, 0, len(q.order))
	for _, id := range q.order {
		if _, deleted := q.removed[id]; !deleted {
			newOrder = append(newOrder, id)
		}
	}
	q.order = newOrder
	q.removed = map[int]struct{}{}
	q.doneOps = 0
}

// Cap 返回队列的缓冲区容量。
func (q *Queue) Cap() int {
	return cap(q.ch)
}

// Position returns the queue position info for the given jobID.
func (q *Queue) Position(jobID int) QueueInfo {
	q.mu.Lock()
	defer q.mu.Unlock()
	pos := 0
	size := 0
	for _, id := range q.order {
		if _, deleted := q.removed[id]; deleted {
			continue
		}
		size++
		if id == jobID {
			pos = size
		}
	}
	if pos > 0 {
		return QueueInfo{Position: pos, Size: size}
	}
	return QueueInfo{Position: -1, Size: size}
}
