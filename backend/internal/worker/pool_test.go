package worker

import (
	"context"
	"errors"
	"testing"
)

func TestWorkerPoolBusyIncludesCancellationDrain(t *testing.T) {
	q := NewQueue(2)
	p := NewWorkerPool(1, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan context.Context, 1)
	drain := make(chan struct{})
	_ = q.Enqueue(ctx, 1)
	p.Start(ctx, q, func(taskCtx context.Context, _ int) error {
		entered <- taskCtx
		<-drain
		return errors.New("controlled failure")
	})
	taskCtx := receiveWorker(t, entered)
	if got := p.Snapshot(); got.Alive != 1 || got.Busy != 1 || got.Capacity != 1 {
		t.Fatalf("busy snapshot: %+v", got)
	}
	if q.Snapshot().Waiting != 0 {
		t.Fatal("claimed work remains queued")
	}
	q.Cancel(1)
	if taskCtx.Err() == nil || workerLifetime(taskCtx).Err() != nil {
		t.Fatal("execution cancellation affected worker lifetime")
	}
	if p.Snapshot().Busy != 1 {
		t.Fatal("cancelled draining worker became idle too early")
	}
	cancel()
	if workerLifetime(taskCtx).Err() == nil {
		t.Fatal("shutdown did not cancel cleanup lifetime")
	}
	close(drain)
	p.Wait()
	if got := p.Snapshot(); got.Alive != 0 || got.Busy != 0 {
		t.Fatalf("workers not released: %+v", got)
	}
}
