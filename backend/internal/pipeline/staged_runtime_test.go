package pipeline

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
)

type runtimeTestBackend struct {
	calls atomic.Int64
	call  func(context.Context) (*backend.Response, error)
}

func (*runtimeTestBackend) Name() string { return "runtime-test" }
func (*runtimeTestBackend) Close() error { return nil }
func (b *runtimeTestBackend) Translate(ctx context.Context, _ backend.Request) (*backend.Response, error) {
	b.calls.Add(1)
	if b.call != nil {
		return b.call(ctx)
	}
	return &backend.Response{Text: "ok"}, nil
}

type runtimeTestStore struct {
	RoundStore
	reserve  func(context.Context, RequestIntent) error
	record   func(context.Context, string, RequestRecord) error
	complete func(context.Context, string, RequestRecord) error
	sent     func(context.Context, string, RequestRecord) error
	abort    func(context.Context, string) error
}

func (s *runtimeTestStore) Reserve(ctx context.Context, intent RequestIntent) error {
	if s.reserve != nil {
		return s.reserve(ctx, intent)
	}
	return nil
}
func (s *runtimeTestStore) Record(ctx context.Context, id string, record RequestRecord) error {
	if record.State == "completed" {
		if s.complete != nil {
			return s.complete(ctx, id, record)
		}
		return nil
	}
	if record.State == "sent" {
		if s.sent != nil {
			return s.sent(ctx, id, record)
		}
		return nil
	}
	if s.record != nil {
		return s.record(ctx, id, record)
	}
	return nil
}

func (s *runtimeTestStore) AbortRequest(ctx context.Context, id string) error {
	if s.abort != nil {
		return s.abort(ctx, id)
	}
	return nil
}

type terminalRuntimeStoreError struct{ error }

func (terminalRuntimeStoreError) Permanent() bool { return true }
func (e terminalRuntimeStoreError) Unwrap() error { return e.error }

func testExecutionRuntime(t *testing.T, completions int) *ExecutionRuntime {
	t.Helper()
	a, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{
		MainConcurrency: map[int]int{0: 4}, AlignmentConcurrency: 2, MaxCompletions: completions,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return NewExecutionRuntime(a, DefaultCandidateLimits(), nil)
}

func testRequestSession(runtime *ExecutionRuntime, store RoundStore) (*requestSession, context.Context) {
	s := &requestSession{runtime: runtime, store: store, intent: RequestIntent{Stage: backend.RequestStageMain, RoundIndex: 0, Phase: "main"}}
	return s, context.WithValue(context.Background(), requestSessionKey{}, s)
}

func awaitRuntimeResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(time.Second):
		t.Fatal("runtime operation did not finish")
		return nil
	}
}

func TestRequestBackendKeepsCompletionWhileSaving(t *testing.T) {
	runtime := testExecutionRuntime(t, 1)
	entered, release := make(chan struct{}), make(chan struct{})
	store := &runtimeTestStore{record: func(ctx context.Context, _ string, _ RequestRecord) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	s, ctx := testRequestSession(runtime, store)
	defer s.finish()
	inner := &runtimeTestBackend{}
	b := BindRequestBackend(inner, 1, 0, runtime)
	done := make(chan error, 1)
	go func() { _, err := b.Translate(ctx, backend.Request{}); done <- err }()
	<-entered
	snapshot := runtime.Admission.Snapshot()
	if snapshot.MainInflight[0] != 0 || snapshot.PendingResults != 1 || runtime.Gate.Inflight() != 0 {
		t.Fatalf("request/save lifetimes: %+v", snapshot)
	}
	runtime.Gate.Pause()
	close(release)
	if err := awaitRuntimeResult(t, done); err != nil {
		t.Fatal(err)
	}
	if inner.calls.Load() != 1 {
		t.Fatal("pause reissued request")
	}
	s.finish()
	if runtime.Admission.Snapshot().PendingResults != 0 {
		t.Fatal("saved completion leaked")
	}
}

func TestRequestBackendRetriesOnlyLocalStorage(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "transient", true: "terminal"}[terminal], func(t *testing.T) {
			runtime := testExecutionRuntime(t, 2)
			runtime.saveRetryDelay = time.Nanosecond
			var records int
			failure := errors.New("database unavailable")
			store := &runtimeTestStore{record: func(context.Context, string, RequestRecord) error {
				records++
				if terminal {
					return terminalRuntimeStoreError{failure}
				}
				if records == 1 {
					return failure
				}
				return nil
			}}
			s, ctx := testRequestSession(runtime, store)
			defer s.finish()
			inner := &runtimeTestBackend{}
			b := BindRequestBackend(inner, 1, 0, runtime)
			_, err := b.Translate(ctx, backend.Request{})
			if terminal {
				var storage *StorageError
				if !errors.As(err, &storage) || backend.IsRetryable(err) || s.err == nil {
					t.Fatalf("storage error lost: %v", err)
				}
				if _, again := b.Translate(ctx, backend.Request{}); again != s.err {
					t.Fatal("failed session allowed another request")
				}
			} else if err != nil || records != 2 {
				t.Fatalf("local retry: records=%d err=%v", records, err)
			}
			if inner.calls.Load() != 1 {
				t.Fatal("storage failure reissued model request")
			}
		})
	}
}

