package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

// SyncTaskRunner 术语同步任务执行器，实现 TaskRunner 接口。
type SyncTaskRunner struct {
	logger   *slog.Logger
	client   *ent.Client
	syncSvc  *service.GlossarySyncService
	queue    *Queue
	resMutex *ResourceMutex

	// per-task 取消注册表：taskID → cancel 函数
	mu          sync.Mutex
	activeTasks map[int]context.CancelFunc
}

// NewSyncTaskRunner 创建一个新的术语同步任务执行器。
func NewSyncTaskRunner(
	logger *slog.Logger,
	client *ent.Client,
	syncSvc *service.GlossarySyncService,
	queue *Queue,
	resMutex *ResourceMutex,
) *SyncTaskRunner {
	if logger == nil {
		logger = slog.Default()
	}
	return &SyncTaskRunner{
		logger:      logger,
		client:      client,
		syncSvc:     syncSvc,
		queue:       queue,
		resMutex:    resMutex,
		activeTasks: make(map[int]context.CancelFunc),
	}
}

// Type 返回任务类型标识。
func (r *SyncTaskRunner) Type() string {
	return "sync"
}

// Queue 返回此 Runner 的任务队列。
func (r *SyncTaskRunner) Queue() *Queue {
	return r.queue
}

// ProcessOne 处理单个术语同步任务，不负责 Dequeue/Done。
func (r *SyncTaskRunner) ProcessOne(ctx context.Context, taskID int) error {
	return r.processTask(ctx, taskID)
}

// Run 从队列中取任务并执行，直到 ctx 取消。
func (r *SyncTaskRunner) Run(ctx context.Context) error {
	for {
		taskID, err := r.queue.Dequeue(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
		if err := r.processTask(ctx, taskID); err != nil {
			r.logger.Error("sync task worker: process task failed", "task_id", taskID, "err", err)
		}
		r.queue.Done(taskID)
	}
}

// Cancel 通知运行中的同步任务停止。
func (r *SyncTaskRunner) Cancel(taskID int) {
	r.mu.Lock()
	cancel, ok := r.activeTasks[taskID]
	r.mu.Unlock()
	if ok {
		r.logger.Info("cancelling running sync task", "task_id", taskID)
		cancel()
	}
}

// Recover 从数据库恢复挂起的任务并返回 ID 列表。
func (r *SyncTaskRunner) Recover(ctx context.Context) ([]int, error) {
	taskIDs, err := r.syncSvc.RecoverPendingJobs(ctx)
	if err != nil {
		return nil, err
	}
	return taskIDs, nil
}

// processTask 处理单个术语同步任务。
func (r *SyncTaskRunner) processTask(ctx context.Context, taskID int) error {
	// 创建 per-task context，支持外部取消
	taskCtx, taskCancel := context.WithCancel(ctx)
	defer taskCancel()

	// 注册到 activeTasks，使 Cancel 能触发取消
	r.mu.Lock()
	r.activeTasks[taskID] = taskCancel
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.activeTasks, taskID)
		r.mu.Unlock()
	}()

	// 加载任务获取受影响的 ResourceIDs
	task, err := r.client.SyncTask.Get(taskCtx, taskID)
	if err != nil {
		return fmt.Errorf("load sync task for resource lock: %w", err)
	}

	// 解析 resource_ids 并按顺序获取锁（防止死锁）
	var resourceIDs []int
	if task.ResourceIds == "" {
		return r.failTask(taskCtx, taskID, fmt.Errorf("sync task %d has no resource_ids", taskID))
	}
	if err := json.Unmarshal([]byte(task.ResourceIds), &resourceIDs); err != nil {
		return r.failTask(taskCtx, taskID, fmt.Errorf("sync task %d: parse resource_ids: %w", taskID, err))
	}
	sort.Ints(resourceIDs)

	// 获取所有受影响 Resource 的锁
	releases := make([]func(), 0, len(resourceIDs))
	for _, resourceID := range resourceIDs {
		if r.resMutex != nil {
			release, err := r.resMutex.Acquire(taskCtx, resourceID)
			if err != nil {
				// 释放已获取的锁
				for _, rel := range releases {
					rel()
				}
				return fmt.Errorf("acquire resource lock %d: %w", resourceID, err)
			}
			releases = append(releases, release)
		}
	}
	defer func() {
		for _, rel := range releases {
			rel()
		}
	}()

	return r.syncSvc.ExecuteSyncTask(taskCtx, taskID)
}

// failTask 标记同步任务为 failed 并返回错误。
func (r *SyncTaskRunner) failTask(ctx context.Context, taskID int, err error) error {
	r.logger.Error("sync task failed", "task_id", taskID, "error", err)
	if updateErr := r.client.SyncTask.UpdateOneID(taskID).
		SetStatus(service.SyncTaskStatusFailed).
		SetError(err.Error()).
		Exec(ctx); updateErr != nil {
		r.logger.Error("failed to mark sync task as failed", "task_id", taskID, "error", updateErr)
	}
	return err
}
