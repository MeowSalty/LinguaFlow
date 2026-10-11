package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
)

type storedRoundTestStore struct {
	RoundStore
	mu          sync.Mutex
	fail        string
	intents     []RequestIntent
	records     []RequestRecord
	cursorSaved func(int, WorkCursor)
}

func (*storedRoundTestStore) ResourceIdentity() int { return 73 }

func (s *storedRoundTestStore) Seal(ctx context.Context, indices []int) (RoundRecovery, error) {
	if s.fail == "seal" {
		return RoundRecovery{}, terminalRuntimeStoreError{errors.New("manifest unavailable")}
	}
	return s.RoundStore.Seal(ctx, indices)
}

func (s *storedRoundTestStore) Reserve(_ context.Context, intent RequestIntent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail == "reserve" {
		return terminalRuntimeStoreError{errors.New("intent unavailable")}
	}
	s.intents = append(s.intents, intent)
	return nil
}

func (s *storedRoundTestStore) Record(_ context.Context, _ string, record RequestRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail == "record" && record.State != "sent" {
		return terminalRuntimeStoreError{errors.New("accounting unavailable")}
	}
	if s.fail == "finish" && record.State == "completed" {
		return terminalRuntimeStoreError{errors.New("handoff confirmation unavailable")}
	}
	s.records = append(s.records, record)
	return nil
}

func (s *storedRoundTestStore) Cursor(ctx context.Context, idx int, cursor WorkCursor) error {
	if err := s.RoundStore.Cursor(ctx, idx, cursor); err != nil {
		return err
	}
	if s.cursorSaved != nil {
		s.cursorSaved(idx, cursor)
	}
	return nil
}

func (s *storedRoundTestStore) snapshot() ([]RequestIntent, []RequestRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]RequestIntent(nil), s.intents...), append([]RequestRecord(nil), s.records...)
}

type storedRoundTestHandler struct {
	backend      backend.Backend
	build        func([]int, int) ([][]int, error)
	initialScans atomic.Int64
	finalized    atomic.Int64
}

func (*storedRoundTestHandler) ModeName() string { return RoundModeSemanticQA }

func (h *storedRoundTestHandler) BuildBatches(_ context.Context, doc *Document, pending []int, pool int) ([][]int, error) {
	if pending == nil {
		h.initialScans.Add(1)
		pending = make([]int, len(doc.Segments))
		for i := range pending {
			pending[i] = i
		}
	}
	if h.build != nil {
		return h.build(pending, pool)
	}
	batches := make([][]int, len(pending))
	for i, idx := range pending {
		batches[i] = []int{idx}
	}
	return batches, nil
}

func (h *storedRoundTestHandler) ProcessBatch(ctx context.Context, doc *Document, indices []int, _ int, _ *slog.Logger) batchResult {
	if _, err := h.backend.Translate(ctx, backend.Request{System: "inspect", User: fmt.Sprint(indices)}); err != nil {
		// Existing best-effort handlers may return unresolved for a backend error.
		// The executor must inspect the session before treating it as a model retry.
		return batchResult{unresolved: indices}
	}
	result := &BatchResult{}
	for _, idx := range indices {
		result.Segments = append(result.Segments, TranslatedSegment{Index: idx, ID: doc.Segments[idx].ID, TargetText: doc.Segments[idx].Target})
	}
	return batchResult{callbackResult: result}
}

func (h *storedRoundTestHandler) Finalize(context.Context, *Document, []int) error {
	h.finalized.Add(1)
	return nil
}

type storedRoundTestReporter struct {
	progress.Nop
	flush func(context.Context) error
}

func (r *storedRoundTestReporter) FlushCheckpoint(ctx context.Context) error {
	if r.flush != nil {
		return r.flush(ctx)
	}
	return nil
}

func awaitStoredRoundSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("stored round did not reach the expected barrier")
	}
}

func TestStoredRoundStorageFailuresDoNotRetryModel(t *testing.T) {
	for _, failure := range []string{"seal", "reserve", "record"} {
		t.Run(failure, func(t *testing.T) {
			runtime := testExecutionRuntime(t, 1)
			store := &storedRoundTestStore{RoundStore: NewMemoryRoundStore(nil), fail: failure}
			inner := &runtimeTestBackend{}
			h := &storedRoundTestHandler{backend: BindRequestBackend(inner, 9, 0, runtime)}
			var callbacks int
			result, err := RunRound(context.Background(), Round{Runtime: runtime, Store: store, Handler: h, Concurrency: 1, Retry: backend.RetryPolicy{MaxAttempts: 2}}, newTestDoc(1), func(context.Context, BatchResult) error {
				callbacks++
				return nil
			}, quietLogger(), nil)
			var storage *StorageError
			if !errors.As(err, &storage) || callbacks != 0 || len(result.Resolved) != 0 || h.finalized.Load() != 0 {
				t.Fatalf("storage failure was treated as model work: result=%+v callbacks=%d err=%v", result, callbacks, err)
			}
			wantCalls := int64(0)
			if failure == "record" {
				wantCalls = 1
			}
			if got := inner.calls.Load(); got != wantCalls {
				t.Fatalf("model calls=%d, want %d", got, wantCalls)
			}
			if runtime.Admission.Snapshot().PendingResults != 0 || len(runtime.parse) != 0 {
				t.Fatal("storage failure retained a response reservation")
			}
		})
	}
}