func TestRequestBackendBoundsParsingAndReleasesNetwork(t *testing.T) {
	runtime := testExecutionRuntime(t, 4)
	inner := &runtimeTestBackend{}
	b := BindRequestBackend(inner, 1, 0, runtime)
	sessions := make([]*requestSession, 3)
	contexts := make([]context.Context, 3)
	for i := range sessions {
		sessions[i], contexts[i] = testRequestSession(runtime, &runtimeTestStore{})
		defer sessions[i].finish()
	}
	for i := 0; i < 2; i++ {
		if _, err := b.Translate(contexts[i], backend.Request{}); err != nil {
			t.Fatal(err)
		}
	}
	thirdReturned := make(chan struct{})
	third := BindRequestBackend(&runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
		close(thirdReturned)
		return &backend.Response{Text: "ok"}, nil
	}}, 1, 0, runtime)
	done := make(chan error, 1)
	go func() { _, err := third.Translate(contexts[2], backend.Request{}); done <- err }()
	<-thirdReturned
	select {
	case err := <-done:
		t.Fatalf("third parser escaped capacity: %v", err)
	default:
	}
	sessions[0].finish()
	if err := awaitRuntimeResult(t, done); err != nil {
		t.Fatal(err)
	}
	if len(runtime.parse) != 2 {
		t.Fatalf("active parsers=%d", len(runtime.parse))
	}
	if snapshot := runtime.Admission.Snapshot(); snapshot.MainInflight[0] != 0 || snapshot.PendingResults != 2 {
		t.Fatalf("parse result accounting: %+v", snapshot)
	}
}

func TestRequestBackendPromptUpgradeReusesBoundedSession(t *testing.T) {
	runtime := testExecutionRuntime(t, 1)
	var phases []string
	store := &runtimeTestStore{reserve: func(_ context.Context, intent RequestIntent) error { phases = append(phases, intent.Phase); return nil }}
	s, ctx := testRequestSession(runtime, store)
	defer s.finish()
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	inner := &runtimeTestBackend{}
	b := BindRequestBackend(inner, 1, 0, runtime)
	for range 2 {
		if _, err := b.Translate(ctx, backend.Request{}); err != nil {
			t.Fatal(err)
		}
	}
	if inner.calls.Load() != 2 || len(phases) != 2 || phases[0] != "main" || phases[1] != "prompt_upgrade" {
		t.Fatalf("calls=%d phases=%v", inner.calls.Load(), phases)
	}
	if got := runtime.Admission.Snapshot().PendingResults; got != 1 {
		t.Fatalf("completion reservations=%d", got)
	}
}

func TestRequestBackendSaveWaitDoesNotConsumeAdmission(t *testing.T) {
	runtime := testExecutionRuntime(t, 1)
	entered := make(chan struct{})
	store := &runtimeTestStore{reserve: func(ctx context.Context, _ RequestIntent) error { close(entered); <-ctx.Done(); return ctx.Err() }}
	s, ctx := testRequestSession(runtime, store)
	defer s.finish()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	inner := &runtimeTestBackend{}
	b := BindRequestBackend(inner, 1, 0, runtime)
	done := make(chan error, 1)
	go func() { _, err := b.Translate(ctx, backend.Request{}); done <- err }()
	<-entered
	if snapshot := runtime.Admission.Snapshot(); snapshot.PendingResults != 0 || snapshot.MainInflight[0] != 0 || inner.calls.Load() != 0 {
		t.Fatalf("save wait consumed request resources: %+v", snapshot)
	}
	cancel()
	if err := awaitRuntimeResult(t, done); !errors.Is(err, context.Canceled) || backend.IsRetryable(err) {
		t.Fatalf("save cancellation: %v", err)
	}
	if s.calls != 0 {
		t.Fatal("unsent request counted as dispatched")
	}
}

