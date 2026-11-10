package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
)

type windowResourceStore struct {
	RoundStore
	resourceID int
	roundID    int
}

// These fixtures fill sixteen ordinary member slots. The final contract bills
// main occupancy to S, with one additional slot reserved for unique progress.
func windowFixtureLimits() CandidateLimits {
	l := DefaultCandidateLimits()
	l.Segments = 17
	return l
}

func (s *windowResourceStore) ResourceIdentity() int { return s.resourceID }
func (s *windowResourceStore) RoundIdentity() int    { return s.roundID }

type windowBatchProbe struct {
	batchSize        int
	entered, release chan struct{}
	mu               sync.Mutex
	indices          [][]int
	attempts         []int
}

func (h *windowBatchProbe) handler(t *testing.T, r *ExecutionRuntime) *TranslateHandler {
	t.Helper()
	return &TranslateHandler{Backend: BindRequestBackend(&windowProbeBackend{probe: h}, 1, 0, r), Renderer: newTestRenderer(t), BatchSize: h.batchSize, Logger: quietLogger()}
}

type windowProbeBackend struct{ probe *windowBatchProbe }

func (*windowProbeBackend) Name() string { return "window-provider-probe" }
func (*windowProbeBackend) Close() error { return nil }
func (b *windowProbeBackend) Translate(ctx context.Context, req backend.Request) (*backend.Response, error) {
	session, ok := ctx.Value(requestSessionKey{}).(*requestSession)
	if !ok {
		return nil, errors.New("probe provider has no frozen request session")
	}
	h := b.probe
	h.mu.Lock()
	h.indices = append(h.indices, append([]int(nil), session.intent.Indices...))
	h.attempts = append(h.attempts, session.intent.Attempt)
	first := len(h.indices) == 1
	h.mu.Unlock()
	if first && h.entered != nil {
		close(h.entered)
	}
	if h.release != nil {
		select {
		case <-h.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	var input struct {
		Segments map[string]struct {
			Translate bool `json:"translate"`
		} `json:"segments"`
	}
	if err := json.Unmarshal([]byte(req.User), &input); err != nil {
		return nil, err
	}
	translations := map[string]string{}
	for id, member := range input.Segments {
		if member.Translate {
			translations[id] = "accepted"
		}
	}
	raw, err := json.Marshal(map[string]any{"translations": translations})
	if err != nil {
		return nil, err
	}
	return &backend.Response{Text: string(raw)}, nil
}
func (h *windowBatchProbe) snapshot() ([][]int, []int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([][]int(nil), h.indices...), append([]int(nil), h.attempts...)
}
func waitWindowProbe(t *testing.T, fn func() bool) {
	t.Helper()
	at := time.Now().Add(3 * time.Second)
	for !fn() {
		if time.Now().After(at) {
			t.Fatal("window probe did not reach barrier")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCandidateWindowClassifiesStaticLimitAndBusy(t *testing.T) {
	w := NewCandidateWindow(CandidateLimits{Segments: 8, Bytes: 4 << 20, ItemBytes: 1 << 20})
	if got := w.mainBatchLimit(1); got != 7 {
		t.Fatalf("static ordinary S-1 member limit: %d", got)
	}
	singleton := NewCandidateWindow(CandidateLimits{Segments: 1, Bytes: 4 << 20, ItemBytes: 1 << 20})
	if got, _ := singleton.TryReserveOwned("oversize", 2, 1, nil); got != WindowTooLarge {
		t.Fatalf("static singleton member limit: %v", got)
	}
	if got, _ := w.TryReserveOwned("owner", 4, 1, nil); got != WindowGranted {
		t.Fatal(got)
	}
	got, wake := w.TryReserveOwned("waiter", 4, 2, nil)
	if got != WindowBusy {
		t.Fatalf("temporary occupancy classified as hard limit: %v", got)
	}
	if w.CanDrainFor(nil) {
		t.Fatal("inactive owner treated as draining")
	}
	consumer := w.EnterScope(1, 0)
	if w.CanDrainFor(nil) {
		t.Fatal("new coordinator automatically claimed old entries")
	}
	w.Release("owner")
	if got, _ := w.TryReserveOwned("live", 4, 1, consumer); got != WindowGranted || !w.CanDrainFor(nil) {
		t.Fatal("live reservation not recognized")
	}
	consumer.Leave()
	if w.CanDrainFor(nil) {
		t.Fatal("exited consumer still recognized")
	}
	w.Release("live")
	select {
	case <-wake:
	default:
		t.Fatal("release wake lost")
	}
	if got, _ = w.TryReserveOwned("waiter", 4, 2, nil); got != WindowGranted {
		t.Fatal(got)
	}
	w.Release("waiter")
	if err := w.Restore("unknown", 100); err != nil {
		t.Fatal(err)
	}
	consumer = w.EnterScope(2, 0)
	defer consumer.Leave()
	if w.CanDrainFor(nil) {
		t.Fatal("owner zero treated as draining proof")
	}
	small := NewCandidateWindow(CandidateLimits{Segments: 1, Bytes: 4, ItemBytes: 10})
	if got, _ := small.TryReserveOwned("singleton", 1, 1, nil); got != WindowGranted {
		t.Fatal("lost singleton empty-window exception")
	}
}

func TestStagedWindowHardLimitSplitsOnlyOversizeBatch(t *testing.T) {
	a, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 2}})
	if err != nil {
		t.Fatal(err)
	}
	r := NewExecutionRuntime(a, CandidateLimits{Segments: 8, Bytes: 4 << 20, ItemBytes: 1 << 20}, nil)
	defer r.Close()
	h := &windowBatchProbe{batchSize: 8}
	store := &windowResourceStore{RoundStore: NewMemoryRoundStore(nil), resourceID: 1}
	result, err := RunStagedRound(context.Background(), Round{Concurrency: 2, Handler: h.handler(t, r)}, newTestDoc(8), store, r, quietLogger(), progress.Nop{})
	if err != nil || len(result.Resolved) != 8 {
		t.Fatalf("split failed: %+v %v", result, err)
	}
	batches, _ := h.snapshot()
	if len(batches) != 3 {
		t.Fatalf("static concurrency budget produced unexpected batches: %v", batches)
	}
	for i, batch := range batches {
		want := 3
		if i == 2 {
			want = 2
		}
		if len(batch) != want {
			t.Fatalf("busy fragmented a statically budgeted batch: %v", batches)
		}
	}
}

func TestStagedWindowActiveOwnerWaitsWithSameBatchAndAttempt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r := stagedTestRuntime(t, CandidateLimits{Segments: 8, Bytes: 4 << 20, ItemBytes: 1 << 20})
	first := &windowBatchProbe{batchSize: 4, entered: make(chan struct{}), release: make(chan struct{})}
	ownerStore := &windowResourceStore{RoundStore: NewMemoryRoundStore(nil), resourceID: 1}
	ownerDone := make(chan error, 1)
	done := make(chan error, 1)
	ownerJoined, secondJoined, secondStarted := false, false, false
	var releaseOnce sync.Once
	cleanup := func() {
		releaseOnce.Do(func() { close(first.release) })
		cancel()
		if !ownerJoined {
			<-ownerDone
			ownerJoined = true
		}
		if secondStarted && !secondJoined {
			<-done
			secondJoined = true
		}
	}
	defer cleanup()
	fail := func(format string, args ...any) { cleanup(); t.Fatalf(format, args...) }
	go func() {
		_, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: first.handler(t, r)}, newTestDoc(4), ownerStore, r, quietLogger(), nil)
		ownerDone <- err
	}()
	select {
	case <-first.entered:
	case <-ctx.Done():
		fail("owner provider barrier did not start")
	}
	second := &windowBatchProbe{batchSize: 4}
	store := &windowResourceStore{RoundStore: NewMemoryRoundStore(nil), resourceID: 2}
	_, _ = store.Seal(context.Background(), []int{0, 1, 2, 3})
	var cursors []CursorTransition
	for i := 0; i < 4; i++ {
		cursors = append(cursors, CursorTransition{Index: i, Cursor: WorkCursor{Attempt: 1, State: "pending"}})
	}
	_ = store.Cursors(context.Background(), cursors)
	reporter := &countingReporter{}
	secondStarted = true
	go func() {
		result, err := RunStagedRound(ctx, Round{Concurrency: 1, Retry: backend.RetryPolicy{MaxAttempts: 2}, Handler: second.handler(t, r)}, newTestDoc(4), store, r, quietLogger(), reporter)
		if err == nil && len(result.Resolved) != 4 {
			err = errors.New("waiting member lost")
		}
		done <- err
	}()
	waitWindowProbe(t, func() bool {
		n, _ := r.Preparation.Peaks()
		pending, _ := r.Preparation.Snapshot()
		return n >= 1 && pending == 0 && r.Window.Snapshot().Segments == 4
	})
	if batches, _ := second.snapshot(); len(batches) != 0 {
		fail("busy batch dispatched early: %v", batches)
	}
	releaseOnce.Do(func() { close(first.release) })
	if err := awaitRuntimeResult(t, ownerDone); err != nil {
		ownerJoined = true
		fail("owner failed: %v", err)
	}
	ownerJoined = true
	if err := awaitRuntimeResult(t, done); err != nil {
		secondJoined = true
		fail("waiter failed: %v", err)
	}
	secondJoined = true
	batches, attempts := second.snapshot()
	if len(batches) != 1 || len(batches[0]) != 4 || attempts[0] != 1 {
		t.Fatalf("busy changed batch/attempt: %v %v", batches, attempts)
	}
	if atomic.LoadInt32(&reporter.stageStartCalls) != 1 {
		t.Fatal("active-owner wait restarted stage initialization")
	}
}

