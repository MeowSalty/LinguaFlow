package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

const (
	pendingPageSize       = 128
	pendingPollInterval   = 2 * time.Second
	pendingQueryTimeout   = 5 * time.Second
	pendingEnqueueTimeout = time.Second
	taskStatusTimeout     = 2 * time.Second
)

// RunnerSnapshot contains only instance counts, never task or tenant identities.
// Null counts denote components which have not initialized.
type RunnerSnapshot struct {
	TaskType            string `json:"task_type"`
	State               string `json:"state"`
	RecoveredTotal      int64  `json:"recovered_total"`
	RecoveryErrorsTotal int64  `json:"recovery_errors_total"`
	QueueCapacity       *int   `json:"queue_capacity"`
	QueueWaiting        *int   `json:"queue_waiting"`
	EnqueueWaiters      *int   `json:"enqueue_waiters"`
	WorkerCapacity      *int   `json:"worker_capacity"`
	WorkersAlive        *int   `json:"workers_alive"`
	WorkersBusy         *int   `json:"workers_busy"`
}

type runnerRuntime struct {
	runner         TaskRunner
	notify         chan struct{}
	mu             sync.Mutex
	state          string
	recovered      int64
	recoveryErrors int64
	pool           *WorkerPool
}

func (r *runnerRuntime) setState(state string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if (r.state == "stopping" || r.state == "stopped") && state != "stopped" {
		return
	}
	r.state = state
}

func (r *runnerRuntime) recoveryFailed() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recoveryErrors++
	if r.state != "stopping" && r.state != "stopped" {
		r.state = "degraded"
	}
}

// Dispatcher owns independent runner lifecycles. Durable pending discovery
// repairs missed notifications without resetting tasks already being executed.
type Dispatcher struct {
	logger    *slog.Logger
	runners   []*runnerRuntime
	workerCfg config.WorkerConfig
	mu        sync.Mutex
	started   bool
	stopping  bool
	cancel    context.CancelFunc
	done      chan struct{}
}

func NewDispatcher(logger *slog.Logger, _ *ResourceMutex, workerCfg config.WorkerConfig, runners ...TaskRunner) *Dispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	d := &Dispatcher{logger: logger, workerCfg: workerCfg, done: make(chan struct{})}
	for _, runner := range runners {
		d.runners = append(d.runners, &runnerRuntime{runner: runner, notify: make(chan struct{}, 1), state: "starting"})
	}
	return d
}

// Run is single-use. Each runner finishes recovery preparation before starting
// its consumers; recovered IDs are streamed only after those consumers start.
func (d *Dispatcher) Run(ctx context.Context) error {
	d.mu.Lock()
	if d.started || d.stopping {
		d.mu.Unlock()
		return errors.New("dispatcher cannot be started more than once or after shutdown")
	}
	d.started = true
	runCtx, cancel := context.WithCancel(ctx)
	d.cancel = cancel
	d.mu.Unlock()
	defer cancel()

	var wg sync.WaitGroup
	for _, runtime := range d.runners {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.runRunner(runCtx, runtime)
		}()
	}
	<-runCtx.Done()
	d.stop()
	wg.Wait()
	close(d.done)
	return nil
}

func (d *Dispatcher) runRunner(ctx context.Context, runtime *runnerRuntime) {
	defer runtime.setState("stopped")
	for {
		if ctx.Err() != nil {
			return
		}
		runtime.setState("recovering")
		if err := runtime.runner.PrepareRecovery(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			runtime.recoveryFailed()
			d.logger.Error("dispatcher recovery preparation failed", "type", runtime.runner.Type(), "err", err)
			if !waitForDiscovery(ctx, runtime.notify) {
				return
			}
			continue
		}
		break
	}
	if ctx.Err() != nil {
		return
	}
	pool := NewWorkerPool(d.workerCount(runtime.runner.Type()), d.logger)
	runtime.mu.Lock()
	runtime.pool = pool
	runtime.mu.Unlock()
	pool.Start(ctx, runtime.runner.Queue(), runtime.runner.ProcessOne)
	defer pool.Wait()

	recovering := true
	for {
		if ctx.Err() != nil {
			return
		}
		if err := d.discover(ctx, runtime, recovering); err != nil {
			if ctx.Err() != nil {
				return
			}
			runtime.recoveryFailed()
			d.logger.Error("dispatcher pending discovery failed", "type", runtime.runner.Type(), "err", err)
		} else {
			recovering = false
			runtime.setState("running")
		}
		if !waitForDiscovery(ctx, runtime.notify) {
			return
		}
	}
}

func waitForDiscovery(ctx context.Context, notify <-chan struct{}) bool {
	timer := time.NewTimer(pendingPollInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-notify:
		return true
	case <-timer.C:
		return true
	}
}

func (d *Dispatcher) discover(ctx context.Context, runtime *runnerRuntime, recovering bool) error {
	afterID := 0
	for {
		queryCtx, cancelQuery := context.WithTimeout(ctx, pendingQueryTimeout)
		ids, err := runtime.runner.PendingTaskIDs(queryCtx, afterID, pendingPageSize)
		cancelQuery()
		if err != nil {
			return err
		}
		for _, id := range ids {
			if id <= afterID {
				return errors.New("pending discovery returned non-increasing task IDs")
			}
			afterID = id
			// Initial recovery streams behind live consumers; it must not
			// silently skip a recovered ID merely because execution is slow.
			// Later scans bound each capacity wait before checking other IDs.
			enqueueCtx := ctx
			cancelEnqueue := func() {}
			if !recovering {
				enqueueCtx, cancelEnqueue = context.WithTimeout(ctx, pendingEnqueueTimeout)
			}
			accepted, err := enqueuePendingTask(enqueueCtx, runtime.runner, id)
			cancelEnqueue()
			if accepted && recovering {
				runtime.mu.Lock()
				runtime.recovered++
				runtime.mu.Unlock()
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Capacity pressure and concurrent cancellation are normal. The
			// next bounded scan rediscovers any task which remains pending.
			if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
				return err
			}
		}
		if len(ids) < pendingPageSize {
			return nil
		}
	}
}

