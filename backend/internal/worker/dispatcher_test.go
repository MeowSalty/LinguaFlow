package worker

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

type dispatcherTestRunner struct {
	taskType  string
	queue     *Queue
	mu        sync.Mutex
	pending   map[int]bool
	prepare   func(context.Context) error
	process   func(context.Context, int) error
	status    func(context.Context, int) (string, error)
	pageRead  func(context.Context, []int)
	processed chan int
	pages     chan []int
}

func newDispatcherTestRunner(taskType string, capacity int) *dispatcherTestRunner {
	return &dispatcherTestRunner{taskType: taskType, queue: NewQueue(capacity), pending: make(map[int]bool), processed: make(chan int, 512), pages: make(chan []int, 512)}
}

func (r *dispatcherTestRunner) Type() string   { return r.taskType }
func (r *dispatcherTestRunner) Queue() *Queue  { return r.queue }
func (r *dispatcherTestRunner) Cancel(int)     {}
func (r *dispatcherTestRunner) Pause(int) bool { return false }
func (r *dispatcherTestRunner) TaskStatus(ctx context.Context, id int) (string, error) {
	if r.status != nil {
		return r.status(ctx, id)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending[id] {
		return "pending", nil
	}
	return "completed", nil
}
func (r *dispatcherTestRunner) PrepareRecovery(ctx context.Context) error {
	if r.prepare != nil {
		return r.prepare(ctx)
	}
	return nil
}
func (r *dispatcherTestRunner) PendingTaskIDs(ctx context.Context, afterID, limit int) ([]int, error) {
	r.mu.Lock()
	ids := make([]int, 0, len(r.pending))
	for id := range r.pending {
		if id > afterID {
			ids = append(ids, id)
		}
	}
	r.mu.Unlock()
	sort.Ints(ids)
	if len(ids) > limit {
		ids = ids[:limit]
	}
	if r.pageRead != nil {
		r.pageRead(ctx, ids)
	}
	r.pages <- append([]int(nil), ids...)
	return ids, nil
}
func (r *dispatcherTestRunner) ProcessOne(ctx context.Context, id int) error {
	r.mu.Lock()
	delete(r.pending, id)
	r.mu.Unlock()
	r.processed <- id
	if r.process != nil {
		return r.process(ctx, id)
	}
	return nil
}
func (r *dispatcherTestRunner) add(id int) {
	r.mu.Lock()
	r.pending[id] = true
	r.mu.Unlock()
}

func runTestDispatcher(t *testing.T, d *Dispatcher) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- d.Run(context.Background()) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := d.Shutdown(ctx); err != nil {
			t.Errorf("cleanup dispatcher: %v", err)
		}
	})
	return done
}

