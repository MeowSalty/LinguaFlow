package worker

import (
	"context"
	"testing"
)

// TestQueue_CancelledJobExcludedFromPosition 锁定 bug 修复：
// 已取消（Done）的任务不得继续占据队列位置，否则后入队的任务会
// 显示"排队中，前面有 N 个任务"，N 为残留的已取消任务数。
func TestQueue_CancelledJobExcludedFromPosition(t *testing.T) {
	q := NewQueue(8)
	ctx := context.Background()

	// 入队任务 1、2、3
	for _, id := range []int{1, 2, 3} {
		if err := q.Enqueue(ctx, id); err != nil {
			t.Fatalf("Enqueue(%d): %v", id, err)
		}
	}

	// 任务 1、2 被取消（等价于 CancelTask 调用 Done）
	q.Done(1)
	q.Done(2)

	// 仅剩任务 3，因此其位置应为 1、队列大小应为 1
	info := q.Position(3)
	if info.Position != 1 {
		t.Errorf("after cancelling 1 and 2, Position(3) = %d, want 1", info.Position)
	}
	if info.Size != 1 {
		t.Errorf("after cancelling 1 and 2, Size = %d, want 1", info.Size)
	}

	// 再入队任务 4，它不应排在被取消的任务之后
	if err := q.Enqueue(ctx, 4); err != nil {
		t.Fatalf("Enqueue(4): %v", err)
	}
	info4 := q.Position(4)
	if info4.Position != 2 {
		t.Errorf("Position(4) = %d, want 2 (cancelled jobs must not count)", info4.Position)
	}
	if info4.Size != 2 {
		t.Errorf("Size = %d, want 2", info4.Size)
	}
}

// TestQueue_DoneIdempotent 确保 Done 对未在队列中的任务安全，
// 这是 CancelTask 在"任务已出队/已完成"时仍调用 Done 的前提。
func TestQueue_DoneIdempotent(t *testing.T) {
	q := NewQueue(4)
	ctx := context.Background()

	// 对从未入队的任务调用 Done 不应 panic，也不应改变状态
	q.Done(999)

	if err := q.Enqueue(ctx, 1); err != nil {
		t.Fatalf("Enqueue(1): %v", err)
	}
	// 重复 Done 同一任务
	q.Done(1)
	q.Done(1)

	info := q.Position(1)
	if info.Position != -1 {
		t.Errorf("Position(1) after Done = %d, want -1 (removed)", info.Position)
	}
}