func TestRequestBackendRefundsUnsentReservation(t *testing.T) {
	for _, pause := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "pause"}[pause], func(t *testing.T) {
			runtime := testExecutionRuntime(t, 1)
			var reserved string
			var refunds, records int
			var cancel context.CancelFunc
			store := &runtimeTestStore{
				reserve: func(_ context.Context, intent RequestIntent) error {
					reserved = intent.ID
					if pause {
						runtime.Gate.Pause()
					} else {
						cancel()
					}
					return nil
				},
				abort: func(ctx context.Context, id string) error {
					if ctx.Err() != nil || reserved == "" || id != reserved {
						t.Errorf("refund lost its identity or live save context: %q %v", id, ctx.Err())
					}
					refunds++
					return nil
				},
				record: func(context.Context, string, RequestRecord) error { records++; return nil },
				sent:   func(context.Context, string, RequestRecord) error { records++; return nil },
			}
			s, ctx := testRequestSession(runtime, store)
			defer s.finish()
			ctx, cancel = context.WithCancel(ctx)
			defer cancel()
			inner := &runtimeTestBackend{}
			b := BindRequestBackend(inner, 1, 0, runtime)
			_, err := b.Translate(ctx, backend.Request{})
			want := error(context.Canceled)
			if pause {
				want = backend.ErrDispatchPaused
			}
			if !errors.Is(err, want) || refunds != 1 || records != 0 || s.calls != 0 || inner.calls.Load() != 0 {
				t.Fatalf("unsent request: refunds=%d records=%d calls=%d err=%v", refunds, records, s.calls, err)
			}
		})
	}
}

func TestRequestBackendPauseBeforeReservationIsNotStorageFailure(t *testing.T) {
	for _, durablePause := range []bool{false, true} {
		t.Run(map[bool]string{false: "gate_already_closed", true: "durable_pause_precedes_gate"}[durablePause], func(t *testing.T) {
			runtime := testExecutionRuntime(t, 1)
			reserves, aborts := 0, 0
			store := &runtimeTestStore{
				reserve: func(context.Context, RequestIntent) error {
					reserves++
					return terminalRuntimeStoreError{backend.ErrDispatchPaused}
				},
				abort: func(context.Context, string) error { aborts++; return nil },
			}
			if !durablePause {
				runtime.Gate.Pause()
			}
			s, ctx := testRequestSession(runtime, store)
			defer s.finish()
			inner := &runtimeTestBackend{}
			b := BindRequestBackend(inner, 1, 0, runtime)
			_, err := b.Translate(ctx, backend.Request{})
			wantReserves := 0
			if durablePause {
				wantReserves = 1
			}
			if err != backend.ErrDispatchPaused || s.err != nil || s.calls != 0 || inner.calls.Load() != 0 || reserves != wantReserves || aborts != 0 || !runtime.Gate.Paused() {
				t.Fatalf("pause reservation: reserves=%d aborts=%d calls=%d sticky=%v returned=%v paused=%t", reserves, aborts, s.calls, s.err, err, runtime.Gate.Paused())
			}
		})
	}
}

func TestRequestBackendSentPersistenceDoesNotDelayDispatch(t *testing.T) {
	runtime := testExecutionRuntime(t, 1)
	sentStarted, releaseSent := make(chan struct{}), make(chan struct{})
	requestStarted := make(chan struct{})
	var states []string
	store := &runtimeTestStore{
		sent: func(ctx context.Context, _ string, _ RequestRecord) error {
			close(sentStarted)
			select {
			case <-releaseSent:
				states = append(states, "sent")
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
		record: func(_ context.Context, _ string, record RequestRecord) error {
			states = append(states, record.State)
			return nil
		},
		complete: func(_ context.Context, _ string, record RequestRecord) error {
			states = append(states, record.State)
			return nil
		},
	}
	s, ctx := testRequestSession(runtime, store)
	defer s.finish()
	inner := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
		close(requestStarted)
		return &backend.Response{Text: "ok"}, nil
	}}
	b := BindRequestBackend(inner, 1, 0, runtime)
	done := make(chan error, 1)
	go func() { _, err := b.Translate(ctx, backend.Request{}); done <- err }()
	<-sentStarted
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("network request waited for sent persistence")
	}
	deadline := time.Now().Add(time.Second)
	for runtime.Gate.Inflight() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if snap := runtime.Admission.Snapshot(); snap.MainInflight[0] != 0 || snap.PendingResults != 1 {
		t.Fatalf("sent persistence retained network capacity: %+v", snap)
	}
	select {
	case err := <-done:
		t.Fatalf("terminal save overtook sent save: %v", err)
	default:
	}
	close(releaseSent)
	if err := awaitRuntimeResult(t, done); err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 || states[0] != "sent" || states[1] != "received" {
		t.Fatalf("request state ordering: %v", states)
	}
	if err := s.finish(); err != nil {
		t.Fatal(err)
	}
	if len(states) != 3 || states[2] != "completed" {
		t.Fatalf("reliable completion was not recorded: %v", states)
	}
}

