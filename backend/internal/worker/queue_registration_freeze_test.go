package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/tasklife"
)

func TestQueueRegistrationFreezeCancelsAcceptedWaitingClaim(t *testing.T) {
	ctx := context.Background()
	life := &tasklife.Coordinator{}
	q := NewQueue(2).WithLifecycle(life, "translation")
	defer q.Close()
	if err := q.Enqueue(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if !life.Busy("translation", 1) {
		t.Fatal("waiting queue entry lacks claim")
	}
	unfreeze, err := life.FreezeRegistration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer unfreeze()
	q.Cancel(1)
	if life.Busy("translation", 1) {
		t.Fatal("cancelled waiting entry retained claim")
	}
	deadline, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	if _, err := q.Dequeue(deadline); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled task claimed=%v", err)
	}
	if err := q.Enqueue(deadline, 2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("new enqueue escaped freeze=%v", err)
	}
	unfreeze()
	if err := q.Enqueue(ctx, 2); err != nil {
		t.Fatal(err)
	}
	execution, err := q.Dequeue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	q.Done(execution)
}