func TestStagedWindowInactiveRecoveryOwnerYieldsThenResumes(t *testing.T) {
	r := stagedTestRuntime(t, CandidateLimits{Segments: 8, Bytes: 4 << 20, ItemBytes: 1 << 20})
	foreign := &windowResourceStore{RoundStore: NewMemoryRoundStore(nil), resourceID: 9}
	// Four inactive durable members leave three ordinary S slots; a retained
	// four-member batch must yield without fragmenting on unknown scope owners.
	draftDoc := newTestDoc(4)
	_, _ = foreign.Seal(context.Background(), []int{0, 1, 2, 3})
	for i := range draftDoc.Segments {
		c := newCandidate(draftDoc.Segments[i], i, RoundModeTranslate, "text")
		c.Ready = true
		c.Segment.Target = "saved"
		if err := foreign.Save(context.Background(), c); err != nil {
			t.Fatal(err)
		}
		if err := r.Window.RestoreScoped(c.ID, c.StoredBytes, 9, 0); err != nil {
			t.Fatal(err)
		}
	}
	store := &windowResourceStore{RoundStore: NewMemoryRoundStore(nil), resourceID: 1}
	h := &windowBatchProbe{batchSize: 4}
	doc := newTestDoc(4)
	result, err := RunStagedRound(context.Background(), Round{Concurrency: 1, Handler: h.handler(t, r)}, doc, store, r, quietLogger(), nil)
	if !errors.Is(err, ErrResourceYield) || len(result.Unresolved) != 4 {
		t.Fatalf("inactive owner did not yield: %+v %v", result, err)
	}
	if _, err = RunStagedRound(context.Background(), Round{Concurrency: 1, Handler: (&windowBatchProbe{batchSize: 1}).handler(t, r)}, draftDoc, foreign, r, quietLogger(), nil); err != nil {
		t.Fatal(err)
	}
	if result, err = RunStagedRound(context.Background(), Round{Concurrency: 1, Handler: h.handler(t, r)}, doc, store, r, quietLogger(), nil); err != nil || len(result.Resolved) != 4 {
		t.Fatalf("yield did not resume: %+v %v", result, err)
	}
	batches, _ := h.snapshot()
	if len(batches) != 1 || len(batches[0]) != 4 {
		t.Fatalf("yield fragmented original batch: %v", batches)
	}
}