func TestRequestBackendAccountsSentRequestAfterCancellation(t *testing.T) {
	runtime := testExecutionRuntime(t, 1)
	var recorded bool
	store := &runtimeTestStore{record: func(ctx context.Context, _ string, record RequestRecord) error {
		if ctx.Err() != nil || record.State != "failed" {
			t.Errorf("late accounting lost live context or terminal state: %v %+v", ctx.Err(), record)
		}
		recorded = true
		return nil
	}}
	s, ctx := testRequestSession(runtime, store)
	defer s.finish()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	inner := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
		cancel()
		return nil, context.Canceled
	}}
	b := BindRequestBackend(inner, 1, 0, runtime)
	_, err := b.Translate(ctx, backend.Request{})
	if !errors.Is(err, context.Canceled) || !recorded || s.calls != 1 || s.err != nil {
		t.Fatalf("cancelled call was not accounted: calls=%d recorded=%t err=%v", s.calls, recorded, err)
	}
}

func TestRequestBackendReceivedRecordRepairsFailedSentRecord(t *testing.T) {
	runtime := testExecutionRuntime(t, 1)
	var completed bool
	store := &runtimeTestStore{
		sent: func(context.Context, string, RequestRecord) error {
			return terminalRuntimeStoreError{errors.New("sent unavailable")}
		},
		record: func(_ context.Context, _ string, record RequestRecord) error {
			completed = record.State == "received" && record.UsageKnown
			return nil
		},
	}
	s, ctx := testRequestSession(runtime, store)
	defer s.finish()
	inner := &runtimeTestBackend{}
	b := BindRequestBackend(inner, 1, 0, runtime)
	if _, err := b.Translate(ctx, backend.Request{}); err != nil || !completed || inner.calls.Load() != 1 {
		t.Fatalf("terminal record did not repair intermediate failure: completed=%t calls=%d err=%v", completed, inner.calls.Load(), err)
	}
}

type runtimeWorkObserver struct {
	progress.Nop
	changes atomic.Int64
}

func (r *runtimeWorkObserver) OnWorkStateChange() { r.changes.Add(1) }

func TestRequestSessionFinishKeepsSavingVisibleUntilDurableCompletion(t *testing.T) {
	runtime := testExecutionRuntime(t, 1)
	entered, release := make(chan struct{}), make(chan struct{})
	var completed atomic.Int64
	store := &runtimeTestStore{complete: func(ctx context.Context, _ string, _ RequestRecord) error {
		close(entered)
		select {
		case <-release:
			completed.Add(1)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	s, ctx := testRequestSession(runtime, store)
	observer := &runtimeWorkObserver{}
	s.reporter = observer
	b := BindRequestBackend(&runtimeTestBackend{}, 1, 0, runtime)
	if _, err := b.Translate(ctx, backend.Request{}); err != nil {
		t.Fatal(err)
	}
	if completed.Load() != 0 || len(s.pending) != 1 || observer.changes.Load() != 2 {
		t.Fatal("response receipt was marked completed before processing")
	}
	done := make(chan error, 1)
	go func() { done <- s.finish() }()
	<-entered
	if snap := runtime.Admission.Snapshot(); snap.MainInflight[0] != 0 || snap.PendingResults != 1 {
		t.Fatalf("saving lifecycle: %+v", snap)
	}
	runtime.Gate.Pause()
	close(release)
	if err := awaitRuntimeResult(t, done); err != nil {
		t.Fatal(err)
	}
	if err := s.finish(); err != nil {
		t.Fatal(err)
	}
	if completed.Load() != 1 || observer.changes.Load() != 3 || runtime.Admission.Snapshot().PendingResults != 0 {
		t.Fatal("completion was duplicated or slots were retained")
	}
}

func TestRequestSessionFinishFailureDoesNotClaimCompletion(t *testing.T) {
	for _, earlierFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "request_completion_save", true: "candidate_save"}[earlierFailure], func(t *testing.T) {
			runtime := testExecutionRuntime(t, 1)
			failure := terminalRuntimeStoreError{errors.New("injected persistence failure")}
			var completeAttempts int
			store := &runtimeTestStore{complete: func(context.Context, string, RequestRecord) error { completeAttempts++; return failure }}
			s, ctx := testRequestSession(runtime, store)
			inner := &runtimeTestBackend{}
			if _, err := BindRequestBackend(inner, 1, 0, runtime).Translate(ctx, backend.Request{}); err != nil {
				t.Fatal(err)
			}
			if earlierFailure {
				s.fail(failure)
			}
			err := s.finish()
			wantAttempts := 1
			if earlierFailure {
				wantAttempts = 0
			}
			if !errors.Is(err, failure) || s.err == nil || completeAttempts != wantAttempts || len(s.pending) != 1 || runtime.Admission.Snapshot().PendingResults != 0 {
				t.Fatalf("failed completion: err=%v attempts=%d pending=%v", err, completeAttempts, s.pending)
			}
			_ = s.finish()
			if completeAttempts != wantAttempts || inner.calls.Load() != 1 {
				t.Fatal("cleanup retried a network request or terminal failed save")
			}
		})
	}
}