// Notify is a lossy wakeup, not task ownership. Persistence is the source of
// truth; notifications before Run, during recovery, or during a scan are safe.
func (d *Dispatcher) Notify(taskType string) {
	for _, runtime := range d.runners {
		if runtime.runner.Type() == taskType {
			select {
			case runtime.notify <- struct{}{}:
			default:
			}
			return
		}
	}
}

// Enqueue is retained for internal callers. HTTP submissions should Notify
// after persistence, so a request cancellation cannot change accepted work.
func (d *Dispatcher) Enqueue(ctx context.Context, taskType string, taskID int) error {
	for _, runtime := range d.runners {
		if runtime.runner.Type() == taskType {
			_, err := enqueuePendingTask(ctx, runtime.runner, taskID)
			return err
		}
	}
	return fmt.Errorf("dispatcher: unknown task type %q", taskType)
}

func enqueuePendingTask(ctx context.Context, runner TaskRunner, taskID int) (bool, error) {
	reader, ok := runner.(taskStatusReader)
	if !ok {
		return false, errors.New("runner does not support durable task state checks")
	}
	return runner.Queue().enqueueChecked(ctx, taskID, func(ctx context.Context, id int) (bool, error) {
		checkCtx, cancel := context.WithTimeout(ctx, taskStatusTimeout)
		defer cancel()
		status, err := reader.TaskStatus(checkCtx, id)
		return status == "pending", err
	})
}

func (d *Dispatcher) CancelTask(taskType string, taskID int) {
	for _, runtime := range d.runners {
		if runtime.runner.Type() == taskType {
			queue := runtime.runner.Queue()
			execution, exists := queue.Capture(taskID)
			if !exists {
				return
			}
			reader, ok := runtime.runner.(taskStatusReader)
			if !ok {
				d.logger.Error("dispatcher cancellation cannot verify task state", "type", taskType)
				return
			}
			// Persistence can already have advanced to retry/resume when an
			// older handler reaches this notification. Check after Capture:
			// a fresh claim appearing while the query runs must remain intact.
			ctx, cancel := context.WithTimeout(context.Background(), taskStatusTimeout)
			defer cancel()
			status, err := reader.TaskStatus(ctx, taskID)
			if err != nil {
				d.logger.Warn("dispatcher cancellation state check failed", "type", taskType, "task_id", taskID, "err", err)
				return
			}
			if status == "cancelled" {
				queue.CancelExecution(execution)
			}
			return
		}
	}
}

func (d *Dispatcher) PauseTask(taskType string, taskID int) bool {
	for _, runtime := range d.runners {
		if runtime.runner.Type() == taskType {
			return runtime.runner.Pause(taskID)
		}
	}
	return false
}

func (d *Dispatcher) QueuePosition(taskType string, taskID int) *QueueInfo {
	for _, runtime := range d.runners {
		if runtime.runner.Type() == taskType {
			info := runtime.runner.Queue().Position(taskID)
			return &info
		}
	}
	return nil
}

func (d *Dispatcher) stop() {
	d.mu.Lock()
	if d.stopping {
		d.mu.Unlock()
		return
	}
	d.stopping = true
	started := d.started
	if d.cancel != nil {
		d.cancel()
	}
	d.mu.Unlock()
	for _, runtime := range d.runners {
		runtime.setState("stopping")
		runtime.runner.Queue().Close()
		if !started {
			runtime.setState("stopped")
		}
	}
	if !started {
		close(d.done)
	}
}

// Shutdown stops delivery and execution, then waits within the caller's budget.
// A timeout is reported instead of pretending background workers have exited.
func (d *Dispatcher) Shutdown(ctx context.Context) error {
	d.stop()
	select {
	case <-d.done:
		return nil
	default:
	}
	select {
	case <-d.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("dispatcher shutdown: %w", ctx.Err())
	}
}

func (d *Dispatcher) Snapshot() []RunnerSnapshot {
	result := make([]RunnerSnapshot, 0, len(d.runners))
	for _, runtime := range d.runners {
		runtime.mu.Lock()
		snapshot := RunnerSnapshot{
			TaskType: runtime.runner.Type(), State: runtime.state,
			RecoveredTotal: runtime.recovered, RecoveryErrorsTotal: runtime.recoveryErrors,
		}
		pool := runtime.pool
		runtime.mu.Unlock()
		if snapshot.TaskType == "sync" {
			snapshot.TaskType = "glossary_sync"
		}
		if queue := runtime.runner.Queue(); queue != nil {
			q := queue.Snapshot()
			snapshot.QueueCapacity = &q.Capacity
			snapshot.QueueWaiting = &q.Waiting
			snapshot.EnqueueWaiters = &q.EnqueueWaiters
		}
		if pool != nil {
			p := pool.Snapshot()
			snapshot.WorkerCapacity = &p.Capacity
			snapshot.WorkersAlive = &p.Alive
			snapshot.WorkersBusy = &p.Busy
		}
		result = append(result, snapshot)
	}
	return result
}

func (d *Dispatcher) workerCount(taskType string) int {
	switch taskType {
	case "translation":
		if d.workerCfg.Translation.Count > 0 {
			return d.workerCfg.Translation.Count
		}
	case "sync":
		if d.workerCfg.Sync.Count > 0 {
			return d.workerCfg.Sync.Count
		}
	}
	return 1
}
