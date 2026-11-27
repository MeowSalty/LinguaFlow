package pipeline

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
)

type runtimeMetadataObserver struct {
	mu     sync.Mutex
	events []persistence.Observation
}

func (o *runtimeMetadataObserver) Observe(v persistence.Observation) {
	o.mu.Lock()
	o.events = append(o.events, v)
	o.mu.Unlock()
}
func (o *runtimeMetadataObserver) snapshot() []persistence.Observation {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]persistence.Observation(nil), o.events...)
}

func TestStagedRecoveryCommitUsesActualResourceAndRequestMetadata(t *testing.T) {
	for _, alignmentID := range []string{"", "alignment-request"} {
		ctx := context.Background()
		o := &runtimeMetadataObserver{}
		ctx = persistence.WithObserver(ctx, o)
		r := stagedTestRuntime(t, DefaultCandidateLimits())
		store := &nativeStoredRoundTestStore{MemoryRoundStore: NewMemoryRoundStore(nil)}
		_, _ = store.Seal(ctx, []int{0})
		doc := newTestDoc(1)
		c := newCandidate(doc.Segments[0], 0, RoundModeTranslate, "text")
		c.Ready = true
		c.Segment.Target = "recovered target"
		c.ParentRequestID = "main-request"
		c.LastAlignmentRequestID = alignmentID
		if err := store.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
		model := &runtimeTestBackend{}
		result, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: stagedTestHandler(t, r, model)}, doc, store, r, quietLogger(), nil)
		if err != nil || len(result.Resolved) != 1 || model.calls.Load() != 0 {
			t.Fatalf("recovery not committed: %+v %v", result, err)
		}
		wantRequest := "main-request"
		if alignmentID != "" {
			wantRequest = alignmentID
		}
		found := false
		for _, event := range o.snapshot() {
			if event.Phase == "save_execution" && event.Metadata.Operation == "commit" {
				found = true
				if m := event.Metadata; m.ResourceID != 73 || m.RequestID != wantRequest || m.CandidateID != c.ID {
					t.Fatalf("incorrect recovered association: %+v", m)
				}
			}
		}
		if !found {
			t.Fatal("recovered commit was not observed")
		}
	}
}

func TestRuntimePreservesUpstreamObserverWithoutDoubleCounting(t *testing.T) {
	r := testExecutionRuntime(t, 1)
	defaults := r.Observer.(*persistence.Collector)
	upstream := &persistence.Collector{}
	for _, observer := range []persistence.Observer{upstream, defaults} {
		ctx := persistence.WithObserver(context.Background(), observer)
		if err := r.SaveNamed(ctx, persistence.Metadata{Operation: "reserve"}, func(ctx context.Context) error {
			persistence.ObserveTransaction(ctx, "transaction", "committed", time.Millisecond, nil)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if got := upstream.Snapshot().Transactions; got != 1 {
		t.Fatalf("upstream lost event: %d", got)
	}
	if got := defaults.Snapshot().Transactions; got != 2 {
		t.Fatalf("default observer duplicated event: %d", got)
	}
}

func TestRequestAccountingLifetimeRetainsUpstreamObserver(t *testing.T) {
	r := testExecutionRuntime(t, 1)
	r.Lifetime = context.Background()
	o := &runtimeMetadataObserver{}
	s, ctx := testRequestSession(r, &runtimeTestStore{})
	ctx = persistence.WithObserver(ctx, o)
	if _, err := BindRequestBackend(&runtimeTestBackend{}, 1, 0, r).Translate(ctx, backend.Request{}); err != nil {
		t.Fatal(err)
	}
	if err := s.finish(); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, event := range o.snapshot() {
		if event.Phase == "save_execution" {
			counts[event.Metadata.Operation]++
		}
	}
	for _, operation := range []string{"reserve", "sent", "received", "completed"} {
		if counts[operation] != 1 {
			t.Fatalf("accounting observer lost/duplicated %s: %v", operation, counts)
		}
	}
}

func TestRequestBackendWithoutResponseLogsDispatchReturned(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(previous)
	r := testExecutionRuntime(t, 1)
	s, ctx := testRequestSession(r, &runtimeTestStore{})
	b := BindRequestBackend(&runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
		return nil, errors.New("connection closed without response")
	}}, 1, 0, r)
	_, _ = b.Translate(ctx, backend.Request{})
	_ = s.finish()
	if logs := buf.String(); !strings.Contains(logs, "request_phase=dispatch_returned") || strings.Contains(logs, "request_phase=response_received") {
		t.Fatalf("no-response phase claims a response: %s", logs)
	}
}

func TestRuntimeProductionObserverAndOriginalHandoffBudget(t *testing.T) {
	r := testExecutionRuntime(t, 1)
	if r.Observer == nil {
		t.Fatal("runtime has no production persistence observer")
	}
	r.Lifetime = context.Background()
	s, _ := testRequestSession(r, &runtimeTestStore{})
	ctx := s.Context(context.Background())
	deadline := time.Now().Add(20 * time.Millisecond)
	s.startHandoff(deadline)
	for _, advance := range []time.Duration{time.Second, time.Minute, 120 * time.Second} {
		s.startHandoff(deadline.Add(advance))
		if got := persistence.Deadline(s.accountingContext(ctx)); !got.Equal(deadline) {
			t.Fatalf("progress/retry refreshed attempt deadline: %v", got)
		}
	}
	if got := persistence.Deadline(s.accountingContext(ctx)); !got.Equal(deadline) {
		t.Fatalf("later sent or abort budget extended original handoff: %v", got)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("handoff deadline did not stop producer")
	}
	if err := s.finish(); !storageFailure(err) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("handoff lost storage failure cause: %v", err)
	}
}

