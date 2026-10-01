package worker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

func TestDispatcherLateCancellationDoesNotCancelRetriedExecution(t *testing.T) {
	for _, currentStatus := range []string{"pending", "running"} {
		t.Run(currentStatus, func(t *testing.T) {
			r := newDispatcherTestRunner("translation", 1)
			r.status = func(context.Context, int) (string, error) { return currentStatus, nil }
			d := NewDispatcher(nil, nil, config.WorkerConfig{}, r)
			q := r.Queue()
			t.Cleanup(q.Close)
			_ = q.Enqueue(context.Background(), 7)
			old, _ := q.Dequeue(context.Background())
			q.CancelExecution(old)
			q.Done(old)
			_ = q.Enqueue(context.Background(), 7)
			fresh, _ := q.Dequeue(context.Background())
			// The older cancel handler only now reaches its notification,
			// after retry has committed and installed a new execution.
			d.CancelTask("translation", 7)
			if fresh.Context().Err() != nil {
				t.Fatal("late notification cancelled the retried execution")
			}
			q.Done(fresh)
		})
	}
}

func TestDispatcherCancellationCapturesBeforeStateRead(t *testing.T) {
	r := newDispatcherTestRunner("translation", 1)
	queryEntered := make(chan struct{})
	queryReturn := make(chan struct{})
	r.status = func(ctx context.Context, _ int) (string, error) {
		close(queryEntered)
		select {
		case <-queryReturn:
			return "cancelled", nil // Result read before concurrent retry.
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	d := NewDispatcher(nil, nil, config.WorkerConfig{}, r)
	q := r.Queue()
	t.Cleanup(q.Close)
	_ = q.Enqueue(context.Background(), 7)
	old, _ := q.Dequeue(context.Background())
	done := make(chan struct{})
	go func() { d.CancelTask("translation", 7); close(done) }()
	receiveWorker(t, queryEntered)
	// Swap generations while the durable cancellation check is in flight.
	q.Done(old)
	_ = q.Enqueue(context.Background(), 7)
	fresh, _ := q.Dequeue(context.Background())
	close(queryReturn)
	receiveWorker(t, done)
	if fresh.Context().Err() != nil {
		t.Fatal("state check finished by cancelling a different generation")
	}
	q.Done(fresh)
}

func TestDispatcherConfirmedCancellationCoversAdmissionAndClaim(t *testing.T) {
	for _, phase := range []string{"enqueuing", "waiting", "running"} {
		t.Run(phase, func(t *testing.T) {
			r := newDispatcherTestRunner("sync", 1)
			r.status = func(context.Context, int) (string, error) { return "cancelled", nil }
			d := NewDispatcher(nil, nil, config.WorkerConfig{}, r)
			q := r.Queue()
			t.Cleanup(q.Close)
			var claim Execution
			var enqueued chan error
			if phase == "enqueuing" {
				_ = q.Enqueue(context.Background(), 1)
				enqueued = make(chan error, 1)
				go func() { enqueued <- q.Enqueue(context.Background(), 2) }()
				waitQueue(t, q, func(s QueueSnapshot) bool { return s.EnqueueWaiters == 1 })
			} else {
				_ = q.Enqueue(context.Background(), 2)
				if phase == "running" {
					claim, _ = q.Dequeue(context.Background())
				}
			}
			d.CancelTask("sync", 2)
			if enqueued != nil {
				if err := receiveWorker(t, enqueued); !errors.Is(err, context.Canceled) {
					t.Fatalf("admission cancellation: %v", err)
				}
				q.Cancel(1)
			}
			if phase == "running" {
				if claim.Context().Err() == nil {
					t.Fatal("claim was not cancelled")
				}
				q.Done(claim)
			}
			if got := q.Snapshot(); got.Waiting != 0 || got.EnqueueWaiters != 0 {
				t.Fatalf("cancelled entry retained queue counts: %+v", got)
			}
		})
	}
}

func TestDispatcherCancellationStateCheckFailureIsSafeAndBounded(t *testing.T) {
	for _, fail := range []string{"database_error", "timeout"} {
		t.Run(fail, func(t *testing.T) {
			r := newDispatcherTestRunner("translation", 1)
			deadlineSeen := make(chan time.Time, 1)
			r.status = func(ctx context.Context, _ int) (string, error) {
				deadline, _ := ctx.Deadline()
				deadlineSeen <- deadline
				if fail == "timeout" {
					<-ctx.Done()
					return "", ctx.Err()
				}
				return "", errors.New("controlled database failure")
			}
			d := NewDispatcher(nil, nil, config.WorkerConfig{}, r)
			q := r.Queue()
			t.Cleanup(q.Close)
			_ = q.Enqueue(context.Background(), 7)
			claim, _ := q.Dequeue(context.Background())
			done := make(chan struct{})
			started := time.Now()
			go func() { d.CancelTask("translation", 7); close(done) }()
			deadline := receiveWorker(t, deadlineSeen)
			if deadline.IsZero() || deadline.After(started.Add(taskStatusTimeout+time.Second)) {
				t.Fatalf("unbounded state read: %v", deadline)
			}
			receiveWorker(t, done)
			if claim.Context().Err() != nil {
				t.Fatal("failed state verification cancelled execution")
			}
			q.Done(claim)
		})
	}
}

func TestDispatcherStaleDiscoveryAfterCancellationIsNotWaiting(t *testing.T) {
	for _, kind := range []string{"translation", "sync"} {
		t.Run(kind, func(t *testing.T) {
			r := newDispatcherTestRunner(kind, 1)
			r.add(7)
			var status atomic.Value
			status.Store("pending")
			r.status = func(context.Context, int) (string, error) { return status.Load().(string), nil }
			pageRead := make(chan struct{})
			returnPage := make(chan struct{})
			r.pageRead = func(ctx context.Context, _ []int) {
				close(pageRead)
				select {
				case <-returnPage:
				case <-ctx.Done():
				}
			}
			d := NewDispatcher(nil, nil, config.WorkerConfig{}, r)
			t.Cleanup(r.Queue().Close)
			done := make(chan error, 1)
			go func() { done <- d.discover(context.Background(), d.runners[0], false) }()
			receiveWorker(t, pageRead)
			status.Store("cancelled")
			// No queue identity exists yet, so this notification cannot remove
			// anything. The old page must validate before becoming waiting.
			d.CancelTask(kind, 7)
			close(returnPage)
			if err := receiveWorker(t, done); err != nil {
				t.Fatal(err)
			}
			if got := r.Queue().Snapshot(); got.Waiting != 0 || got.EnqueueWaiters != 0 {
				t.Fatalf("cancelled stale page admitted: %+v", got)
			}
			if _, exists := r.Queue().Capture(7); exists {
				t.Fatal("rejected admission retained identity")
			}
		})
	}
}

func TestDispatcherCancellationDuringAdmissionValidation(t *testing.T) {
	r := newDispatcherTestRunner("translation", 1)
	queryEntered := make(chan struct{})
	returnOldStatus := make(chan struct{})
	var calls atomic.Int32
	r.status = func(ctx context.Context, _ int) (string, error) {
		if calls.Add(1) == 1 {
			close(queryEntered)
			select {
			case <-returnOldStatus:
				return "pending", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return "cancelled", nil
	}
	d := NewDispatcher(nil, nil, config.WorkerConfig{}, r)
	t.Cleanup(r.Queue().Close)
	done := make(chan error, 1)
	go func() { done <- d.Enqueue(context.Background(), "translation", 7) }()
	receiveWorker(t, queryEntered)
	if _, exists := r.Queue().Capture(7); !exists {
		t.Fatal("state read started before registering admission identity")
	}
	if got := r.Queue().Snapshot(); got.Waiting != 0 {
		t.Fatalf("unvalidated task counted as waiting: %+v", got)
	}
	d.CancelTask("translation", 7)
	close(returnOldStatus)
	if err := receiveWorker(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("stale admission survived cancellation: %v", err)
	}
	if got := r.Queue().Snapshot(); got.Waiting != 0 || got.EnqueueWaiters != 0 {
		t.Fatalf("cancelled validation leaked counts: %+v", got)
	}
}