func TestStoredRoundKeepsCompletionThroughCallbackAndCheckpoint(t *testing.T) {
	runtime := testExecutionRuntime(t, 1)
	store := &storedRoundTestStore{RoundStore: NewMemoryRoundStore(nil)}
	inner := &runtimeTestBackend{}
	h := &storedRoundTestHandler{backend: BindRequestBackend(inner, 9, 0, runtime)}
	callbackEntered, callbackRelease := make(chan struct{}), make(chan struct{})
	checkpointEntered, checkpointRelease := make(chan struct{}), make(chan struct{})
	reporter := &storedRoundTestReporter{flush: func(ctx context.Context) error {
		close(checkpointEntered)
		select {
		case <-checkpointRelease:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	station := NewStation(1)
	if !station.Acquire(context.Background()) {
		t.Fatal("station setup")
	}
	defer station.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		result, err := RunRound(ctx, Round{Runtime: runtime, Store: store, Handler: h, Concurrency: 1, Slots: station}, newTestDoc(1), func(ctx context.Context, _ BatchResult) error {
			close(callbackEntered)
			select {
			case <-callbackRelease:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}, quietLogger(), reporter)
		if err == nil && !reflect.DeepEqual(result.Resolved, []int{0}) {
			err = fmt.Errorf("accepted in-flight work lost on pause: %+v", result)
		}
		done <- err
	}()
	awaitStoredRoundSignal(t, callbackEntered)
	assertLifetime := func() {
		t.Helper()
		snapshot := runtime.Admission.Snapshot()
		if snapshot.MainInflight[0] != 0 || snapshot.PendingResults != 1 || runtime.Gate.Inflight() != 0 || len(runtime.parse) != 1 {
			t.Fatalf("response lifetime: %+v, parsers=%d", snapshot, len(runtime.parse))
		}
		_, records := store.snapshot()
		if len(records) != 2 || records[1].State != "received" {
			t.Fatalf("response declared completed before checkpoint: %+v", records)
		}
	}
	assertLifetime()
	runtime.Gate.Pause()
	close(callbackRelease)
	awaitStoredRoundSignal(t, checkpointEntered)
	assertLifetime()
	close(checkpointRelease)
	if err := awaitRuntimeResult(t, done); err != nil {
		t.Fatal(err)
	}
	if runtime.Admission.Snapshot().PendingResults != 0 || len(runtime.parse) != 0 || inner.calls.Load() != 1 {
		t.Fatal("response reservation or dispatch escaped the pause boundary")
	}
}

func TestStoredRoundCheckpointFailureAbortsLocally(t *testing.T) {
	runtime := testExecutionRuntime(t, 1)
	store := &storedRoundTestStore{RoundStore: NewMemoryRoundStore(nil)}
	inner := &runtimeTestBackend{}
	h := &storedRoundTestHandler{backend: BindRequestBackend(inner, 9, 0, runtime)}
	reporter := &storedRoundTestReporter{flush: func(context.Context) error {
		return terminalRuntimeStoreError{errors.New("checkpoint unavailable")}
	}}
	result, err := RunRound(context.Background(), Round{Runtime: runtime, Store: store, Handler: h, Concurrency: 1, Retry: backend.RetryPolicy{MaxAttempts: 2}}, newTestDoc(2), nil, quietLogger(), reporter)
	var storage *StorageError
	if !errors.As(err, &storage) || len(result.Resolved) != 0 || h.finalized.Load() != 0 {
		t.Fatalf("checkpoint failure did not abort: %+v, %v", result, err)
	}
	if inner.calls.Load() != 1 || runtime.Admission.Snapshot().PendingResults != 0 {
		t.Fatalf("checkpoint failure retried the model or leaked capacity: calls=%d", inner.calls.Load())
	}
	_, records := store.snapshot()
	if len(records) != 2 || records[1].State != "received" {
		t.Fatalf("failed checkpoint declared its response completed: %+v", records)
	}
}

func TestStoredRoundFinishFailureDoesNotReissueAcceptedWork(t *testing.T) {
	runtime := testExecutionRuntime(t, 1)
	store := &storedRoundTestStore{RoundStore: NewMemoryRoundStore(nil), fail: "finish"}
	inner := &runtimeTestBackend{}
	h := &storedRoundTestHandler{backend: BindRequestBackend(inner, 9, 0, runtime)}
	var callbacks int
	result, err := RunRound(context.Background(), Round{Runtime: runtime, Store: store, Handler: h, Concurrency: 1, Retry: backend.RetryPolicy{MaxAttempts: 2}}, newTestDoc(2), func(context.Context, BatchResult) error {
		callbacks++
		return nil
	}, quietLogger(), &storedRoundTestReporter{})
	var storage *StorageError
	if !errors.As(err, &storage) || callbacks != 1 || inner.calls.Load() != 1 || len(result.Resolved) != 0 || h.finalized.Load() != 0 {
		t.Fatalf("handoff failure was treated as model retry: result=%+v calls=%d callbacks=%d err=%v", result, inner.calls.Load(), callbacks, err)
	}
	_, records := store.snapshot()
	if len(records) != 2 || records[1].State != "received" || runtime.Admission.Snapshot().PendingResults != 0 {
		t.Fatalf("unconfirmed received response lost: %+v", records)
	}
}

func TestStoredRoundCallbackFailureLeavesResponseReceived(t *testing.T) {
	runtime := testExecutionRuntime(t, 1)
	store := &storedRoundTestStore{RoundStore: NewMemoryRoundStore(nil)}
	inner := &runtimeTestBackend{}
	h := &storedRoundTestHandler{backend: BindRequestBackend(inner, 9, 0, runtime)}
	wantErr := errors.New("business writeback failed")
	result, err := RunRound(context.Background(), Round{Runtime: runtime, Store: store, Handler: h, Concurrency: 1, Retry: backend.RetryPolicy{MaxAttempts: 2}}, newTestDoc(1), func(context.Context, BatchResult) error { return wantErr }, quietLogger(), nil)
	if !errors.Is(err, wantErr) || inner.calls.Load() != 1 || len(result.Resolved) != 0 || h.finalized.Load() != 0 {
		t.Fatalf("unconfirmed callback counted as completed: %+v %v", result, err)
	}
	_, records := store.snapshot()
	if len(records) != 2 || records[1].State != "received" || runtime.Admission.Snapshot().PendingResults != 0 {
		t.Fatalf("failed callback lost its received response: %+v", records)
	}
}

func TestStoredRoundRequestUsesFrozenScopeAndUsage(t *testing.T) {
	a, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{4: 1}, MaxCompletions: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	runtime := NewExecutionRuntime(a, DefaultCandidateLimits(), nil)
	store := &storedRoundTestStore{RoundStore: NewMemoryRoundStore(nil)}
	inner := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
		return &backend.Response{Text: `{"issues":[]}`, Usage: backend.Usage{PromptTokens: 17, CompletionTokens: 3}}, nil
	}}
	h := &SemanticQAHandler{Backend: BindRequestBackend(inner, 9, 4, runtime), Renderer: newSemanticQARenderer(t), SegmentScope: "all", RoundIndex: 4}
	result, err := RunRound(context.Background(), Round{Runtime: runtime, Store: store, Handler: h, Concurrency: 1}, semanticQADoc([]string{"translated", "translated"}, nil), nil, quietLogger(), nil)
	if err != nil || len(result.Resolved) != 2 {
		t.Fatalf("semantic QA result=%+v err=%v", result, err)
	}
	intents, records := store.snapshot()
	if len(intents) != 1 {
		t.Fatalf("request intents=%+v", intents)
	}
	intent := intents[0]
	if intent.ID == "" || intent.InputDigest == "" || intent.ResourceID != 73 || intent.RoundIndex != 4 || intent.BackendID != 9 || intent.Stage != backend.RequestStageMain || intent.Phase != "main" || !reflect.DeepEqual(intent.Indices, []int{0, 1}) {
		t.Fatalf("request identity or scope lost: %+v", intent)
	}
	if len(records) != 3 || records[0].State != "sent" || records[1].State != "received" || records[2].State != "completed" || !records[1].UsageKnown || records[1].Usage.PromptTokens != 17 || records[1].Usage.CompletionTokens != 3 {
		t.Fatalf("request accounting=%+v", records)
	}
}

func TestStoredRoundRecoveryPreservesPoolAndAttemptBudget(t *testing.T) {
	runtime := testExecutionRuntime(t, 1)
	memory := NewMemoryRoundStore(nil)
	memory.state = RoundRecovery{Sealed: true, Members: []int{0, 1, 2, 3}, Completed: []int{3}, Cursors: map[int]WorkCursor{
		0: {Pool: 1, Attempt: 1, State: "pending"},
		1: {Pool: 0, Attempt: 2, State: "pending"},
		2: {Pool: 2, State: "unresolved"},
	}}
	store := &storedRoundTestStore{RoundStore: memory}
	inner := &runtimeTestBackend{}
	h := &storedRoundTestHandler{backend: BindRequestBackend(inner, 9, 0, runtime)}
	result, err := RunRound(context.Background(), Round{Runtime: runtime, Store: store, Handler: h, Concurrency: 1, Retry: backend.RetryPolicy{MaxAttempts: 1}}, newTestDoc(4), nil, quietLogger(), nil)
	if err != nil || !reflect.DeepEqual(result.Resolved, []int{0, 1, 3}) || !reflect.DeepEqual(result.Unresolved, []int{2}) {
		t.Fatalf("recovered round=%+v err=%v", result, err)
	}
	intents, _ := store.snapshot()
	if len(intents) != 2 || intents[0].Pool != 1 || intents[0].Attempt != 0 || !reflect.DeepEqual(intents[0].Indices, []int{1}) || intents[1].Pool != 1 || intents[1].Attempt != 1 || !reflect.DeepEqual(intents[1].Indices, []int{0}) {
		t.Fatalf("pool or attempt budget reset: %+v", intents)
	}
	if h.initialScans.Load() != 0 || inner.calls.Load() != 2 {
		t.Fatal("sealed recovery rescanned or re-requested completed work")
	}
}

func TestStoredRoundPauseRetainsRetryDeadline(t *testing.T) {
	runtime := testExecutionRuntime(t, 1)
	memory := NewMemoryRoundStore(nil)
	store := &storedRoundTestStore{RoundStore: memory, cursorSaved: func(_ int, cursor WorkCursor) {
		if cursor.Attempt == 1 {
			runtime.Gate.Pause()
		}
	}}
	inner := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
		return nil, &backend.StatusError{StatusCode: 429, RetryAfter: time.Hour, Err: errors.New("rate limited")}
	}}
	h := &SemanticQAHandler{Backend: BindRequestBackend(inner, 9, 0, runtime), Renderer: newSemanticQARenderer(t), SegmentScope: "all", Retry: backend.RetryPolicy{MaxAttempts: 1}}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := RunRound(ctx, Round{Runtime: runtime, Store: store, Handler: h, Concurrency: 1, Retry: h.Retry}, semanticQADoc([]string{"translated"}, nil), nil, quietLogger(), nil)
	if err != nil || !reflect.DeepEqual(result.Unresolved, []int{0}) || len(result.Resolved) != 0 {
		t.Fatalf("pause while backing off: %+v %v", result, err)
	}
	state, _ := memory.Load(context.Background())
	cursor := state.Cursors[0]
	if cursor.Pool != 0 || cursor.Attempt != 1 || cursor.NextAttemptAt.Before(start.Add(time.Hour)) || inner.calls.Load() != 1 {
		t.Fatalf("pause reset retry state: %+v, calls=%d", cursor, inner.calls.Load())
	}
	if runtime.Admission.Snapshot().PendingResults != 0 || runtime.Gate.Inflight() != 0 {
		t.Fatal("backoff retained request resources")
	}
}

