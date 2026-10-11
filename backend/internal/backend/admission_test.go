package backend_test

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

type admissionGate struct {
	mu       sync.Mutex
	done     chan struct{}
	paused   bool
	inflight int
}

func newAdmissionGate() *admissionGate         { return &admissionGate{done: make(chan struct{})} }
func (g *admissionGate) Done() <-chan struct{} { return g.done }
func (g *admissionGate) TryStartDispatch(admit func() bool) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.paused || !admit() {
		return false
	}
	g.inflight++
	return true
}
func (g *admissionGate) ReleaseInflight() { g.mu.Lock(); g.inflight--; g.mu.Unlock() }
func (g *admissionGate) Inflight() int    { g.mu.Lock(); defer g.mu.Unlock(); return g.inflight }
func (g *admissionGate) Pause() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.paused {
		g.paused = true
		close(g.done)
	}
}

func newAdmission(t *testing.T, pool *backend.LimiterPool, legacy bool, results int) *backend.RequestAdmission {
	t.Helper()
	a, err := backend.NewRequestAdmission(pool, backend.RequestAdmissionConfig{
		MainConcurrency: map[int]int{0: 1, 1: 1}, AlignmentConcurrency: 2,
		LegacyRoundShared: legacy, MaxCompletions: results,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a
}

func admit(t *testing.T, a *backend.RequestAdmission, stage backend.RequestStage, round, id int, gate backend.DispatchGate) *backend.RequestPermit {
	t.Helper()
	p, wait, err := a.TryAdmit(context.Background(), backend.RequestAdmissionIntent{Stage: stage, RoundIndex: round, BackendID: id}, gate)
	if err != nil || p == nil {
		t.Fatalf("admission: wait=%+v err=%v", wait, err)
	}
	t.Cleanup(p.Release)
	return p
}

func TestRequestAdmissionSeparateAndLegacyBudgets(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "separate", true: "legacy"}[legacy], func(t *testing.T) {
			a := newAdmission(t, nil, legacy, 16)
			main := admit(t, a, backend.RequestStageMain, 0, 1, nil)
			_, wait, err := a.TryAdmit(context.Background(), backend.RequestAdmissionIntent{Stage: backend.RequestStageMain, RoundIndex: 0}, nil)
			if err != nil || wait.Reason == "" {
				t.Fatal("main round exceeded capacity")
			}
			p, wait, err := a.TryAdmit(context.Background(), backend.RequestAdmissionIntent{Stage: backend.RequestStageAlignment, RoundIndex: 0}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if legacy {
				if p != nil || wait.Reason != "legacy_round_concurrency" {
					t.Fatal("legacy budget was not shared")
				}
				main.Release()
				admit(t, a, backend.RequestStageAlignment, 0, 1, nil)
			} else {
				if p == nil {
					t.Fatal("main occupied alignment capacity")
				}
				defer p.Release()
				admit(t, a, backend.RequestStageAlignment, 1, 1, nil)
				p, wait, err = a.TryAdmit(context.Background(), backend.RequestAdmissionIntent{Stage: backend.RequestStageAlignment, RoundIndex: 1}, nil)
				if err != nil || p != nil || wait.Reason != "alignment_concurrency" {
					t.Fatal("alignment budget multiplied across rounds")
				}
			}
			admit(t, a, backend.RequestStageMain, 1, 1, nil)
		})
	}
}

func TestRequestAdmissionJointRPMAndCapacity(t *testing.T) {
	pool := backend.NewLimiterPool()
	pool.Initialize(map[int]int{1: 2, 2: 0})
	defer pool.Shutdown()
	a := newAdmission(t, pool, false, 16)
	main := admit(t, a, backend.RequestStageMain, 0, 1, nil)
	intent := backend.RequestAdmissionIntent{Stage: backend.RequestStageMain, RoundIndex: 0, BackendID: 1}
	if p, wait, _ := a.TryAdmit(context.Background(), intent, nil); p != nil || wait.Reason != "main_concurrency" {
		t.Fatal("expected full main capacity")
	}
	// The failed main admission must not consume the remaining RPM token.
	alignment := admit(t, a, backend.RequestStageAlignment, 0, 1, nil)
	main.Release()
	alignment.Release()
	if p, wait, _ := a.TryAdmit(context.Background(), intent, nil); p != nil || wait.Reason != "backend_rpm" || wait.AvailableAt.IsZero() {
		t.Fatal("expected RPM wait")
	}
	// Waiting for backend 1 must not reserve the main stage against backend 2.
	admit(t, a, backend.RequestStageMain, 0, 2, nil)
	// Another Job has its own main/alignment capacity but shares backend 1 RPM.
	other := newAdmission(t, pool, false, 16)
	if p, wait, _ := other.TryAdmit(context.Background(), intent, nil); p != nil || wait.Reason != "backend_rpm" {
		t.Fatal("RPM was not shared across Jobs")
	}
}

