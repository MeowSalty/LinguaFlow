package worker

import (
	"context"
	"log/slog"
	"sync"
)

type PoolSnapshot struct {
	Capacity int `json:"worker_capacity"`
	Alive    int `json:"workers_alive"`
	Busy     int `json:"workers_busy"`
}

// WorkerPool owns claims until ProcessOne returns, including resource and
// limiter waiting, admission, and pause draining.
type WorkerPool struct {
	concurrency int
	logger      *slog.Logger
	once        sync.Once
	wg          sync.WaitGroup
	mu          sync.Mutex
	alive       int
	busy        int
	done        chan struct{}
}

func NewWorkerPool(concurrency int, logger *slog.Logger) *WorkerPool {
	if concurrency < 1 {
		concurrency = 1
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &WorkerPool{concurrency: concurrency, logger: logger, done: make(chan struct{})}
}

func (wp *WorkerPool) Start(ctx context.Context, queue *Queue, processFn func(context.Context, int) error) {
	wp.once.Do(func() {
		wp.wg.Add(wp.concurrency)
		for i := 0; i < wp.concurrency; i++ {
			go func() {
				wp.mu.Lock()
				wp.alive++
				wp.mu.Unlock()
				defer func() {
					wp.mu.Lock()
					wp.alive--
					wp.mu.Unlock()
					wp.wg.Done()
				}()
				for {
					execution, err := queue.Dequeue(ctx)
					if err != nil {
						return
					}
					wp.process(queue, execution, processFn)
				}
			}()
		}
		go func() {
			wp.wg.Wait()
			close(wp.done)
		}()
	})
}

func (wp *WorkerPool) process(queue *Queue, execution Execution, processFn func(context.Context, int) error) {
	wp.mu.Lock()
	wp.busy++
	wp.mu.Unlock()
	defer func() {
		wp.mu.Lock()
		wp.busy--
		wp.mu.Unlock()
		queue.Done(execution)
	}()
	if err := processFn(execution.Context(), execution.TaskID); err != nil && execution.Context().Err() == nil {
		wp.logger.Error("worker pool: task processing failed", "task_id", execution.TaskID, "err", err)
	}
}

func (wp *WorkerPool) Wait() { <-wp.done }

func (wp *WorkerPool) WaitContext(ctx context.Context) error {
	select {
	case <-wp.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (wp *WorkerPool) Snapshot() PoolSnapshot {
	wp.mu.Lock()
	defer wp.mu.Unlock()
	return PoolSnapshot{Capacity: wp.concurrency, Alive: wp.alive, Busy: wp.busy}
}
