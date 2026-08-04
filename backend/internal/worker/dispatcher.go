package worker

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

// Dispatcher 管理所有 TaskRunner 的生命周期。
// 负责 Recover、启动 WorkerPool、以及任务入队和取消的路由。
type Dispatcher struct {
	logger    *slog.Logger
	runners   []TaskRunner
	resMutex  *ResourceMutex
	workerCfg config.WorkerConfig
}

// NewDispatcher 创建一个新的 Dispatcher。
func NewDispatcher(
	logger *slog.Logger,
	resMutex *ResourceMutex,
	workerCfg config.WorkerConfig,
	runners ...TaskRunner,
) *Dispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	return &Dispatcher{
		logger:    logger,
		runners:   runners,
		resMutex:  resMutex,
		workerCfg: workerCfg,
	}
}

// Run 执行 Dispatcher 的主循环：
// 1. 遍历 runners，调用 Recover()，将返回的 taskIDs 入队
// 2. 为每个 runner 创建 WorkerPool，调用 Start()
// 3. 等待 ctx.Done()，然后等待所有 WorkerPool 完成
func (d *Dispatcher) Run(ctx context.Context) error {
	pools := make([]*WorkerPool, 0, len(d.runners))

	for _, runner := range d.runners {
		// Recover
		taskIDs, err := runner.Recover(ctx)
		if err != nil {
			d.logger.Error("dispatcher: recovery failed", "type", runner.Type(), "err", err)
		} else {
			for _, taskID := range taskIDs {
				if err := runner.Queue().Enqueue(ctx, taskID); err != nil {
					d.logger.Error("dispatcher: enqueue recovered task failed", "type", runner.Type(), "task_id", taskID, "err", err)
				}
			}
			if len(taskIDs) > 0 {
				d.logger.Info("dispatcher: recovery completed", "type", runner.Type(), "tasks", len(taskIDs))
			}
		}

		// Determine worker count from config
		count := d.workerCount(runner.Type())
		d.logger.Info("dispatcher: starting worker pool",
			"type", runner.Type(),
			"workers", count,
			"queue_capacity", runner.Queue().Cap(),
		)
		pool := NewWorkerPool(count, d.logger)
		pool.Start(ctx, runner.Queue(), func(ctx context.Context, id int) error {
			return runner.ProcessOne(ctx, id)
		})
		pools = append(pools, pool)
	}

	// Wait for ctx cancellation
	<-ctx.Done()

	// Wait for all pools to finish
	for _, pool := range pools {
		pool.Wait()
	}

	return nil
}

// CancelTask 查找匹配的 runner 并取消指定任务。
func (d *Dispatcher) CancelTask(taskType string, taskID int) {
	for _, runner := range d.runners {
		if runner.Type() == taskType {
			runner.Cancel(taskID)
			return
		}
	}
}

// Enqueue 查找匹配的 runner 并将任务入队。
func (d *Dispatcher) Enqueue(ctx context.Context, taskType string, taskID int) error {
	for _, runner := range d.runners {
		if runner.Type() == taskType {
			return runner.Queue().Enqueue(ctx, taskID)
		}
	}
	return fmt.Errorf("dispatcher: unknown task type %q", taskType)
}

// QueuePosition 查找匹配的 runner 并返回队列位置信息。
func (d *Dispatcher) QueuePosition(taskType string, taskID int) *QueueInfo {
	for _, runner := range d.runners {
		if runner.Type() == taskType {
			info := runner.Queue().Position(taskID)
			return &info
		}
	}
	return nil
}

// workerCount 根据任务类型返回配置的 worker 数量。
// 配置值由 ValidateServerConfig 保证 >= 1，此处仅做防御性兜底。
func (d *Dispatcher) workerCount(taskType string) int {
	switch taskType {
	case "translation":
		if d.workerCfg.Translation.Count > 0 {
			return d.workerCfg.Translation.Count
		}
	case "sync":
		if d.workerCfg.Sync.Count > 0 {
			return d.workerCfg.Sync.Count
		}
	}
	return 1
}
