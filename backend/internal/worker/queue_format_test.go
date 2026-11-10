package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tasklife"
)

func TestQueueExecutionFormatCheckPrecedesClaimAndGeneration(t *testing.T) {
	ctx := context.Background()
	lifecycle := &tasklife.Coordinator{}
	q := NewQueue(1).WithLifecycle(lifecycle, "translation").WithDispatchCheck(func(context.Context, int) error { return execution.ErrUnsupportedExecutionFormat })
	if err := q.Enqueue(ctx, 1); !errors.Is(err, execution.ErrUnsupportedExecutionFormat) {
		t.Fatalf("old execution queued: %v", err)
	}
	if q.next != 0 || len(q.entries) != 0 || q.Snapshot().EnqueueWaiters != 0 {
		t.Fatal("format rejection acquired queue ownership")
	}
	guard, err := lifecycle.Lock(ctx, "translation", 1)
	if err != nil {
		t.Fatal(err)
	}
	if guard.Active() {
		t.Fatal("format rejection retained a lifecycle claim")
	}
	guard.Release()
	q.WithDispatchCheck(nil)
	if err := q.Enqueue(ctx, 1); err != nil {
		t.Fatal(err)
	}
	q.Close()
}