func TestStagedWindowWaitingPauseCancelLeavesMembersAndCredit(t *testing.T) {
	for _, pause := range []bool{true, false} {
		r := stagedTestRuntime(t, CandidateLimits{Segments: 8, Bytes: 4 << 20, ItemBytes: 1 << 20})
		r.Gate = NewPauseGate()
		consumer := r.Window.EnterScope(9, 0)
		defer consumer.Leave()
		_, _ = r.Window.TryReserveOwned("held", 4, 9, consumer)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		h := &windowBatchProbe{batchSize: 4}
		store := &windowResourceStore{RoundStore: NewMemoryRoundStore(nil), resourceID: 1}
		type outcome struct {
			result RunRoundResult
			err    error
		}
		done := make(chan outcome, 1)
		go func() {
			result, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: h.handler(t, r)}, newTestDoc(4), store, r, quietLogger(), nil)
			done <- outcome{result, err}
		}()
		waitWindowProbe(t, func() bool {
			peak, _ := r.Preparation.Peaks()
			pending, _ := r.Preparation.Snapshot()
			return peak > 0 && pending == 0 && r.Window.Snapshot().Segments > 0
		})
		if pause {
			r.Gate.Pause()
		} else {
			cancel()
		}
		select {
		case out := <-done:
			if pause && out.err != nil || !pause && !errors.Is(out.err, context.Canceled) || len(out.result.Unresolved) != 4 {
				t.Fatalf("waiting cancellation lost members: %+v", out)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("waiting batch did not stop")
		}
		if batches, _ := h.snapshot(); len(batches) != 0 {
			t.Fatal("cancelled waiting batch ran")
		}
		if pending, _ := r.Preparation.Snapshot(); pending != 0 {
			t.Fatal("waiting prepare ticket leaked")
		}
		r.Window.mu.Lock()
		active := 0
		for consumer := range r.Window.consumers {
			if consumer.resourceID == 1 {
				active++
			}
		}
		r.Window.mu.Unlock()
		if active != 0 {
			t.Fatal("waiting resource remained active")
		}
		r.Window.Release("held")
	}
}