func TestStoredRoundSealedMembershipCannotDisappear(t *testing.T) {
	runtime := testExecutionRuntime(t, 1)
	memory := NewMemoryRoundStore(nil)
	memory.state = RoundRecovery{Sealed: true, Members: []int{0, 1}, Cursors: map[int]WorkCursor{}}
	inner := &runtimeTestBackend{}
	h := &storedRoundTestHandler{backend: BindRequestBackend(inner, 9, 0, runtime), build: func([]int, int) ([][]int, error) { return [][]int{{0}}, nil }}
	_, err := RunRound(context.Background(), Round{Runtime: runtime, Store: memory, Handler: h, Concurrency: 1}, newTestDoc(2), nil, quietLogger(), nil)
	if err == nil || inner.calls.Load() != 0 {
		t.Fatalf("incomplete sealed membership dispatched: err=%v calls=%d", err, inner.calls.Load())
	}
}

func TestStoredRoundRecoversTerminalScanFailure(t *testing.T) {
	runtime := testExecutionRuntime(t, 1)
	memory := NewMemoryRoundStore(nil)
	memory.state = RoundRecovery{Sealed: true, Members: []int{0}, Cursors: map[int]WorkCursor{0: {Pool: 1, State: "unresolved", Phase: "terminal_failure"}}}
	inner := &runtimeTestBackend{}
	h := &storedRoundTestHandler{backend: BindRequestBackend(inner, 9, 0, runtime)}
	result, err := RunRound(context.Background(), Round{Runtime: runtime, Store: memory, Handler: h, Concurrency: 1}, newTestDoc(1), nil, quietLogger(), nil)
	if err != nil || !reflect.DeepEqual(result.FailedSegments, []int{0}) || len(result.Unresolved) != 0 || inner.calls.Load() != 0 {
		t.Fatalf("terminal scan failure lost on recovery: %+v %v", result, err)
	}
}
