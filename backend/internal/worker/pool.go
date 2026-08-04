package worker

import (
	"context"
	"log/slog"
	"sync"
)

// WorkerPool 管理多个 Worker goroutine，每个 Worker 独立从队列取任务执行。
// 错误独立处理，不汇总。
type WorkerPool struct {
	concurrency int
	logger      *slog.Logger
	wg          sync.WaitGroup
}

// NewWorkerPool 创建一个新的 WorkerPool。
func NewWorkerPool(concurrency int, logger *slog.Logger) *WorkerPool {
	if concurrency < 1 {
		concurrency = 1
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &WorkerPool{
		concurrency: concurrency,
		logger:      logger,
	}
}

// Start 启动所有 Worker goroutine，从 queue 中 Dequeue 并调用 processFn 处理。
// Worker 在 ctx 取消后停止取新任务，等待正在执行的任务完成后返回。
func (wp *WorkerPool) Start(ctx context.Context, queue *Queue, processFn func(ctx context.Context, id int) error) {
	for i := 0; i < wp.concurrency; i++ {
		wp.wg.Add(1)
		go func() {
			defer wp.wg.Done()
			for {
				id, err := queue.Dequeue(ctx)
				if err != nil {
					return // ctx cancelled or queue closed
				}
				if err := processFn(ctx, id); err != nil {
					wp.logger.Error("worker pool: task processing failed", "task_id", id, "err", err)
				}
				queue.Done(id)
			}
		}()
	}
}

// Wait 等待所有 Worker goroutine 完成。
func (wp *WorkerPool) Wait() {
	wp.wg.Wait()
}