func TestCandidateWindowGrowthRemainsImmediateAndDoesNotWaitForProducer(t *testing.T) {
	w := NewCandidateWindow(CandidateLimits{Segments: 4, Bytes: 20, ItemBytes: 10})
	_ = w.RestoreScoped("candidate", 8, 1, 0)
	_ = w.RestoreScoped("other", 2, 2, 0)
	_, _ = w.TryReserveOwned("main", 1, 2, nil)
	if err := w.ReserveResize("candidate", 10); !errors.Is(err, backend.ErrPipelineCapacity) {
		t.Fatalf("growth without full Q pregrant must fail immediately: %v", err)
	}
	if err := w.ledger.AttachProgress(backend.PipelineRuntimeState{Version: 1, ProgressID: "candidate"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := w.ledger.ReserveCandidateGrowth("candidate"); err != nil || !ok {
		t.Fatalf("growth pregrant: %v %v", ok, err)
	}
	if err := w.ReserveResize("candidate", 10); err != nil {
		t.Fatal(err)
	}
	// Main owns only member S. It must not manufacture a W wait after this
	// consumer acquired its full Q before dispatch/save.
	w.Release("main")
	w.Release("candidate")
	w.Release("other")
}

func TestWindowConsumerDoesNotReviveEarlierInstanceClaims(t *testing.T) {
	w := NewCandidateWindow(windowFixtureLimits())
	if err := w.RestoreScoped("recovered", 100, 1, 0); err != nil {
		t.Fatal(err)
	}
	first := w.EnterScope(1, 0)
	if w.CanDrainFor(nil) {
		t.Fatal("header without current-scope Claim considered draining")
	}
	if err := w.ClaimRecovered("recovered", 100, first); err != nil || !w.CanDrainFor(nil) {
		t.Fatalf("current-scope claim failed: %v", err)
	}
	first.Leave()
	second := w.EnterScope(1, 0)
	defer second.Leave()
	if w.CanDrainFor(nil) {
		t.Fatal("same resource next round revived the old consumer")
	}
	if err := w.ClaimRecovered("recovered", 100, first); err == nil {
		t.Fatal("exited consumer reclaimed header")
	}
	if err := w.ClaimRecovered("recovered", 100, second); err != nil || !w.CanDrainFor(nil) {
		t.Fatalf("explicit new-scope claim failed: %v", err)
	}
}

func TestStagedWindowSameResourceFutureRoundWithoutClaimMakesProgress(t *testing.T) {
	// The fixed recovery exception permits the earliest singleton while a
	// later header remains pinned. The later body is never claimed or decoded.
	r := stagedTestRuntime(t, CandidateLimits{Segments: 1, Bytes: 1 << 20, ItemBytes: 1 << 20})
	if err := r.Window.RestoreScoped("future-round", 100, 1, 2); err != nil {
		t.Fatal(err)
	}
	h := &windowBatchProbe{batchSize: 1}
	store := &windowResourceStore{RoundStore: NewMemoryRoundStore(nil), resourceID: 1, roundID: 1}
	type outcome struct {
		result RunRoundResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := RunStagedRound(context.Background(), Round{Concurrency: 1, Handler: h.handler(t, r)}, newTestDoc(1), store, r, quietLogger(), nil)
		done <- outcome{result, err}
	}()
	select {
	case out := <-done:
		if out.err != nil || len(out.result.Resolved) != 1 || len(out.result.Unresolved) != 0 {
			t.Fatalf("earliest singleton failed beside future pin: %+v", out)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("same-resource future candidate caused permanent wait")
	}
	if batches, _ := h.snapshot(); len(batches) != 1 || len(batches[0]) != 1 {
		t.Fatal("earliest singleton was replayed or fragmented")
	}
	if snapshot := r.Window.Snapshot(); snapshot.Segments != 1 || snapshot.Bytes != 100 {
		t.Fatalf("future recovery accounting discarded: %+v", snapshot)
	}
}

type claimedRecoveryProbeStore struct {
	*windowResourceStore
	window *CandidateWindow
	claims atomic.Int32
}

func (s *claimedRecoveryProbeStore) ValidateCandidate(context.Context, *Candidate) (bool, error) {
	if s.window.CanDrainFor(nil) {
		s.claims.Add(1)
	}
	return true, nil
}
func TestStagedWindowCurrentScopeRecoveryPageClaimsConsumer(t *testing.T) {
	r := stagedTestRuntime(t, CandidateLimits{Segments: 8, Bytes: 4 << 20, ItemBytes: 1 << 20})
	store := &claimedRecoveryProbeStore{windowResourceStore: &windowResourceStore{RoundStore: NewMemoryRoundStore(nil), resourceID: 1}, window: r.Window}
	doc := newTestDoc(1)
	_, _ = store.Seal(context.Background(), []int{0})
	c := newCandidate(doc.Segments[0], 0, RoundModeTranslate, "text")
	c.Ready = true
	c.Segment.Target = "saved"
	if err := store.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	data, _ := c.Encode()
	if err := r.Window.RestoreScoped(c.ID, int64(len(data)), 1, 0); err != nil {
		t.Fatal(err)
	}
	h := &windowBatchProbe{batchSize: 1}
	result, err := RunStagedRound(context.Background(), Round{Concurrency: 1, Handler: h.handler(t, r)}, doc, store, r, quietLogger(), nil)
	if err != nil || len(result.Resolved) != 1 || store.claims.Load() != 1 {
		t.Fatalf("current recovery did not claim active consumer: %+v %v claims=%d", result, err, store.claims.Load())
	}
	if batches, _ := h.snapshot(); len(batches) != 0 {
		t.Fatal("recovered member replayed main")
	}
}

func TestStagedWindowPartialFuturePinCompletesEarlierAndRecoversLater(t *testing.T) {
	r := stagedTestRuntime(t, windowFixtureLimits())
	future := &windowResourceStore{RoundStore: NewMemoryRoundStore(nil), resourceID: 1, roundID: 2}
	futureDoc := newTestDoc(1)
	_, _ = future.Seal(context.Background(), []int{0})
	c := newCandidate(futureDoc.Segments[0], 0, RoundModeTranslate, "text")
	c.Ready = true
	c.Segment.Target = "durable later result"
	if err := future.Save(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	payload, _ := c.Encode()
	if err := r.Window.RestoreScoped(c.ID, int64(len(payload)), 1, 2); err != nil {
		t.Fatal(err)
	}
	current := &windowResourceStore{RoundStore: NewMemoryRoundStore(nil), resourceID: 1, roundID: 1}
	var members []int
	for i := 0; i < 16; i++ {
		members = append(members, i)
	}
	_, _ = current.Seal(context.Background(), members)
	var cursors []CursorTransition
	for i := 0; i < 16; i++ {
		cursors = append(cursors, CursorTransition{Index: i, Cursor: WorkCursor{Pool: 0, Attempt: 1, State: "pending"}})
	}
	_ = current.Cursors(context.Background(), cursors)
	h := &windowBatchProbe{batchSize: 16}
	reporter := &countingReporter{}
	result, err := RunStagedRound(context.Background(), Round{Concurrency: 2, Retry: backend.RetryPolicy{MaxAttempts: 2}, Handler: h.handler(t, r)}, newTestDoc(16), current, r, quietLogger(), reporter)
	if err != nil || len(result.Resolved) != 16 || len(result.Unresolved) != 0 {
		t.Fatalf("partial future pin stopped earlier round: %+v %v", result, err)
	}
	batches, attempts := h.snapshot()
	if len(batches) != 2 || len(batches[0]) != 8 || len(batches[1]) != 8 || attempts[0] != 1 || attempts[1] != 1 {
		t.Fatalf("fixed-pin split changed members/attempt: %v %v", batches, attempts)
	}
	seen := make(map[int]bool)
	for _, batch := range batches {
		for _, idx := range batch {
			if seen[idx] {
				t.Fatalf("member repeated: %d", idx)
			}
			seen[idx] = true
		}
	}
	if atomic.LoadInt32(&reporter.stageStartCalls) != 1 {
		t.Fatal("fixed-pin compatibility restarted the resource")
	}
	if got := r.Window.Snapshot(); got.Segments != 1 || got.Bytes != int64(len(payload)) {
		t.Fatalf("later draft capacity discarded: %+v", got)
	}
	laterHandler := &windowBatchProbe{batchSize: 1}
	result, err = RunStagedRound(context.Background(), Round{Concurrency: 1, Handler: laterHandler.handler(t, r)}, futureDoc, future, r, quietLogger(), nil)
	if err != nil || len(result.Resolved) != 1 || futureDoc.Segments[0].Target != "durable later result" {
		t.Fatalf("later round could not recover original draft: %+v %v", result, err)
	}
	if batches, _ := laterHandler.snapshot(); len(batches) != 0 || r.Window.Snapshot().Segments != 0 {
		t.Fatal("later draft was replayed or leaked")
	}
}

func TestStagedWindowPinAndActiveOccupancyWaitAfterFixedSplit(t *testing.T) {
	for _, pin := range []bool{false, true} {
		t.Run(map[bool]string{false: "active_only", true: "fixed_pin_and_active"}[pin], func(t *testing.T) {
			r := stagedTestRuntime(t, windowFixtureLimits())
			if pin {
				if err := r.Window.RestoreScoped("future", 1024, 1, 2); err != nil {
					t.Fatal(err)
				}
			}
			owner := r.Window.EnterScope(9, 9)
			defer owner.Leave()
			if result, _ := r.Window.TryReserveOwned("live", 8, 9, owner); result != WindowGranted {
				t.Fatal(result)
			}
			h := &windowBatchProbe{batchSize: 16}
			store := &windowResourceStore{RoundStore: NewMemoryRoundStore(nil), resourceID: 1, roundID: 1}
			done := make(chan error, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() {
				result, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: h.handler(t, r)}, newTestDoc(16), store, r, quietLogger(), nil)
				if err == nil && len(result.Resolved) != 16 {
					err = errors.New("fixed-pin wait lost members")
				}
				done <- err
			}()
			waitWindowProbe(t, func() bool {
				peak, _ := r.Preparation.Peaks()
				pending, _ := r.Preparation.Snapshot()
				return peak > 0 && pending == 0 && r.Window.Snapshot().Segments > 0
			})
			select {
			case err := <-done:
				t.Fatalf("active consumer did not wait: %v", err)
			case <-time.After(25 * time.Millisecond):
			}
			if batches, _ := h.snapshot(); len(batches) != 0 {
				t.Fatalf("active capacity triggered additional fragmentation: %v", batches)
			}
			r.Window.Release("live")
			if err := awaitRuntimeResult(t, done); err != nil {
				t.Fatal(err)
			}
			batches, _ := h.snapshot()
			if pin && (len(batches) != 2 || len(batches[0]) != 8 || len(batches[1]) != 8) ||
				!pin && (len(batches) != 1 || len(batches[0]) != 16) {
				t.Fatalf("batch ceiling did not distinguish fixed/live occupancy: %v", batches)
			}
		})
	}
}

func TestCandidateWindowFixedPinRequiresDurableScopeAndCompleteRecovery(t *testing.T) {
	for _, kind := range []string{"future", "same_round", "foreign_resource", "unknown_resource", "unknown_round", "active_future", "active_foreign_page", "active_same_future_page", "unknown_active_foreign_page", "unconfirmed_reservation", "unconfirmed_transfer"} {
		t.Run(kind, func(t *testing.T) {
			w := NewCandidateWindow(windowFixtureLimits())
			consumer := w.EnterScope(1, 1)
			defer consumer.Leave()
			resourceID, roundID := 1, 2
			switch kind {
			case "same_round":
				roundID = 1
			case "foreign_resource", "active_foreign_page", "unknown_active_foreign_page":
				resourceID = 9
			case "unknown_resource":
				resourceID = 0
			case "unknown_round":
				roundID = 0
			}
			activeRound := roundID
			if kind == "unknown_active_foreign_page" {
				activeRound = 0
			}
			old := w.EnterScope(resourceID, activeRound)
			defer old.Leave()
			if kind == "unconfirmed_reservation" || kind == "unconfirmed_transfer" {
				if result, _ := w.TryReserveOwned("occupancy", 1, resourceID, old); result != WindowGranted {
					t.Fatal(result)
				}
				if kind == "unconfirmed_transfer" {
					if ok, err := w.ledger.BeginPromotion("occupancy", "draft", 1024, false); err != nil || !ok {
						t.Fatalf("unconfirmed promotion pregrant: %v %v", ok, err)
					}
					defer w.ledger.AbortPromotion("draft")
					// Transfer is the durable transaction ACK boundary. Before it,
					// a staged body cannot become a fixed recovery pin.
				}
				old.Leave()
			} else {
				if err := w.RestoreScoped("occupancy", 1024, resourceID, roundID); err != nil {
					t.Fatal(err)
				}
				if kind == "active_future" {
					if err := w.ClaimRecovered("occupancy", 1024, old); err != nil {
						t.Fatal(err)
					}
				} else if kind != "active_foreign_page" && kind != "active_same_future_page" && kind != "unknown_active_foreign_page" {
					old.Leave()
				}
			}
			if result, _ := w.TryReserveOwned("before_complete", 16, 1, consumer); result != WindowBusy {
				t.Fatalf("incomplete pagination manufactured a fixed ceiling: %v", result)
			}
			consumer.RecoveryComplete()
			want := WindowBusy
			if kind == "future" || kind == "foreign_resource" {
				want = WindowPinnedLimit
			}
			if result, _ := w.TryReserveOwned("after_complete", 16, 1, consumer); result != want {
				t.Fatalf("pin classification %s: got %v want %v", kind, result, want)
			}
			if want == WindowPinnedLimit {
				if err := w.ClaimRecovered("occupancy", 1024, consumer); err == nil {
					t.Fatal("current scope claimed future draft")
				}
			}
		})
	}
}

func TestStagedWindowTwoResourcesFuturePinsMakeProgressWithOneResourceSlot(t *testing.T) {
	r := stagedTestRuntime(t, windowFixtureLimits())
	futureStores := make([]*windowResourceStore, 2)
	futureDocs := make([]*Document, 2)
	for resource := 1; resource <= 2; resource++ {
		store := &windowResourceStore{RoundStore: NewMemoryRoundStore(nil), resourceID: resource, roundID: 2}
		doc := newTestDoc(4)
		_, _ = store.Seal(context.Background(), []int{0, 1, 2, 3})
		for idx := range doc.Segments {
			c := newCandidate(doc.Segments[idx], idx, RoundModeTranslate, "text")
			c.Ready = true
			c.Segment.Target = "original future result"
			payload, _ := c.Encode()
			c.Segment.Source += strings.Repeat("x", (1<<20)-len(payload))
			doc.Segments[idx].Source = c.Segment.Source
			if err := store.Save(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			if c.StoredBytes != 1<<20 {
				t.Fatalf("fixture recovery occupancy is not exact: %d", c.StoredBytes)
			}
			if err := r.Window.RestoreScoped(c.ID, c.StoredBytes, resource, 2); err != nil {
				t.Fatal(err)
			}
		}
		futureStores[resource-1], futureDocs[resource-1] = store, doc
	}
	// One slot admits the entire resource pipeline, matching R=1. Both future
	// rounds start durable/inactive, so neither resource may rely on their drain.
	resourceSlot := make(chan struct{}, 1)
	resourceSlot <- struct{}{}
	for resource := 1; resource <= 2; resource++ {
		<-resourceSlot
		current := &windowResourceStore{RoundStore: NewMemoryRoundStore(nil), resourceID: resource, roundID: 1}
		h := &windowBatchProbe{batchSize: 300}
		result, err := RunStagedRound(context.Background(), Round{Concurrency: 1, Handler: h.handler(t, r)}, newTestDoc(300), current, r, quietLogger(), nil)
		if err != nil || len(result.Resolved) != 300 || len(result.Unresolved) != 0 {
			t.Fatalf("resource %d made no progress with partial foreign pins: %+v %v", resource, result, err)
		}
		batches, attempts := h.snapshot()
		// Main reserves S only: sixteen ordinary slots minus eight/four
		// durable future members. Fixed prefix halving selects eight in either
		// case; later live occupancy may wait but cannot rehalve that prefix.
		firstSize := 8
		if len(batches) == 0 || len(batches[0]) != firstSize {
			t.Fatalf("resource %d did not fit fixed recovery ceiling: %v", resource, batches)
		}
		seen := map[int]bool{}
		for batchIndex, batch := range batches {
			if attempts[batchIndex] != 0 {
				t.Fatal("recovery split consumed an attempt")
			}
			for _, idx := range batch {
				if seen[idx] {
					t.Fatalf("resource %d repeated member %d", resource, idx)
				}
				seen[idx] = true
			}
		}
		if snapshot := r.Window.Snapshot(); snapshot.Segments != (3-resource)*4 || snapshot.Bytes != int64((3-resource)*4)<<20 {
			t.Fatalf("earlier round changed future recovery accounting: %+v", snapshot)
		}
		laterHandler := &windowBatchProbe{batchSize: 4}
		result, err = RunStagedRound(context.Background(), Round{Concurrency: 1, Handler: laterHandler.handler(t, r)}, futureDocs[resource-1], futureStores[resource-1], r, quietLogger(), nil)
		if err != nil || len(result.Resolved) != 4 {
			t.Fatalf("resource %d lost sequential future recovery: %+v %v", resource, result, err)
		}
		if batches, _ := laterHandler.snapshot(); len(batches) != 0 {
			t.Fatal("future draft was redispatched")
		}
		for _, segment := range futureDocs[resource-1].Segments {
			if segment.Target != "original future result" {
				t.Fatal("future draft was replaced")
			}
		}
		resourceSlot <- struct{}{}
	}
	if r.Window.Snapshot().Segments != 0 {
		t.Fatal("recovery capacity leaked")
	}
}

func TestCandidateWindowMultipleRecoveryPinsSingletonBoundary(t *testing.T) {
	for _, available := range []int64{10, 9} {
		w := NewCandidateWindow(CandidateLimits{Segments: 5, Bytes: 40, ItemBytes: 10})
		consumer := w.EnterScope(1, 1)
		consumer.RecoveryComplete()
		defer consumer.Leave()
		for i, bytes := range []int64{10, 10, 8, 12 - available} {
			if err := w.RestoreScoped(string(rune('a'+i)), bytes, i+1, 2); err != nil {
				t.Fatal(err)
			}
		}
		if result, _ := w.TryReserveOwned("batch", 2, 1, consumer); result != WindowPinnedLimit {
			t.Fatal("multiple recovery pins did not form a fixed ceiling")
		}
		if result, _ := w.TryReserveOwned("singleton", 1, 1, consumer); result != WindowPinnedLimit {
			t.Fatalf("all ordinary S pinned before progress: %v", result)
		}
		state, err := w.ledger.MemoryProgress("singleton", "singleton", 1, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err = w.ledger.AttachProgress(state); err != nil {
			t.Fatal(err)
		}
		if result, _ := w.TryReserveOwned("singleton", 1, 1, consumer); result != WindowGranted {
			t.Fatalf("available W %d: unique S singleton got %v", available, result)
		}
		// W does not precharge Q for main. Actual promotion/growth must later
		// acquire W under this same fixed owner and remain within B0+Q.
		w.Release("singleton")
	}
}

type windowReleaseLogProbe struct {
	once    sync.Once
	release func()
	hit     atomic.Bool
}

func (*windowReleaseLogProbe) Enabled(context.Context, slog.Level) bool { return true }
func (p *windowReleaseLogProbe) Handle(_ context.Context, record slog.Record) error {
	if record.Message == "preparation released" {
		record.Attrs(func(attr slog.Attr) bool {
			if attr.Key == "resource_id" && attr.Value.Int64() == 1 {
				p.once.Do(func() {
					p.hit.Store(true)
					p.release()
				})
			}
			return true
		})
	}
	return nil
}
func (p *windowReleaseLogProbe) WithAttrs([]slog.Attr) slog.Handler { return p }
func (p *windowReleaseLogProbe) WithGroup(string) slog.Handler      { return p }

func TestStagedWindowReleaseBetweenBusyAndYieldRetriesRetainedBatch(t *testing.T) {
	r := stagedTestRuntime(t, windowFixtureLimits())
	_ = r.Window.RestoreScoped("future", 1024, 1, 2)
	owner := r.Window.EnterScope(9, 9)
	defer owner.Leave()
	_, _ = r.Window.TryReserveOwned("active", 8, 9, owner)
	logProbe := &windowReleaseLogProbe{release: func() {
		// Preparation.Release executes after TryReserveOwned returned Busy and
		// before CanDrainFor decides whether a live consumer remains.
		r.Window.Release("active")
		owner.Leave()
	}}
	previous := slog.Default()
	slog.SetDefault(slog.New(logProbe))
	defer slog.SetDefault(previous)
	h := &windowBatchProbe{batchSize: 16}
	store := &windowResourceStore{RoundStore: NewMemoryRoundStore(nil), resourceID: 1, roundID: 1}
	result, err := RunStagedRound(context.Background(), Round{Concurrency: 1, Handler: h.handler(t, r)}, newTestDoc(16), store, r, quietLogger(), nil)
	if err != nil || len(result.Resolved) != 16 || !logProbe.hit.Load() {
		t.Fatalf("released capacity caused a false sole-resource yield: %+v %v hook=%v", result, err, logProbe.hit.Load())
	}
	batches, _ := h.snapshot()
	if len(batches) != 2 || len(batches[0]) != 8 || len(batches[1]) != 8 {
		t.Fatalf("release raced batch was rebuilt: %v", batches)
	}
}

func TestCandidateWindowForeignEnteredScopeMustLeaveBeforePin(t *testing.T) {
	w := NewCandidateWindow(windowFixtureLimits())
	current := w.EnterScope(1, 1)
	defer current.Leave()
	current.RecoveryComplete()
	_ = w.RestoreScoped("unclaimed", 1024, 9, 2)
	foreign := w.EnterScope(9, 2)
	defer foreign.Leave()
	if result, wake := w.TryReserveOwned("before_claim", 16, 1, current); result != WindowBusy {
		t.Fatalf("page-loading foreign coordinator manufactured a pin: %v", result)
	} else {
		foreign.Leave()
		select {
		case <-wake:
		default:
			t.Fatal("foreign scope exit did not wake the retained batch")
		}
	}
	if result, _ := w.TryReserveOwned("after_exit", 16, 1, current); result != WindowPinnedLimit {
		t.Fatalf("inactive foreign recovery did not establish fixed ceiling: %v", result)
	}
}

func TestStagedWindowForeignScopeLoadingWaitsForClaimOrExit(t *testing.T) {
	for _, outcome := range []string{"exit", "completed_without_claim", "claim_and_release"} {
		t.Run(outcome, func(t *testing.T) {
			r := stagedTestRuntime(t, windowFixtureLimits())
			_ = r.Window.RestoreScoped("unclaimed", 1024, 9, 2)
			foreign := r.Window.EnterScope(9, 2)
			defer foreign.Leave()
			h := &windowBatchProbe{batchSize: 16}
			store := &windowResourceStore{RoundStore: NewMemoryRoundStore(nil), resourceID: 1, roundID: 1}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				result, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: h.handler(t, r)}, newTestDoc(16), store, r, quietLogger(), nil)
				if err == nil && len(result.Resolved) != 16 {
					err = errors.New("page-loader wait lost members")
				}
				done <- err
			}()
			waitWindowProbe(t, func() bool {
				peak, _ := r.Preparation.Peaks()
				pending, _ := r.Preparation.Snapshot()
				return peak > 0 && pending == 0 && r.Window.Snapshot().Segments > 0
			})
			select {
			case err := <-done:
				t.Fatalf("matching page loader caused early yield: %v", err)
			case <-time.After(25 * time.Millisecond):
			}
			if batches, _ := h.snapshot(); len(batches) != 0 {
				t.Fatalf("page-loading temporary occupancy split the original batch: %v", batches)
			}
			if outcome == "completed_without_claim" {
				foreign.RecoveryComplete()
				if err := awaitRuntimeResult(t, done); !errors.Is(err, ErrResourceYield) {
					t.Fatalf("exhausted page retained a false consumption proof: %v", err)
				}
				return
			}
			if outcome == "claim_and_release" {
				if err := r.Window.ClaimRecovered("unclaimed", 1024, foreign); err != nil {
					t.Fatal(err)
				}
				foreign.RecoveryComplete()
				select {
				case err := <-done:
					t.Fatalf("claimed active candidate stopped temporary waiting: %v", err)
				case <-time.After(25 * time.Millisecond):
				}
				r.Window.Release("unclaimed")
			}
			foreign.Leave()
			if err := awaitRuntimeResult(t, done); err != nil {
				t.Fatal(err)
			}
			batches, _ := h.snapshot()
			if outcome == "exit" && (len(batches) != 2 || len(batches[0]) != 8 || len(batches[1]) != 8) ||
				outcome == "claim_and_release" && (len(batches) != 1 || len(batches[0]) != 16) {
				t.Fatalf("page outcome changed the wrong batch ceiling: %v", batches)
			}
		})
	}
}

func TestStagedWindowFixedPinWaitPauseCancelPreservesBatch(t *testing.T) {
	for _, pause := range []bool{true, false} {
		r := stagedTestRuntime(t, windowFixtureLimits())
		_ = r.Window.RestoreScoped("future", 1024, 1, 2)
		owner := r.Window.EnterScope(9, 9)
		defer owner.Leave()
		_, _ = r.Window.TryReserveOwned("active", 8, 9, owner)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		h := &windowBatchProbe{batchSize: 16}
		store := &windowResourceStore{RoundStore: NewMemoryRoundStore(nil), resourceID: 1, roundID: 1}
		type outcome struct {
			result RunRoundResult
			err    error
		}
		done := make(chan outcome, 1)
		go func() {
			result, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: h.handler(t, r)}, newTestDoc(16), store, r, quietLogger(), nil)
			done <- outcome{result, err}
		}()
		waitWindowProbe(t, func() bool {
			peak, _ := r.Preparation.Peaks()
			pending, _ := r.Preparation.Snapshot()
			return peak > 0 && pending == 0 && r.Window.Snapshot().Segments > 0
		})
		if pause {
			r.Gate.Pause()
		} else {
			cancel()
		}
		select {
		case out := <-done:
			if pause && out.err != nil || !pause && !errors.Is(out.err, context.Canceled) || len(out.result.Unresolved) != 16 {
				t.Fatalf("fixed-pin waiting batch lost members: %+v", out)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("fixed-pin wait failed to stop")
		}
		if batches, _ := h.snapshot(); len(batches) != 0 {
			t.Fatal("cancelled fixed-pin batch dispatched")
		}
		// Eight active main members own S; only the actual future candidate
		// body owns W. Pause/Cancel cannot turn either into phantom payload Q.
		if got := r.Window.Snapshot(); got.Segments != 9 || got.Bytes != 1024 {
			t.Fatalf("waiting changed durable/active accounting: %+v", got)
		}
		if pending, _ := r.Preparation.Snapshot(); pending != 0 {
			t.Fatal("fixed-pin waiter leaked preparation credit")
		}
		state, _ := store.Load(context.Background())
		for _, cursor := range state.Cursors {
			if cursor.Pool != 0 || cursor.Attempt != 0 {
				t.Fatalf("cancelled waiting advanced attempt: %+v", cursor)
			}
		}
		r.Window.Release("active")
	}
}

type windowGrantLogProbe struct{ first, second atomic.Int64 }

func (*windowGrantLogProbe) Enabled(context.Context, slog.Level) bool { return true }
func (p *windowGrantLogProbe) Handle(_ context.Context, record slog.Record) error {
	if record.Message == "preparation granted" {
		record.Attrs(func(attr slog.Attr) bool {
			if attr.Key == "resource_id" {
				switch attr.Value.Int64() {
				case 2:
					p.first.Add(1)
				case 3:
					p.second.Add(1)
				}
			}
			return true
		})
	}
	return nil
}
func (p *windowGrantLogProbe) WithAttrs([]slog.Attr) slog.Handler { return p }
func (p *windowGrantLogProbe) WithGroup(string) slog.Handler      { return p }
func TestStagedWindowMultipleWaitersDoNotWakeEachOthersPreparation(t *testing.T) {
	r := stagedTestRuntime(t, CandidateLimits{Segments: 8, Bytes: 4 << 20, ItemBytes: 1 << 20})
	owner := &windowBatchProbe{batchSize: 4, entered: make(chan struct{}), release: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 3)
	joined := 0
	var releaseOnce sync.Once
	open := func() { releaseOnce.Do(func() { close(owner.release) }) }
	cleanup := func() {
		open()
		cancel()
		for joined < 3 {
			<-done
			joined++
		}
	}
	go func() {
		result, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: owner.handler(t, r)}, newTestDoc(4), &windowResourceStore{RoundStore: NewMemoryRoundStore(nil), resourceID: 1}, r, quietLogger(), nil)
		if err == nil && len(result.Resolved) != 4 {
			err = errors.New("owner lost members")
		}
		done <- err
	}()
	select {
	case <-owner.entered:
	case <-ctx.Done():
		open()
		cancel()
		<-done
		t.Fatal(ctx.Err())
	}
	probes := []*windowBatchProbe{{batchSize: 4}, {batchSize: 4}}
	stores := []*windowResourceStore{{RoundStore: NewMemoryRoundStore(nil), resourceID: 2}, {RoundStore: NewMemoryRoundStore(nil), resourceID: 3}}
	for i := range probes {
		go func(i int) {
			result, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: probes[i].handler(t, r)}, newTestDoc(4), stores[i], r, quietLogger(), nil)
			if err == nil && len(result.Resolved) != 4 {
				err = errors.New("waiter lost members")
			}
			done <- err
		}(i)
	}
	defer cleanup()
	fail := func(format string, args ...any) { cleanup(); t.Fatalf(format, args...) }
	for {
		wake := r.Preparation.Changed()
		pending, queued := r.Preparation.Snapshot()
		if pending == 0 && queued == 2 {
			break
		}
		select {
		case <-wake:
		case err := <-done:
			joined++
			fail("coordinator overtook held owner: %v", err)
		case <-ctx.Done():
			fail("waiter descriptor queue failed: %v", ctx.Err())
		}
	}
	// Short build tickets are gone while the two cold descriptors remain.
	// Neither waiter may produce an intent/attempt or HTTP before the owner
	// releases its durable response capacity; the queues share no sticky ticket.
	if s := r.Admission.PipelineSnapshot(); s.MainOwners[0] != 1 || s.PreparedBytes != 0 {
		fail("waiters retained build/response credit: %+v", s)
	}
	for i, probe := range probes {
		if batches, _ := probe.snapshot(); len(batches) != 0 {
			fail("waiter%d dispatched before owner", i)
		}
		state, err := stores[i].Load(ctx)
		if err != nil {
			fail("load waiter: %v", err)
		}
		for _, c := range state.Cursors {
			if c.Attempt != 0 || c.Pool != 0 {
				fail("waiter advanced durable attempt: %+v", c)
			}
		}
	}
	open()
	for joined < 3 {
		select {
		case err := <-done:
			joined++
			if err != nil {
				fail("wake completion: %v", err)
			}
		case <-ctx.Done():
			fail("owner release did not wake retained waiters: %v", ctx.Err())
		}
	}
	for i, probe := range probes {
		batches, attempts := probe.snapshot()
		if len(batches) != 1 || len(batches[0]) != 4 || len(attempts) != 1 || attempts[0] != 0 {
			fail("waiter%d rebuilt or retried: %v/%v", i, batches, attempts)
		}
	}
}
