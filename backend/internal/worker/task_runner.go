package worker

import "context"

// TaskRunner 定义任务执行器的统一接口。
// 每种任务类型（translation、sync）实现此接口。
type TaskRunner interface {
	// Type 返回任务类型标识，如 "translation"、"sync"。
	Type() string

	// Run 从队列中取任务并执行，直到 ctx 取消或队列关闭。
	Run(ctx context.Context) error

	// ProcessOne 处理单个任务，不负责 Dequeue/Done。
	// 供 WorkerPool 在 Dequeue 后调用，由 WorkerPool 统一管理队列生命周期。
	ProcessOne(ctx context.Context, taskID int) error

	// Cancel 通知运行中的任务停止。
	Cancel(taskID int)

	// Pause 通知运行中的任务优雅排空（等待在途 LLM 请求返回后冻结）。
	// 返回 false 表示任务未在运行（由 service 层处理 pending 态暂停）。
	// 无暂停语义的 Runner（如 sync）实现为 no-op 返回 false。
	Pause(taskID int) bool

	// Recover 从数据库恢复挂起的任务并重新入队，返回恢复的任务 ID 列表。
	Recover(ctx context.Context) ([]int, error)

	// Queue 返回此 Runner 的任务队列。
	Queue() *Queue
}