func TestRequestAdmissionResponseCapacityLivesPastHTTP(t *testing.T) {
	a := newAdmission(t, nil, false, 1)
	gate := newAdmissionGate()
	p := admit(t, a, backend.RequestStageMain, 0, 1, gate)
	p.RequestDone()
	if gate.Inflight() != 0 || a.Snapshot().MainInflight[0] != 0 {
		t.Fatal("network capacity not released")
	}
	intent := backend.RequestAdmissionIntent{Stage: backend.RequestStageAlignment, RoundIndex: 0}
	if p, wait, _ := a.TryAdmit(context.Background(), intent, gate); p != nil || wait.Reason != "completion_capacity" {
		t.Fatal("result capacity released before save")
	}
	p.Release()
	p.Release()
	admit(t, a, backend.RequestStageAlignment, 0, 1, gate)
}

func TestRequestAdmissionPauseAndPolicyWakeups(t *testing.T) {
	for _, action := range []string{"pause", "refresh", "remove", "cancel", "close"} {
		t.Run(action, func(t *testing.T) {
			pool := backend.NewLimiterPool()
			pool.Initialize(map[int]int{1: 1})
			defer pool.Shutdown()
			a := newAdmission(t, pool, false, 16)
			gate := newAdmissionGate()
			admit(t, a, backend.RequestStageMain, 0, 1, gate).Release()
			intent := backend.RequestAdmissionIntent{Stage: backend.RequestStageMain, RoundIndex: 0, BackendID: 1}
			_, wait, err := a.TryAdmit(context.Background(), intent, gate)
			if err != nil || wait.Reason != "backend_rpm" {
				t.Fatal("expected exhausted RPM")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				p, err := a.Acquire(ctx, intent, gate)
				if p != nil {
					p.Release()
				}
				done <- err
			}()
			var want error
			switch action {
			case "pause":
				gate.Pause()
				want = backend.ErrDispatchPaused
			case "refresh":
				pool.Refresh(1, 0)
			case "remove":
				pool.Remove(1)
				want = backend.ErrLimiterMissing
			case "cancel":
				cancel()
				want = context.Canceled
			case "close":
				a.Close()
				want = backend.ErrAdmissionClosed
			}
			select {
			case got := <-done:
				if !errors.Is(got, want) {
					t.Fatalf("got %v want %v", got, want)
				}
			case <-time.After(time.Second):
				t.Fatal("admission waiter did not wake")
			}
		})
	}
}

func TestRequestAdmissionPauseBeforeDispatchConsumesNothing(t *testing.T) {
	pool := backend.NewLimiterPool()
	pool.Initialize(map[int]int{1: 1})
	defer pool.Shutdown()
	a := newAdmission(t, pool, false, 16)
	gate := newAdmissionGate()
	gate.Pause()
	intent := backend.RequestAdmissionIntent{Stage: backend.RequestStageMain, RoundIndex: 0, BackendID: 1}
	p, _, err := a.TryAdmit(context.Background(), intent, gate)
	if p != nil || !errors.Is(err, backend.ErrDispatchPaused) {
		t.Fatalf("paused: %v", err)
	}
	if a.Snapshot().PendingResults != 0 || gate.Inflight() != 0 {
		t.Fatal("pause consumed capacity")
	}
	admit(t, a, backend.RequestStageMain, 0, 1, nil)
}

type queuedGrant struct {
	id     string
	permit *backend.RequestPermit
	err    error
}