func TestPreparationBoundsBeforeReservation(t *testing.T) {
	a, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 10}, AlignmentConcurrency: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	p := NewPreparation(a, 2)
	defer p.Close()
	intent := backend.RequestAdmissionIntent{RoundIndex: 0, BackendID: 1, ResourceID: 1, Stage: backend.RequestStageMain}
	first, err := p.Try(context.Background(), intent, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Try(context.Background(), intent, nil); workDeferred(err) == nil {
		t.Fatalf("same resource-stage admitted twice: %v", err)
	}
	intent.ResourceID = 2
	second, err := p.Try(context.Background(), intent, nil)
	if err != nil {
		t.Fatal(err)
	}
	intent.ResourceID = 3
	if _, err = p.Try(context.Background(), intent, nil); workDeferred(err) == nil {
		t.Fatal("global capacity bypassed")
	}
	if n, _ := p.Snapshot(); n != 2 {
		t.Fatalf("pending %d", n)
	}
	first.Release()
	second.Release()
}

// A cold descriptor in a different round must remain queued until a ready
// competitor can displace it. Its RPM deadline stays well beyond this test.
func fullColdPreparationRuntime(t *testing.T) (*ExecutionRuntime, backend.RequestAdmissionIntent, backend.AdmissionWait) {
	t.Helper()
	pool := backend.NewLimiterPool()
	pool.Initialize(map[int]int{1: 0, 2: 1})
	t.Cleanup(pool.Shutdown)
	a, err := backend.NewRequestAdmission(pool, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 1, 1: 1}, AlignmentConcurrency: 2})
	if err != nil {
		t.Fatal(err)
	}
	r := NewExecutionRuntime(a, DefaultCandidateLimits(), nil)
	r.Preparation.Resize(1)
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	cold := backend.RequestAdmissionIntent{RequestID: "cold-RPM", RoundIndex: 1, ResourceID: 88, BackendID: 2, Stage: backend.RequestStageMain}
	if ok, err := a.PrepareMain(cold.RequestID, cold.RoundIndex, cold.ResourceID, 1, 1, 0, false); !ok || err != nil {
		t.Fatal(ok, err)
	}
	permit, _, err := a.TryAdmit(context.Background(), cold, r.Gate)
	if permit == nil || err != nil {
		t.Fatal("cold RPM setup", err)
	}
	permit.Release()
	cold.RequestID = ""
	_, err = r.Preparation.Try(context.Background(), cold, r.Gate)
	deferred := workDeferred(err)
	if deferred == nil || deferred.Wait.Reason != "backend_rpm" || time.Until(deferred.Wait.AvailableAt) < 30*time.Second {
		t.Fatalf("cold descriptor not queued on future RPM: %v", err)
	}
	if pending, queued := r.Preparation.Snapshot(); pending != 0 || queued != 1 {
		t.Fatalf("cold queue setup: pending%d queued%d", pending, queued)
	}
	return r, cold, deferred.Wait
}

