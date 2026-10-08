package worker

import "context"

// TaskRunner separates startup reconciliation from read-only rediscovery.
// Preparation must finish before consumers start; discovery never resets work.
type TaskRunner interface {
	Type() string
	ProcessOne(ctx context.Context, taskID int) error
	Cancel(taskID int)
	Pause(taskID int) bool
	PrepareRecovery(ctx context.Context) error
	PendingTaskIDs(ctx context.Context, afterID, limit int) ([]int, error)
	Queue() *Queue
}

// taskStatusReader is the minimal durable-state capability used by Dispatcher.
// It is separate from TaskRunner to keep existing constructor interfaces stable.
// Missing tasks return an empty status; implementations project only that field.
type taskStatusReader interface {
	TaskStatus(ctx context.Context, taskID int) (string, error)
}