func TestCandidateWindowReservationTransferAndResize(t *testing.T) {
	w := NewCandidateWindow(CandidateLimits{Segments: 4, Bytes: 20, ItemBytes: 10})
	if w.TryReserve("batch", 3) || !w.TryReserve("batch", 2) {
		t.Fatal("incorrect full-item reservation")
	}
	if err := w.Transfer("batch", "a", 2); err != nil {
		t.Fatal(err)
	}
	if w.TryReserve("next", 1) {
		t.Fatal("admitted production beyond byte window")
	}
	w.Release("batch")
	if !w.TryReserve("next", 1) {
		t.Fatal("remaining reservation not released")
	}
	if err := w.Transfer("next", "b", -1); !errors.Is(err, ErrCandidateCapacity) {
		t.Fatal("accepted negative payload")
	}
	if err := w.Transfer("next", "b", 3); err != nil {
		t.Fatal(err)
	}
	if err := w.Resize("a", 10); err != nil {
		t.Fatal(err)
	}
	if err := w.ReserveResize("a", 2); err != nil {
		t.Fatal(err)
	}
	if got := w.Snapshot().Bytes; got != 13 {
		t.Fatalf("unconfirmed shrink released bytes: %d", got)
	}
	if err := w.Resize("a", 2); err != nil {
		t.Fatal(err)
	}
	if err := w.Restore("a", 2); err != nil {
		t.Fatal(err)
	}
	if err := w.Restore("a", 3); err == nil {
		t.Fatal("duplicate identity changed capacity")
	}
	if w.TryReserve("a", 1) {
		t.Fatal("candidate identity reused as reservation")
	}
	w.Release("a")
	w.Release("b")
	w.Release("a")
	if got := w.Snapshot(); got.Bytes != 0 || got.Segments != 0 {
		t.Fatalf("leaked window: %+v", got)
	}
}

func TestCandidateWindowRestoresAboveLoweredLimit(t *testing.T) {
	w := NewCandidateWindow(CandidateLimits{Segments: 1, Bytes: 4, ItemBytes: 10})
	for _, key := range []string{"a", "b"} {
		if err := w.Restore(key, 8); err != nil {
			t.Fatal(err)
		}
	}
	if w.TryReserve("new", 1) {
		t.Fatal("new production while over lowered window")
	}
	w.Release("a")
	if w.TryReserve("new", 1) {
		t.Fatal("new production while old candidate remains")
	}
	w.Release("b")
	if !w.TryReserve("single", 1) {
		t.Fatal("single item could not run alone")
	}
	if err := w.Transfer("single", "large", 11); !errors.Is(err, ErrCandidateCapacity) {
		t.Fatal("single item bypassed hard limit")
	}
	if err := w.Restore("old-too-large", 11); !errors.Is(err, ErrCandidateCapacity) {
		t.Fatal("recovery bypassed hard limit")
	}
}

func TestCandidateWindowRejectsArithmeticOverflow(t *testing.T) {
	w := NewCandidateWindow(CandidateLimits{Segments: math.MaxInt, Bytes: math.MaxInt64, ItemBytes: math.MaxInt64})
	if w.TryReserve("overflow", 2) || !w.TryReserve("one", 1) {
		t.Fatal("reservation arithmetic overflow")
	}
	if err := w.Transfer("one", "saved", math.MaxInt64); err != nil {
		t.Fatal(err)
	}
	if err := w.Restore("overflow", 1); !errors.Is(err, ErrCandidateCapacity) {
		t.Fatal("restored total overflowed")
	}
	if got := w.Snapshot().Bytes; got != math.MaxInt64 {
		t.Fatalf("accounting wrapped: %d", got)
	}
}
