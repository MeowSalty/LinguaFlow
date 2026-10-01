package worker

import (
	"context"
	"errors"
	"testing"
	"time"
)

func waitQueue(t *testing.T, q *Queue, check func(QueueSnapshot) bool) QueueSnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		q.mu.Lock()
		snapshot := QueueSnapshot{Capacity: q.capacity, Waiting: len(q.waiting), EnqueueWaiters: q.waiters}
		changed := q.changed
		q.mu.Unlock()
		if check(snapshot) {
			return snapshot
		}
		select {
		case <-changed:
		case <-ctx.Done():
			t.Fatalf("queue did not reach expected state: %+v", snapshot)
		}
	}
}

func receiveWorker[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for controlled worker event")
		var zero T
		return zero
	}
}

func TestQueueWaitingAndGeneration(t *testing.T) {
	q := NewQueue(2)
	ctx := context.Background()
	if err := q.Enqueue(ctx, 0); !errors.Is(err, ErrInvalidTaskID) {
		t.Fatalf("invalid ID: %v", err)
	}
	for _, id := range []int{1, 1, 2} {
		if err := q.Enqueue(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if got := q.Snapshot(); got.Waiting != 2 || got.EnqueueWaiters != 0 {
		t.Fatalf("accepted duplicates changed counts: %+v", got)
	}
	old, err := q.Dequeue(ctx)
	if err != nil || old.TaskID != 1 {
		t.Fatalf("claim: %+v %v", old, err)
	}
	if got := q.Position(1); got.Position != -1 || got.Size != 1 {
		t.Fatalf("running work remains waiting: %+v", got)
	}
	q.Cancel(1)
	if old.Context().Err() == nil {
		t.Fatal("running claim was not cancelled")
	}
	if err := q.Enqueue(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if q.Snapshot().Waiting != 1 {
		t.Fatal("cancelled running ID was allowed to execute again before Done")
	}
	q.Done(old)
	q.Cancel(2)
	if err := q.Enqueue(ctx, 1); err != nil {
		t.Fatal(err)
	}
	fresh, err := q.Dequeue(ctx)
	if err != nil || fresh.Generation == old.Generation {
		t.Fatalf("new claim has stale generation: %+v %v", fresh, err)
	}
	q.Done(old)
	q.CancelExecution(old)
	if fresh.Context().Err() != nil {
		t.Fatal("old cancellation affected new execution")
	}
	q.mu.Lock()
	current := q.entries[1]
	q.mu.Unlock()
	if current == nil || current.execution.Generation != fresh.Generation {
		t.Fatal("old Done removed new execution")
	}
	q.Done(fresh)
	q.Done(fresh)
	if got := q.Snapshot(); got.Waiting != 0 || got.EnqueueWaiters != 0 {
		t.Fatalf("unbalanced completion: %+v", got)
	}
}

func TestQueueCapacityWaitCancellationAndDuplicate(t *testing.T) {
	q := NewQueue(1)
	if err := q.Enqueue(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- q.Enqueue(ctx, 2) }()
	waitQueue(t, q, func(s QueueSnapshot) bool { return s.EnqueueWaiters == 1 })
	if err := q.Enqueue(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	if got := q.Snapshot(); got.Waiting != 1 || got.EnqueueWaiters != 1 {
		t.Fatalf("duplicate or waiter counted as waiting: %+v", got)
	}
	cancel()
	if err := receiveWorker(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled enqueue: %v", err)
	}
	if got := q.Snapshot(); got.Waiting != 1 || got.EnqueueWaiters != 0 {
		t.Fatalf("cancelled waiter leaked: %+v", got)
	}
	go func() { done <- q.Enqueue(context.Background(), 2) }()
	waitQueue(t, q, func(s QueueSnapshot) bool { return s.EnqueueWaiters == 1 })
	q.Cancel(1)
	if err := receiveWorker(t, done); err != nil {
		t.Fatal(err)
	}
	claim, err := q.Dequeue(context.Background())
	if err != nil || claim.TaskID != 2 {
		t.Fatalf("stale cancelled FIFO entry consumed: %+v %v", claim, err)
	}
	q.Done(claim)
}

func TestQueueCloseReleasesWaitersAndRunningClaim(t *testing.T) {
	q := NewQueue(1)
	ctx := context.Background()
	_ = q.Enqueue(ctx, 1)
	claim, _ := q.Dequeue(ctx)
	_ = q.Enqueue(ctx, 2)
	done := make(chan error, 1)
	go func() { done <- q.Enqueue(ctx, 3) }()
	waitQueue(t, q, func(s QueueSnapshot) bool { return s.EnqueueWaiters == 1 })
	q.Close()
	q.Close()
	if err := receiveWorker(t, done); !errors.Is(err, ErrQueueClosed) {
		t.Fatalf("close waiter: %v", err)
	}
	if claim.Context().Err() == nil {
		t.Fatal("close did not cancel running claim")
	}
	if _, err := q.Dequeue(ctx); !errors.Is(err, ErrQueueClosed) {
		t.Fatalf("dequeue after close: %v", err)
	}
	q.Done(claim)
	if got := q.Snapshot(); got.Waiting != 0 || got.EnqueueWaiters != 0 {
		t.Fatalf("close leaked queue accounting: %+v", got)
	}
}

func TestQueueValidationDoesNotCountAsCapacityWaiting(t *testing.T) {
	q := NewQueue(1)
	t.Cleanup(q.Close)
	ctx := context.Background()
	if err := q.Enqueue(ctx, 1); err != nil {
		t.Fatal(err)
	}
	checking := make(chan struct{}, 2)
	continueCheck := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := q.enqueueChecked(ctx, 2, func(context.Context, int) (bool, error) {
			checking <- struct{}{}
			<-continueCheck
			return true, nil
		})
		done <- err
	}()
	waitQueue(t, q, func(s QueueSnapshot) bool { return s.EnqueueWaiters == 1 })
	first, err := q.Dequeue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	receiveWorker(t, checking)
	if got := q.Snapshot(); got.EnqueueWaiters != 0 || got.Waiting != 0 {
		t.Fatalf("database validation counted as capacity waiting: %+v", got)
	}
	// Another producer may take the free slot while validation is in flight.
	// The original producer then waits for capacity again and revalidates.
	if err := q.Enqueue(ctx, 3); err != nil {
		t.Fatal(err)
	}
	continueCheck <- struct{}{}
	waitQueue(t, q, func(s QueueSnapshot) bool { return s.EnqueueWaiters == 1 })
	third, err := q.Dequeue(ctx)
	if err != nil || third.TaskID != 3 {
		t.Fatalf("third claim: %+v %v", third, err)
	}
	receiveWorker(t, checking)
	if got := q.Snapshot(); got.EnqueueWaiters != 0 || got.Waiting != 0 {
		t.Fatalf("revalidation counted as capacity waiting: %+v", got)
	}
	continueCheck <- struct{}{}
	if err := receiveWorker(t, done); err != nil {
		t.Fatal(err)
	}
	second, err := q.Dequeue(ctx)
	if err != nil || second.TaskID != 2 {
		t.Fatalf("second claim: %+v %v", second, err)
	}
	q.Done(first)
	q.Done(second)
	q.Done(third)
}
