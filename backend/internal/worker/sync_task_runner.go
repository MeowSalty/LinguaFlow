package worker

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/synctask"
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
	activeTasks map[int]*jobCancelEntry
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
		activeTasks: make(map[int]*jobCancelEntry),
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
	pool := NewWorkerPool(1, r.logger)
	pool.Start(ctx, r.queue, r.ProcessOne)
	pool.Wait()
	return nil
}

// Cancel 通知运行中的同步任务停止。
func (r *SyncTaskRunner) Cancel(taskID int) {
	r.mu.Lock()
	entry, ok := r.activeTasks[taskID]
	r.mu.Unlock()
	if ok {
		r.logger.Info("cancelling running sync task", "task_id", taskID)
		entry.cancel()
	}
}

// Pause 同步任务无暂停语义（短任务，无 LLM 在途请求），恒返回 false。
func (r *SyncTaskRunner) Pause(taskID int) bool {
	return false
}

// Recover 从数据库恢复挂起的任务并返回 ID 列表。
func (r *SyncTaskRunner) Recover(ctx context.Context) ([]int, error) {
	taskIDs, err := r.syncSvc.RecoverPendingJobs(ctx)
	if err != nil {
		return nil, err
	}
	return taskIDs, nil
}

func (r *SyncTaskRunner) PrepareRecovery(ctx context.Context) error {
	return r.syncSvc.PrepareRecovery(ctx)
}

func (r *SyncTaskRunner) PendingTaskIDs(ctx context.Context, afterID, limit int) ([]int, error) {
	return r.syncSvc.PendingTaskIDs(ctx, afterID, limit)
}

func (r *SyncTaskRunner) TaskStatus(ctx context.Context, taskID int) (string, error) {
	row, err := r.client.SyncTask.Query().Where(synctask.IDEQ(taskID)).Select(synctask.FieldStatus).Only(ctx)
	if ent.IsNotFound(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return row.Status, nil
}

// processTask 处理单个术语同步任务。
func (r *SyncTaskRunner) processTask(ctx context.Context, taskID int) error {
	// 创建 per-task context，支持外部取消
	taskCtx, taskCancel := context.WithCancel(ctx)
	defer taskCancel()

	// 注册到 activeTasks，使 Cancel 能触发取消
	entry := &jobCancelEntry{cancel: taskCancel}
	r.mu.Lock()
	r.activeTasks[taskID] = entry
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		if r.activeTasks[taskID] == entry {
			delete(r.activeTasks, taskID)
		}
		r.mu.Unlock()
	}()

	// 加载任务获取受影响的 ResourceIDs
	task, err := r.client.SyncTask.Query().Where(synctask.IDEQ(taskID)).Select(synctask.FieldStatus).Only(taskCtx)
	if err != nil {
		return fmt.Errorf("load sync task for resource lock: %w", err)
	}
	if task.Status != service.SyncTaskStatusPending {
		return nil
	}

	// Read the immutable lock membership in ID pages. Keep every acquired lock
	// until execution finishes, just as the published runner did.
	var releases []func()
	defer func() {
		for _, rel := range releases {
			rel()
		}
	}()
	for after := 0; ; {
		resourceIDs, err := r.syncSvc.SyncTaskResourceIDs(taskCtx, taskID, after, 100)
		if err != nil {
			return r.failTask(taskCtx, taskID, err)
		}
		if len(resourceIDs) == 0 {
			break
		}
		for _, resourceID := range resourceIDs {
			if r.resMutex != nil {
				release, err := r.resMutex.Acquire(taskCtx, resourceID)
				if err != nil {
					return fmt.Errorf("acquire resource lock %d: %w", resourceID, err)
				}
				releases = append(releases, release)
			}
		}
		after = resourceIDs[len(resourceIDs)-1]
	}

	return r.syncSvc.ExecuteSyncTask(taskCtx, taskID)
}

// failTask 标记同步任务为 failed 并返回错误。
func (r *SyncTaskRunner) failTask(ctx context.Context, taskID int, err error) error {
	r.logger.Error("sync task failed", "task_id", taskID, "error", err)
	if updateErr := r.syncSvc.FailSyncTask(ctx, taskID, err); updateErr != nil {
		r.logger.Error("failed to mark sync task as failed", "task_id", taskID, "error", updateErr)
	}
	return err
}