func TestDispatcherRecoveryBeyondCapacity(t *testing.T) {
	r := newDispatcherTestRunner("translation", 1)
	for id := 1; id <= 300; id++ {
		r.add(id)
	}
	d := NewDispatcher(nil, nil, config.WorkerConfig{}, r)
	runDone := runTestDispatcher(t, d)
	seen := make(map[int]bool)
	for len(seen) < 300 {
		id := receiveWorker(t, r.processed)
		if seen[id] {
			t.Fatalf("task %d executed twice", id)
		}
		seen[id] = true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := receiveWorker(t, runDone); err != nil {
		t.Fatal(err)
	}
	snapshot := d.Snapshot()[0]
	if snapshot.State != "stopped" || snapshot.RecoveredTotal != 300 || snapshot.RecoveryErrorsTotal != 0 {
		t.Fatalf("recovery snapshot: %+v", snapshot)
	}
	if *snapshot.QueueWaiting != 0 || *snapshot.WorkersBusy != 0 || *snapshot.WorkersAlive != 0 {
		t.Fatalf("shutdown leaked active counts: %+v", snapshot)
	}
}

func TestDispatcherPrepareBeforeConsumersAndIndependentFailure(t *testing.T) {
	broken := newDispatcherTestRunner("translation", 1)
	broken.add(1)
	prepareEntered := make(chan struct{}, 1)
	broken.prepare = func(context.Context) error {
		select {
		case prepareEntered <- struct{}{}:
		default:
		}
		return errors.New("controlled recovery failure")
	}
	healthy := newDispatcherTestRunner("sync", 1)
	healthy.add(2)
	d := NewDispatcher(nil, nil, config.WorkerConfig{}, broken, healthy)
	runDone := runTestDispatcher(t, d)
	receiveWorker(t, prepareEntered)
	if id := receiveWorker(t, healthy.processed); id != 2 {
		t.Fatal(id)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	receiveWorker(t, runDone)
	select {
	case <-broken.processed:
		t.Fatal("runner consumed work after failed preparation")
	default:
	}
	snapshots := d.Snapshot()
	if snapshots[0].RecoveryErrorsTotal != 1 || snapshots[0].WorkersAlive != nil {
		t.Fatalf("failed recovery was hidden: %+v", snapshots[0])
	}
	if snapshots[1].TaskType != "glossary_sync" {
		t.Fatalf("internal type leaked: %+v", snapshots[1])
	}
}

func TestDispatcherRediscoveryRepairsMissedDelivery(t *testing.T) {
	for _, taskType := range []string{"translation", "sync"} {
		t.Run(taskType, func(t *testing.T) {
			r := newDispatcherTestRunner(taskType, 1)
			d := NewDispatcher(nil, nil, config.WorkerConfig{}, r)
			runTestDispatcher(t, d)
			if ids := receiveWorker(t, r.pages); len(ids) != 0 {
				t.Fatalf("initial page: %v", ids)
			}
			// A persisted task remains accepted when the immediate request-bound
			// attempt fails. No Notify is sent: the periodic scan must repair it.
			r.add(12)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := d.Enqueue(ctx, taskType, 12); !errors.Is(err, context.Canceled) {
				t.Fatalf("delivery failure: %v", err)
			}
			if id := receiveWorker(t, r.processed); id != 12 {
				t.Fatal(id)
			}
		})
	}
}

func TestDispatcherPreparationGatesQueuedWork(t *testing.T) {
	r := newDispatcherTestRunner("sync", 1)
	r.add(1)
	entered := make(chan struct{})
	allow := make(chan struct{})
	r.prepare = func(ctx context.Context) error {
		close(entered)
		select {
		case <-allow:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	d := NewDispatcher(nil, nil, config.WorkerConfig{}, r)
	if err := d.Enqueue(context.Background(), "sync", 1); err != nil {
		t.Fatal(err)
	}
	runTestDispatcher(t, d)
	receiveWorker(t, entered)
	s := d.Snapshot()[0]
	if s.State != "recovering" || s.WorkersAlive != nil || *s.QueueWaiting != 1 {
		t.Fatalf("preparation snapshot: %+v", s)
	}
	close(allow)
	if id := receiveWorker(t, r.processed); id != 1 {
		t.Fatal(id)
	}
}

func TestDispatcherRediscoveryAfterResumeBeforeOldDone(t *testing.T) {
	r := newDispatcherTestRunner("translation", 1)
	r.add(7)
	oldReturning := make(chan struct{})
	var release sync.Once
	var calls atomic.Int32
	r.process = func(context.Context, int) error {
		if calls.Add(1) == 1 {
			<-oldReturning
		}
		return nil
	}
	d := NewDispatcher(nil, nil, config.WorkerConfig{}, r)
	runTestDispatcher(t, d)
	t.Cleanup(func() { release.Do(func() { close(oldReturning) }) })
	if id := receiveWorker(t, r.processed); id != 7 {
		t.Fatal(id)
	}
	// Resume has committed pending, but the old ProcessOne is still unwinding.
	// The immediate enqueue is a duplicate and must not run a second worker.
	r.add(7)
	if err := d.Enqueue(context.Background(), "translation", 7); err != nil {
		t.Fatal(err)
	}
	if q := r.queue.Snapshot(); q.Waiting != 0 {
		t.Fatalf("overlapping execution admitted: %+v", q)
	}
	release.Do(func() { close(oldReturning) })
	// No additional user action/notification: durable rediscovery repairs it.
	if id := receiveWorker(t, r.processed); id != 7 {
		t.Fatal(id)
	}
}

func TestDispatcherShutdownReportsOccupiedWorker(t *testing.T) {
	r := newDispatcherTestRunner("translation", 1)
	r.add(1)
	blocked := make(chan struct{})
	var release sync.Once
	r.process = func(context.Context, int) error { <-blocked; return nil }
	d := NewDispatcher(nil, nil, config.WorkerConfig{}, r)
	runDone := runTestDispatcher(t, d)
	t.Cleanup(func() { release.Do(func() { close(blocked) }) })
	receiveWorker(t, r.processed)
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.Shutdown(expired); !errors.Is(err, context.Canceled) {
		t.Fatalf("unbounded/silent shutdown: %v", err)
	}
	s := d.Snapshot()[0]
	if s.State != "stopping" || *s.WorkersBusy != 1 {
		t.Fatalf("premature stopped snapshot: %+v", s)
	}
	release.Do(func() { close(blocked) })
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if err := d.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	receiveWorker(t, runDone)
	if d.Snapshot()[0].State != "stopped" {
		t.Fatal("worker did not stop")
	}
}

func TestDispatcherShutdownBeforeRun(t *testing.T) {
	r := newDispatcherTestRunner("sync", 1)
	d := NewDispatcher(nil, nil, config.WorkerConfig{}, r)
	d.Notify("sync")
	if err := d.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := d.Run(context.Background()); err == nil {
		t.Fatal("dispatcher restarted after shutdown")
	}
	if err := d.Enqueue(context.Background(), "sync", 1); !errors.Is(err, ErrQueueClosed) {
		t.Fatal(err)
	}
}