func enqueueAdmission(a *backend.RequestAdmission, ctx context.Context, id string, resource int, stage backend.RequestStage, backendID int, grants chan<- queuedGrant) {
	go func() {
		p, err := a.Acquire(ctx, backend.RequestAdmissionIntent{Stage: stage, RoundIndex: 0, ResourceID: resource, BackendID: backendID}, nil)
		grants <- queuedGrant{id: id, permit: p, err: err}
	}()
}

func awaitQueued(t *testing.T, a *backend.RequestAdmission, count int64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for a.Snapshot().QueuedRequests != count {
		if time.Now().After(deadline) {
			t.Fatalf("queued=%d want=%d", a.Snapshot().QueuedRequests, count)
		}
		runtime.Gosched()
	}
}

func receiveGrant(t *testing.T, grants <-chan queuedGrant, want string) *backend.RequestPermit {
	t.Helper()
	select {
	case grant := <-grants:
		if grant.id != want || grant.err != nil {
			t.Fatalf("grant=%s err=%v, want %s", grant.id, grant.err, want)
		}
		return grant.permit
	case <-time.After(time.Second):
		t.Fatalf("missing grant %s", want)
		return nil
	}
}

func TestRequestAdmissionQueueRotatesResources(t *testing.T) {
	a := newAdmission(t, nil, false, 16)
	blocker := admit(t, a, backend.RequestStageMain, 0, 1, nil)
	grants := make(chan queuedGrant, 3)
	for i, request := range []struct {
		id       string
		resource int
	}{{"1a", 1}, {"1b", 1}, {"2a", 2}} {
		enqueueAdmission(a, context.Background(), request.id, request.resource, backend.RequestStageMain, 1, grants)
		awaitQueued(t, a, int64(i+1))
	}
	blocker.Release()
	for _, want := range []string{"1a", "2a", "1b"} {
		receiveGrant(t, grants, want).Release()
	}
}

func TestRequestAdmissionQueueAlternatesStagesForSameBackend(t *testing.T) {
	a := newAdmission(t, nil, true, 16)
	blocker := admit(t, a, backend.RequestStageMain, 0, 1, nil)
	grants := make(chan queuedGrant, 4)
	requests := []struct {
		id    string
		stage backend.RequestStage
	}{
		{"main1", backend.RequestStageMain}, {"main2", backend.RequestStageMain},
		{"alignment1", backend.RequestStageAlignment}, {"alignment2", backend.RequestStageAlignment},
	}
	for i, request := range requests {
		enqueueAdmission(a, context.Background(), request.id, 1, request.stage, 1, grants)
		awaitQueued(t, a, int64(i+1))
	}
	blocker.Release()
	for _, want := range []string{"main1", "alignment1", "main2", "alignment2"} {
		receiveGrant(t, grants, want).Release()
	}
}

func TestRequestAdmissionQueueSkipsCoolingBackend(t *testing.T) {
	pool := backend.NewLimiterPool()
	pool.Initialize(map[int]int{1: 1, 2: 0})
	defer pool.Shutdown()
	a := newAdmission(t, pool, false, 16)
	admit(t, a, backend.RequestStageMain, 0, 1, nil).Release()
	grants := make(chan queuedGrant, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	enqueueAdmission(a, ctx, "cooling", 1, backend.RequestStageMain, 1, grants)
	awaitQueued(t, a, 1)
	enqueueAdmission(a, context.Background(), "ready", 2, backend.RequestStageMain, 2, grants)
	receiveGrant(t, grants, "ready").Release()
	pool.Refresh(1, 0)
	receiveGrant(t, grants, "cooling").Release()
}

func TestRequestAdmissionQueueSkipsFullStage(t *testing.T) {
	a := newAdmission(t, nil, false, 16)
	blocker := admit(t, a, backend.RequestStageMain, 0, 1, nil)
	grants := make(chan queuedGrant, 2)
	enqueueAdmission(a, context.Background(), "main", 1, backend.RequestStageMain, 1, grants)
	awaitQueued(t, a, 1)
	enqueueAdmission(a, context.Background(), "alignment", 2, backend.RequestStageAlignment, 1, grants)
	receiveGrant(t, grants, "alignment").Release()
	blocker.Release()
	receiveGrant(t, grants, "main").Release()
}
