package pipeline

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
)

// sortedCopy 返回升序副本，便于与期望集合比较。
func sortedCopy(in []int) []int {
	out := append([]int(nil), in...)
	sort.Ints(out)
	return out
}

// equalInts 比较两个切片作为集合是否一致（长度与逐元素）。
func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRunPoolPauseRecoversLateWorkDeferredAndUnsentSource(t *testing.T) {
	gate := NewPauseGate()
	runtime := testExecutionRuntime(t, 1)
	runtime.Gate = gate
	entered, release := make(chan struct{}), make(chan struct{})
	process := func(_ context.Context, _ batchJob) batchResult {
		close(entered)
		<-release
		return batchResult{deferred: true, waitCapacity: &WorkDeferred{Wait: backend.AdmissionWait{CapacityChanged: make(chan struct{})}}}
	}
	round := Round{Concurrency: 1, Gate: gate, Runtime: runtime}
	batches := [][]int{{0}, {1}, {2}, {3}, {4}, {5}}
	done := make(chan poolResult, 1)
	go func() {
		result, err := runPool(context.Background(), round, newTestDoc(6), batches, 1, progress.Nop{}, process)
		if err != nil {
			t.Error(err)
		}
		done <- result
	}()
	<-entered
	// Pause wins before the held handler can return its capacity intent. The
	// coordinator must join that result and the producer's unsubmitted tail.
	gate.Pause()
	close(release)
	select {
	case result := <-done:
		if got := sortedCopy(result.unresolved); !equalInts(got, []int{0, 1, 2, 3, 4, 5}) {
			t.Fatalf("pause discarded deferred/source work: %v", got)
		}
		if len(result.resolved) != 0 {
			t.Fatal("unprepared work counted as completed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pause failed to join deferred batch")
	}
}

// inflightTracker 以互斥锁记录并发进入 ProcessBatch 的峰值。
type inflightTracker struct {
	mu       sync.Mutex
	inflight int
	max      int
}

func (t *inflightTracker) enter() {
	t.mu.Lock()
	t.inflight++
	if t.inflight > t.max {
		t.max = t.inflight
	}
	t.mu.Unlock()
}

func (t *inflightTracker) exit() {
	t.mu.Lock()
	t.inflight--
	t.mu.Unlock()
}

func (t *inflightTracker) peak() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.max
}

func TestRunRound_SharedAdmissionSerializesAcrossResources(t *testing.T) {
	runtime := memoryTestRuntime(t, 1, nil)
	var tracker inflightTracker
	provider := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
		tracker.enter()
		defer tracker.exit()
		time.Sleep(15 * time.Millisecond)
		return &backend.Response{Text: `{"translations":{"1":"done"}}`}, nil
	}}
	h := &TranslateHandler{Backend: provider, BatchSize: 1, Renderer: newTestRenderer(t)}
	round := Round{Concurrency: 2, Handler: h, Runtime: runtime}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			_, err := runMemoryTestRound(t, t.Context(), round, newTestDoc(3), nil, quietLogger(), nil)
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls, peak := provider.calls.Load(), tracker.peak(); calls != 6 || peak != 1 {
		t.Fatalf("provider calls=%d peak=%d, want 6 calls with shared concurrency 1", calls, peak)
	}
}

// TestRunRound_HandlerBackoffAbortsOnPause 退避重试等待中的暂停中止：
// 后端返回可重试错误后 handler 进入退避（最小 5s，见 minRateLimitBackoff），
// Pause 置位应经 Gate.Done() 立即中止等待——批次按 unresolved 收场而非等满
// 退避时长，且暂停后不再发起重试调用。
func TestRunRound_HandlerBackoffAbortsOnPause(t *testing.T) {
	gate := NewPauseGate()
	doc := newTestDoc(1)
	fb := &fakeBackend{
		name: "fake",
		errs: []error{errors.New("network down")}, // 无 HTTP 状态码 → backend.IsRetryable = true
	}
	rep := &countingReporter{}
	h := newTestTranslateHandler(fb, 1, 1, func(h *TranslateHandler) {
		h.Reporter = rep
		h.Retry = backend.RetryPolicy{MaxAttempts: 1, Backoff: 5} // permit same-pool retry
	})
	h.Renderer = newTestRenderer(t)

	// 后端被调用即暂停：此刻 handler 正卡在退避 timer 的 select 上。
	go func() {
		deadline := time.Now().Add(3 * time.Second)
		for fb.idx.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(2 * time.Millisecond)
		}
		gate.Pause()
	}()

	start := time.Now()
	round := Round{Concurrency: 1, Retry: h.Retry, Shrink: 1.0, Handler: h, Gate: gate}
	result, err := runMemoryTestRound(t, context.Background(), round, doc, nil, quietLogger(), rep)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("RunRound: %v", err)
	}
	// 未中止时退避最小 5s；Gate.Done() 中止应显著快于它（4s 留足裕量）。
	if elapsed >= 4*time.Second {
		t.Fatalf("RunRound 耗时 %v（已达最小退避 5s 量级），暂停未中止退避等待", elapsed)
	}
	if got := sortedCopy(result.Unresolved); !equalInts(got, []int{0}) {
		t.Fatalf("Unresolved=%v want [0]（段保持未解决，由断点集合覆盖）", got)
	}
	if calls := fb.idx.Load(); calls != 1 {
		t.Fatalf("backend calls=%d want 1（暂停后不再发起重试）", calls)
	}
}

func TestRunRound_PauseWhileWaitingForAdmission(t *testing.T) {
	gate := NewPauseGate()
	runtime := memoryTestRuntime(t, 1, gate)
	store := NewMemoryRoundStore(nil)
	defer store.Close()
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	provider := &runtimeTestBackend{call: func(ctx context.Context) (*backend.Response, error) {
		once.Do(func() { close(started) })
		select {
		case <-release:
			return &backend.Response{Text: `{"translations":{"1":"done"}}`}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	h := &TranslateHandler{Backend: provider, BatchSize: 1, Renderer: newTestRenderer(t)}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	type outcome struct {
		result RunRoundResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := runMemoryTestRound(t, ctx, Round{Handler: h, Runtime: runtime, Store: store, Concurrency: 2}, newTestDoc(2), nil, quietLogger(), nil)
		done <- outcome{result, err}
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("provider did not start")
	}
	gate.Pause()
	close(release)
	select {
	case got := <-done:
		if got.err != nil || len(got.result.Resolved) != 0 || len(got.result.Unresolved) != 2 || provider.calls.Load() != 1 {
			t.Fatalf("paused round: result=%+v err=%v calls=%d", got.result, got.err, provider.calls.Load())
		}
	case <-ctx.Done():
		t.Fatal("paused round did not drain")
	}

	requests := parseRetryRequests(store)
	if len(requests) != 1 || requests[0].response.CaptureBytes == 0 {
		t.Fatalf("in-flight response missing from archive: %v", requests)
	}
	resume := memoryTestRuntime(t, 1, nil)
	doc := newTestDoc(2)
	result, err := runMemoryTestRound(t, ctx, Round{Handler: h, Runtime: resume, Store: store, Concurrency: 2}, doc, nil, quietLogger(), nil)
	if err != nil || len(result.Resolved) != 2 || provider.calls.Load() != 2 ||
		doc.Segments[0].Target != "done" || doc.Segments[1].Target != "done" {
		t.Fatalf("resumed round: result=%+v calls=%d err=%v", result, provider.calls.Load(), err)
	}
}