func TestPreparationUnqueuedWaitPreservesAdmissionCause(t *testing.T) {
	for _, reason := range []string{"main_concurrency", "backend_rpm", "paused", "admission_closed", "cancelled", "invalid_round"} {
		t.Run(reason, func(t *testing.T) {
			r, cold, _ := fullColdPreparationRuntime(t)
			a, p := r.Admission, r.Preparation
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			own := backend.RequestAdmissionIntent{RoundIndex: 0, ResourceID: 89, BackendID: 1, Stage: backend.RequestStageMain}
			switch reason {
			case "main_concurrency":
				heldIntent := own
				heldIntent.ResourceID = 99
				heldIntent.RequestID = "held-C1"
				if ok, err := a.PrepareMain(heldIntent.RequestID, 0, 99, 1, 1, 0, false); !ok || err != nil {
					t.Fatal(ok, err)
				}
				held, _, err := a.TryAdmit(ctx, heldIntent, r.Gate)
				if held == nil || err != nil {
					t.Fatal(err)
				}
				held.RequestDone()
				defer held.Release()
			case "backend_rpm":
				own.RoundIndex, own.BackendID = cold.RoundIndex, cold.BackendID
			case "paused":
				r.Gate.Pause()
			case "admission_closed":
				a.Close()
			case "cancelled":
				cancel()
			case "invalid_round":
				own.RoundIndex = 2
			}
			want, wantErr := a.ReadyHint(ctx, own, r.Gate)
			changed := p.Changed()
			ticket, err := p.Try(ctx, own, r.Gate)
			if ticket != nil {
				ticket.Release()
				t.Fatal("unready key acquired preparation")
			}
			if wantErr != nil {
				if err == nil || workDeferred(err) != nil || err.Error() != wantErr.Error() {
					t.Fatalf("admission error swallowed: got %v want %v", err, wantErr)
				}
			} else {
				got := workDeferred(err)
				if got == nil || got.Wait.Reason != want.Reason || got.Wait.CapacityChanged != want.CapacityChanged || got.Wait.PolicyChanged != want.PolicyChanged || got.Wait.PauseRequested != want.PauseRequested {
					t.Fatalf("unqueued admission cause lost: got %v want %+v", err, want)
				}
				if delta := got.Wait.AvailableAt.Sub(want.AvailableAt); delta < -time.Millisecond || delta > time.Millisecond {
					t.Fatalf("RPM expiry changed: got %v want %v", got.Wait.AvailableAt, want.AvailableAt)
				}
			}
			if pending, queued := p.Snapshot(); pending != 0 || queued != 1 || mainWaitSignalReady(changed) {
				t.Fatalf("unready/error key changed cold queue: pending%d queued%d", pending, queued)
			}
		})
	}
}

func TestPreparationReadyQueueKeepsHeadPriority(t *testing.T) {
	a, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 10}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	p := NewPreparation(a, 2)
	defer p.Close()
	intent := backend.RequestAdmissionIntent{RoundIndex: 0, BackendID: 1, Stage: backend.RequestStageMain}
	acquire := func(resource int) *PreparationTicket {
		t.Helper()
		intent.ResourceID = resource
		ticket, err := p.Try(context.Background(), intent, nil)
		if err != nil {
			t.Fatal(err)
		}
		return ticket
	}
	defer acquire(1).Release()
	occupied := acquire(2)
	defer occupied.Release()
	deferKey := func(resource int) {
		t.Helper()
		intent.ResourceID = resource
		ticket, err := p.Try(context.Background(), intent, nil)
		if ticket != nil {
			ticket.Release()
			t.Fatalf("resource%d bypassed ready queue head", resource)
		}
		if wait := workDeferred(err); wait == nil || wait.Wait.Reason != "prepare_capacity" {
			t.Fatalf("resource%d lost queue wait: %v", resource, err)
		}
	}
	deferKey(3)
	deferKey(4)
	occupied.Release()
	deferKey(5) // Full descriptor queue has two ready keys; neither may be evicted.
	deferKey(4)
	head := acquire(3)
	head.Release()
	tail := acquire(4)
	tail.Release()
	next := acquire(5)
	next.Release()
}

