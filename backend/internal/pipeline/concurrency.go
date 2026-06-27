package pipeline

import (
	"context"
	"sync"
)

// RunConcurrent 并发处理 n 项任务，最多 concurrency 个 worker 同时运行。
// 任一 worker 返回非 nil 错误：保留第一个错误，其余 worker 收到 ctx 取消信号。
func RunConcurrent(parent context.Context, n, concurrency int, fn func(ctx context.Context, idx int) error) error {
	if n == 0 {
		return nil
	}
	if concurrency < 1 {
		concurrency = 1
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	jobs := make(chan int, concurrency*2)
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	setErr := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
		cancel()
	}

	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				if ctx.Err() != nil {
					return
				}
				if err := fn(ctx, idx); err != nil {
					setErr(err)
					return
				}
			}
		}()
	}

	for i := 0; i < n; i++ {
		select {
		case <-ctx.Done():
		case jobs <- i:
		}
	}
	close(jobs)
	wg.Wait()
	return firstErr
}
