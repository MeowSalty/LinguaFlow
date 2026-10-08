package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/tasklife"
)

func TestQueueLifecycleCoversAdmissionAndCancelledDrain(t *testing.T) {
	var lifecycle tasklife.Coordinator
	q := NewQueue(1).WithLifecycle(&lifecycle, "translation")
	checking, allow := make(chan struct{}), make(chan struct{})
	enqueued := make(chan error, 1)
	go func() {
		_, err := q.enqueueChecked(context.Background(), 1, func(context.Context, int) (bool, error) {
			close(checking)
			<-allow
			return true, nil
		})
		enqueued <- err
	}()
	<-checking
	if !lifecycle.Busy("translation", 1) {
		t.Fatal("admission is invisible to deletion")
	}
	guard, err := lifecycle.Lock(context.Background(), "translation", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !guard.Active() {
		t.Fatal("guard missed admission")
	}
	guard.Release()
	close(allow)
	if err := <-enqueued; err != nil {
		t.Fatal(err)
	}
	execution, err := q.Dequeue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	q.Cancel(1)
	if !lifecycle.Busy("translation", 1) {
		t.Fatal("cancellation released active worker")
	}
	q.Done(execution)
	if lifecycle.Busy("translation", 1) {
		t.Fatal("Done retained claim")
	}
	if err := q.Enqueue(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	q.Done(execution)
	if !lifecycle.Busy("translation", 1) {
		t.Fatal("old Done released new admission")
	}
	q.Close()
	if lifecycle.Busy("translation", 1) {
		t.Fatal("queue close retained waiting claim")
	}
}

func TestQueueLifecycleDeletionGuardFencesAdmission(t *testing.T) {
	var lifecycle tasklife.Coordinator
	q := NewQueue(1).WithLifecycle(&lifecycle, "translation")
	guard, err := lifecycle.Lock(context.Background(), "translation", 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := q.Enqueue(ctx, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("admission passed deletion guard: %v", err)
	}
	if guard.Active() {
		t.Fatal("blocked admission claimed task")
	}
	guard.Release()
	accepted, err := q.enqueueChecked(context.Background(), 1, func(context.Context, int) (bool, error) { return false, nil })
	if err != nil || accepted || lifecycle.Busy("translation", 1) {
		t.Fatalf("deleted task rediscovered: %v %v", accepted, err)
	}
}