func TestReadyHintDoesNotSpendRPM(t *testing.T) {
	pool := backend.NewLimiterPool()
	pool.Initialize(map[int]int{1: 1})
	defer pool.Shutdown()
	a, err := backend.NewRequestAdmission(pool, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	i := backend.RequestAdmissionIntent{BackendID: 1, RoundIndex: 0, Stage: backend.RequestStageMain}
	for n := 0; n < 20; n++ {
		hint, err := a.ReadyHint(context.Background(), i, nil)
		if err != nil || hint.Reason != "" {
			t.Fatalf("peek consumed RPM: %+v %v", hint, err)
		}
	}
	permit, err := a.Acquire(context.Background(), i, nil)
	if err != nil {
		t.Fatal(err)
	}
	permit.Release()
}

type uncertainRuntimeStoreError struct{ error }

func (uncertainRuntimeStoreError) Permanent() bool { return true }
func (uncertainRuntimeStoreError) Uncertain() bool { return true }
func (e uncertainRuntimeStoreError) Unwrap() error { return e.error }
func TestRequestBackendUnknownSentStopsAfterReceivedAccounting(t *testing.T) {
	r := testExecutionRuntime(t, 2)
	store := &runtimeTestStore{}
	store.sent = func(context.Context, string, RequestRecord) error {
		return uncertainRuntimeStoreError{errors.New("commit acknowledgement lost")}
	}
	received := false
	store.record = func(_ context.Context, _ string, v RequestRecord) error {
		received = v.State == "received" && v.UsageKnown
		return nil
	}
	s, ctx := testRequestSession(r, store)
	defer s.finish()
	inner := &runtimeTestBackend{}
	b := BindRequestBackend(inner, 1, 0, r)
	if _, err := b.Translate(ctx, backend.Request{}); err == nil || !received {
		t.Fatalf("unknown not stopped/accounted: %v received=%v", err, received)
	}
	if _, err := b.Translate(ctx, backend.Request{}); err == nil {
		t.Fatal("unknown execution continued")
	}
	if inner.calls.Load() != 1 {
		t.Fatal("model called again")
	}
}
func TestPreparationCancellationWakesWaiter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitPreparation(ctx, backend.AdmissionWait{AvailableAt: time.Now().Add(time.Hour)}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestPreparationCancelledHeadDoesNotStarveReadyResource(t *testing.T) {
	a, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	p := NewPreparation(a, 1)
	defer p.Close()
	i := backend.RequestAdmissionIntent{RoundIndex: 0, ResourceID: 1, BackendID: 1, Stage: backend.RequestStageMain}
	ticket, err := p.Try(context.Background(), i, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	i.ResourceID = 2
	if _, err = p.Try(ctx, i, nil); workDeferred(err) == nil {
		t.Fatal("expected queued head")
	}
	cancel()
	at := time.Now().Add(time.Second)
	for {
		_, n := p.Snapshot()
		if n == 0 {
			break
		}
		if time.Now().After(at) {
			t.Fatal("cancelled head retained")
		}
		time.Sleep(time.Millisecond)
	}
	ticket.Release()
	i.ResourceID = 3
	next, err := p.Try(context.Background(), i, nil)
	if err != nil {
		t.Fatalf("ready resource starved: %v", err)
	}
	next.Release()
}

func TestRequestBackendStopsBeforeProviderAndAbortsOnce(t *testing.T) {
	for _, reason := range []string{"parent_cancel", "runtime_stop"} {
		t.Run(reason, func(t *testing.T) {
			r := testExecutionRuntime(t, 2)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			aborts := 0
			store := &runtimeTestStore{reserve: func(context.Context, RequestIntent) error {
				if reason == "parent_cancel" {
					cancel()
				} else {
					r.stopDispatch()
				}
				return nil
			}, abort: func(context.Context, string) error { aborts++; return nil }}
			s, callCtx := testRequestSession(r, store)
			callCtx = context.WithValue(ctx, requestSessionKey{}, s)
			defer s.finish()
			inner := &runtimeTestBackend{}
			b := BindRequestBackend(inner, 1, 0, r)
			if _, err := b.Translate(callCtx, backend.Request{}); err == nil {
				t.Fatal("stopped owner dispatched")
			}
			if inner.calls.Load() != 0 || aborts != 1 {
				t.Fatalf("calls=%d aborts=%d", inner.calls.Load(), aborts)
			}
			if n, _ := r.Preparation.Snapshot(); n != 0 {
				t.Fatalf("confirmed abort retained credit: %d", n)
			}
		})
	}
}

func TestRequestBackendStartedDispatchDrainsAfterStop(t *testing.T) {
	r := testExecutionRuntime(t, 2)
	s, ctx := testRequestSession(r, &runtimeTestStore{})
	defer s.finish()
	inner := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
		r.stopDispatch()
		return &backend.Response{Text: "ok"}, nil
	}}
	b := BindRequestBackend(inner, 1, 0, r)
	if _, err := b.Translate(ctx, backend.Request{}); err != nil {
		t.Fatal(err)
	}
	if err := s.finish(); err != nil {
		t.Fatal(err)
	}
	if inner.calls.Load() != 1 || r.Admission.Snapshot().PendingResults != 0 {
		t.Fatal("started request did not drain")
	}
}

func TestPreparationExpiredLeaseGivesReadyCompetitorPriority(t *testing.T) {
	pool := backend.NewLimiterPool()
	pool.Initialize(map[int]int{1: 1, 2: 0})
	defer pool.Shutdown()
	a, err := backend.NewRequestAdmission(pool, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 2}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	r := NewExecutionRuntime(a, DefaultCandidateLimits(), nil)
	r.Configure(RuntimePolicy{PreparePending: 1, DispatchLease: 5 * time.Millisecond})
	defer r.Close()
	old := backend.RequestAdmissionIntent{RoundIndex: 0, BackendID: 1, ResourceID: 1, Stage: backend.RequestStageMain, RequestID: NewWorkID()}
	if ok, err := a.PrepareMain(old.RequestID, old.RoundIndex, old.ResourceID, 1, 1, 0, false); err != nil || !ok {
		t.Fatalf("warmup request input: %v", err)
	}
	ticket, err := r.Preparation.Try(context.Background(), old, r.Gate)
	if err != nil {
		t.Fatal(err)
	}
	permit, err := a.Acquire(context.Background(), old, r.Gate)
	if err != nil {
		t.Fatal(err)
	}
	permit.Release()
	old.RequestID = NewWorkID()
	if ok, err := a.PrepareMain(old.RequestID, old.RoundIndex, old.ResourceID, 1, 1, 0, false); err != nil || !ok {
		t.Fatalf("reserved lease input: %v", err)
	}
	ticket.MarkReserved()
	b := &requestBackend{runtime: r}
	done := make(chan error, 1)
	go func() { _, e := b.acquire(context.Background(), old, ticket); done <- e }()
	time.Sleep(10 * time.Millisecond)
	// A cold queued description cannot hide the later ready resource.
	if _, err = r.Preparation.Try(context.Background(), old, r.Gate); workDeferred(err) == nil {
		t.Fatal("old group not blocked")
	}
	ready := backend.RequestAdmissionIntent{RoundIndex: 0, BackendID: 2, ResourceID: 2, Stage: backend.RequestStageMain}
	if _, err = r.Preparation.Try(context.Background(), ready, r.Gate); workDeferred(err) == nil {
		t.Fatal("competitor bypassed occupied credit")
	}
	select {
	case err = <-done:
		if workDeferred(err) == nil {
			t.Fatalf("lease did not yield: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("expired lease did not wake for new competitor")
	}
	ticket.Release()
	next, err := r.Preparation.Try(context.Background(), ready, r.Gate)
	if err != nil {
		t.Fatalf("credit not given to ready competitor: %v", err)
	}
	next.Release()
}
