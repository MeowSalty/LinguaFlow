// B-T03: shared S17/C4; one retained n16 batch versus a replenished n1 stream.
// The small backlog has 64 members, but success requires the large HTTP before
// the fifth post-probe small grant. Exhausting a finite backlog is a FAILURE.
package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
)

type unequalBatchHTTPEvent struct {
	large                bool
	members, smallGrants int
	release              chan struct{}
}

type fairnessSlowHandoffStore struct {
	*windowResourceStore
	entered, release chan struct{}
	once             sync.Once
}

func (s *fairnessSlowHandoffStore) HandoffResponse(ctx context.Context, response MainResponse) error {
	s.once.Do(func() { close(s.entered); <-s.release })
	return s.windowResourceStore.HandoffResponse(ctx, response)
}

func TestResourceBudgetStoppedProducerYieldsTurnBeforePaidWriterJoins(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 4}})
	if err != nil {
		t.Fatal(err)
	}
	limits := DefaultCandidateLimits()
	limits.Segments = 17
	runtime := NewExecutionRuntime(a, limits, nil)
	defer runtime.Close()
	store := &fairnessSlowHandoffStore{windowResourceStore: &windowResourceStore{RoundStore: NewMemoryRoundStore(nil), resourceID: 1, roundID: 1}, entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	joined, released := false, false
	cleanup := func() {
		cancel()
		if !released {
			close(store.release)
			released = true
		}
		if !joined {
			<-done
			joined = true
		}
	}
	defer cleanup()
	fail := func(format string, args ...any) { cleanup(); t.Fatalf(format, args...) }
	h := &windowBatchProbe{batchSize: 16}
	handler := h.handler(t, runtime)
	handler.MaxWordsPerBatch = 16
	doc := newTestDoc(19)
	doc.Segments[0].Source = "one two three four five six"
	doc.Segments[1].Source = "one two three four five"
	doc.Segments[2].Source = "one two three four five"
	for i := 3; i < len(doc.Segments); i++ {
		doc.Segments[i].Source = "word"
	}
	go func() {
		_, err := RunStagedRound(ctx, Round{Concurrency: 4, Handler: handler}, doc, store, runtime, quietLogger(), progress.Nop{})
		done <- err
	}()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	select {
	case <-store.entered:
	case <-deadline.C:
		fail("paid main did not reach actual reliable writer barrier")
	}
	peer := backend.RequestAdmissionIntent{Stage: backend.RequestStageMain, ResourceID: 2, RoundIndex: 0, BackendID: 1}
	waitReason := func(want string) {
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			hint, err := a.ReadyHint(context.Background(), peer, nil)
			if err == nil && hint.Reason == want {
				return
			}
			select {
			case <-ticker.C:
			case <-deadline.C:
				fail("peer reason%q want%q err%v", hint.Reason, want, err)
			}
		}
	}
	waitReason("fair_main_turn")
	if snapshot := a.PipelineSnapshot(); snapshot.MainOwners[0] != 1 || snapshot.Segments != 3 {
		fail("slow writer lost paid main ownership: %+v", snapshot)
	}
	cancel()
	waitReason("")
	select {
	case err := <-done:
		joined = true
		fail("producer returned before actual paid writer joined: %v", err)
	default:
	}
	if snapshot := a.PipelineSnapshot(); snapshot.MainOwners[0] != 1 || snapshot.Segments != 3 {
		fail("yielding fair turn refunded unfinished paid writer: %+v", snapshot)
	}
	close(store.release)
	released = true
	select {
	case err := <-done:
		joined = true
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stopped writer result: %v", err)
		}
	case <-deadline.C:
		fail("released writer did not join")
	}
	t.Log("public word bound formed first n3 then retained n16; cancelled producer yielded fair turn while paid main owner/S3 stayed live until reliable writer joined, leaving ordinary room for other small work")
}

type unequalBatchHTTP struct {
	mu                         sync.Mutex
	small, large, active, peak int
	events                     chan unequalBatchHTTPEvent
	stop                       chan struct{}
	once                       sync.Once
}

func (h *unequalBatchHTTP) open() { h.once.Do(func() { close(h.stop) }) }

func (h *unequalBatchHTTP) serve(w http.ResponseWriter, r *http.Request) {
	var request backend.Request
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var input struct {
		Segments map[string]prompt.SegmentDetail `json:"segments"`
	}
	if err := json.Unmarshal([]byte(request.User), &input); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	translations := map[string]string{}
	for id, member := range input.Segments {
		if member.Translate {
			translations[id] = "accepted"
		}
	}
	event := unequalBatchHTTPEvent{large: r.URL.Path == "/large", members: len(translations), release: make(chan struct{})}
	h.mu.Lock()
	if event.large {
		h.large++
	} else {
		h.small++
	}
	event.smallGrants = h.small
	h.active++
	h.peak = max(h.peak, h.active)
	h.mu.Unlock()
	finished := false
	defer func() {
		if !finished {
			h.mu.Lock()
			h.active--
			h.mu.Unlock()
		}
	}()
	select {
	case h.events <- event:
	case <-r.Context().Done():
		return
	}
	select {
	case <-event.release:
	case <-h.stop:
	case <-r.Context().Done():
		return
	}
	body, err := json.Marshal(map[string]any{"translations": translations})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.mu.Lock()
	h.active--
	h.mu.Unlock()
	finished = true
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(backend.Response{Text: string(body), Usage: backend.Usage{PromptTokens: 7, CompletionTokens: 11}})
}

// Observe the existing preparation lifecycle log. The first large grant is
// paused after the actual ticket exists and before TryPrepareOwned. Its release
// record then proves that the S=3/n16 Busy probe completed. No production hook,
// manufactured reservation, or success based on elapsed time is used.
// SetDefault is restored by this sequential (nonparallel) test; the handler
// ignores all other records and retains only three constant-sized channels.
type unequalBatchPrepareObserver struct {
	entered, resume, released chan struct{}
	grantOnce, releaseOnce    sync.Once
}

func (*unequalBatchPrepareObserver) Enabled(context.Context, slog.Level) bool { return true }
func (h *unequalBatchPrepareObserver) WithAttrs([]slog.Attr) slog.Handler     { return h }
func (h *unequalBatchPrepareObserver) WithGroup(string) slog.Handler          { return h }
func (h *unequalBatchPrepareObserver) Handle(ctx context.Context, record slog.Record) error {
	resource := int64(0)
	record.Attrs(func(attribute slog.Attr) bool {
		if attribute.Key == "resource_id" {
			resource = attribute.Value.Int64()
		}
		return true
	})
	if resource != 2 {
		return nil
	}
	if record.Message == "preparation granted" {
		first := false
		h.grantOnce.Do(func() { first = true; close(h.entered) })
		if first {
			select {
			case <-h.resume:
			case <-ctx.Done():
			}
		}
	} else if record.Message == "preparation released" {
		h.releaseOnce.Do(func() { close(h.released) })
	}
	return nil
}

// A real refused small request can prove producer throttling. We also inspect
// an upstream queued ReadyHint below, so the test does not require a particular
// layer to implement the fairness barrier.
type unequalBatchSmallStore struct {
	*windowResourceStore
	runtime       *ExecutionRuntime
	fairlyBlocked chan struct{}
}

func (s *unequalBatchSmallStore) AbortRequest(ctx context.Context, id string) error {
	if err := s.windowResourceStore.RoundStore.(RequestAborter).AbortRequest(ctx, id); err != nil {
		return err
	}
	hint, err := s.runtime.Admission.ReadyHint(ctx, backend.RequestAdmissionIntent{RequestID: id, RoundIndex: 0, ResourceID: 1, BackendID: 1, Stage: backend.RequestStageMain}, nil)
	if err == nil && hint.Reason == "fair_main_turn" {
		select {
		case s.fairlyBlocked <- struct{}{}:
		default:
		}
	}
	return nil
}

func TestResourceBudgetLargeCompleteBatchProgressesAgainstContinuousSmall(t *testing.T) {
	const smallBacklog, initialSmall, maxFurtherSmall, largeN = 64, 3, 4, 16
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h := &unequalBatchHTTP{events: make(chan unequalBatchHTTPEvent, smallBacklog+1), stop: make(chan struct{})}
	server := httptest.NewServer(http.HandlerFunc(h.serve))
	defer server.Close()
	pool := backend.NewLimiterPool()
	pool.Initialize(map[int]int{1: 0})
	defer pool.Shutdown()
	a, err := backend.NewRequestAdmission(pool, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 4}})
	if err != nil {
		t.Fatal(err)
	}
	limits := DefaultCandidateLimits()
	limits.Segments = 17
	runtime := NewExecutionRuntime(a, limits, nil)
	defer runtime.Close()
	if err := runtime.SetResponseLimits(1<<20, 64<<20); err != nil {
		t.Fatal(err)
	}
	makeHandler := func(path string, batch int) *TranslateHandler {
		remote := &controlledBackend{client: backend.LimitResponseClient(server.Client(), 1<<20), endpoint: server.URL + path}
		return &TranslateHandler{Backend: BindRequestBackend(remote, 1, 0, runtime), Renderer: newTestRenderer(t), BatchSize: batch, Logger: quietLogger()}
	}
	smallDoc, largeDoc := newTestDoc(smallBacklog), newTestDoc(largeN)
	for i := range smallDoc.Segments {
		smallDoc.Segments[i].Source = fmt.Sprintf("small-%03d", i)
	}
	for i := range largeDoc.Segments {
		largeDoc.Segments[i].Source = fmt.Sprintf("large-%03d", i)
	}
	smallStore := &unequalBatchSmallStore{windowResourceStore: &windowResourceStore{RoundStore: NewMemoryRoundStore(nil), resourceID: 1, roundID: 1}, runtime: runtime, fairlyBlocked: make(chan struct{}, 1)}
	largeStore := &windowResourceStore{RoundStore: NewMemoryRoundStore(nil), resourceID: 2, roundID: 1}
	prepareObserver := &unequalBatchPrepareObserver{entered: make(chan struct{}), resume: make(chan struct{}), released: make(chan struct{})}
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(prepareObserver))
	defer slog.SetDefault(previousLogger)
	type outcome struct {
		result RunRoundResult
		err    error
	}
	smallDone, largeDone := make(chan outcome, 1), make(chan outcome, 1)
	smallJoined, largeJoined, largeStarted := false, false, false
	prepareResumed := false
	cleanup := func() {
		if !prepareResumed {
			close(prepareObserver.resume)
			prepareResumed = true
		}
		h.open()
		cancel()
		if !smallJoined {
			<-smallDone
			smallJoined = true
		}
		if largeStarted && !largeJoined {
			<-largeDone
			largeJoined = true
		}
	}
	defer cleanup()
	fail := func(format string, args ...any) { cleanup(); t.Fatalf(format, args...) }
	go func() {
		result, err := RunStagedRound(ctx, Round{Concurrency: 3, Handler: makeHandler("/small", 1)}, smallDoc, smallStore, runtime, quietLogger(), progress.Nop{})
		smallDone <- outcome{result, err}
	}()
	var initial []unequalBatchHTTPEvent
	for range initialSmall {
		select {
		case event := <-h.events:
			if event.large || event.members != 1 {
				fail("unexpected initial HTTP: %+v", event)
			}
			initial = append(initial, event)
		case out := <-smallDone:
			smallJoined = true
			fail("small producer ended before occupied barrier: %+v %v", out.result, out.err)
		case <-ctx.Done():
			fail("small occupied HTTP barrier did not start")
		}
	}
	if snapshot := runtime.Admission.PipelineSnapshot(); snapshot.Segments != initialSmall || snapshot.MainOwners[0] != initialSmall {
		fail("initial S/C roots do not match actual HTTP: %+v", snapshot)
	}
	largeStarted = true
	go func() {
		result, err := RunStagedRound(ctx, Round{Concurrency: 4, Handler: makeHandler("/large", largeN)}, largeDoc, largeStore, runtime, quietLogger(), progress.Nop{})
		largeDone <- outcome{result, err}
	}()
	select {
	case <-prepareObserver.entered:
	case <-ctx.Done():
		fail("large did not own its real prepare ticket before full probe")
	}
	if snapshot := runtime.Admission.PipelineSnapshot(); snapshot.Segments != initialSmall {
		fail("occupancy changed before large full probe: %+v", snapshot)
	}
	close(prepareObserver.resume)
	prepareResumed = true
	select {
	case <-prepareObserver.released:
	case <-ctx.Done():
		fail("large did not complete its S3/full n16 probe and release prepare")
	}
	// Release exactly one small owner. Keep two real HTTP owners live while the
	// producer replenishes n1 work. If each released slot is repeatedly stolen,
	// the fifth additional small grant fails while 56+ members remain queued.
	close(initial[0].release)
	queueFairlyBlocked := func() bool {
		runtime.Preparation.mu.Lock()
		defer runtime.Preparation.mu.Unlock()
		for _, intent := range runtime.Preparation.queue {
			if intent.ResourceID != 1 {
				continue
			}
			hint, err := runtime.Admission.ReadyHint(ctx, intent, runtime.Gate)
			if err == nil && hint.Reason == "fair_main_turn" {
				return true
			}
		}
		return false
	}
	remainingInitialReleased := false
	releaseRemaining := func() {
		if !remainingInitialReleased {
			close(initial[1].release)
			close(initial[2].release)
			remainingInitialReleased = true
		}
	}
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	var largeEvent unequalBatchHTTPEvent
	for largeEvent.release == nil {
		if !remainingInitialReleased && queueFairlyBlocked() {
			releaseRemaining()
		}
		select {
		case <-smallStore.fairlyBlocked:
			releaseRemaining()
		case event := <-h.events:
			if event.large {
				if !remainingInitialReleased || event.members != largeN || event.smallGrants > initialSmall+maxFurtherSmall || event.smallGrants >= smallBacklog {
					fail("large did not dispatch its retained full batch within bounded small competition: %+v", event)
				}
				largeEvent = event
			} else {
				if event.members != 1 || event.smallGrants > initialSmall+maxFurtherSmall {
					fail("small batches starved complete n16: small_grants=%d post_probe=%d bound=%d backlog_remaining=%d", event.smallGrants, event.smallGrants-initialSmall, maxFurtherSmall, smallBacklog-event.smallGrants)
				}
				close(event.release)
			}
		case <-ticker.C:
		case out := <-largeDone:
			largeJoined = true
			fail("large yielded/failed while actual small consumers could drain: %+v %v", out.result, out.err)
		case out := <-smallDone:
			smallJoined = true
			fail("finite small queue exhausted before large progress: %+v %v", out.result, out.err)
		case <-ctx.Done():
			fail("large made no bounded progress against continuous small competition")
		}
	}
	atLarge := runtime.Admission.PipelineSnapshot()
	if atLarge.Segments < largeN || atLarge.Segments > limits.Segments || atLarge.MainOwners[0] > 4 {
		fail("large HTTP and S17/C4 ledger disagree: %+v", atLarge)
	}
	// Success is already established with a nonempty small backlog. Opening
	// provider barriers now only joins both resources and checks exact work.
	h.open()
	select {
	case out := <-largeDone:
		largeJoined = true
		if out.err != nil || len(out.result.Resolved) != largeN || len(out.result.Unresolved) != 0 {
			fail("large round failed after grant: %+v %v", out.result, out.err)
		}
	case <-ctx.Done():
		fail("large did not join")
	}
	select {
	case out := <-smallDone:
		smallJoined = true
		if out.err != nil || len(out.result.Resolved) != smallBacklog || len(out.result.Unresolved) != 0 {
			fail("small round failed after fairness barrier: %+v %v", out.result, out.err)
		}
	case <-ctx.Done():
		fail("small did not join")
	}
	h.mu.Lock()
	smallCalls, largeCalls, peak := h.small, h.large, h.peak
	h.mu.Unlock()
	if smallCalls != smallBacklog || largeCalls != 1 || peak > 4 {
		fail("actual unequal batch HTTP work differs: small%d large%d peak%d", smallCalls, largeCalls, peak)
	}
	if smallDoc.InputTokens != smallBacklog*7 || smallDoc.OutputTokens != smallBacklog*11 || largeDoc.InputTokens != 7 || largeDoc.OutputTokens != 11 {
		fail("unequal fairness changed paid usage")
	}
	if snapshot := runtime.Admission.PipelineSnapshot(); snapshot.Segments != 0 || snapshot.CandidateBytes != 0 || snapshot.GrowthBytes != 0 || snapshot.MainOwners[0] != 0 {
		fail("unequal resources retained active credit: %+v", snapshot)
	}
	t.Logf("shared_S=17 C=4 large_n=16 small_n=1 initial_small=%d post_probe_small_grants=%d bound=%d backlog_at_large=%d large_actual_HTTP=1 members=16 small_actual_HTTP=%d peak=%d barrier=%+v", initialSmall, largeEvent.smallGrants-initialSmall, maxFurtherSmall, smallBacklog-largeEvent.smallGrants, smallCalls, peak, atLarge)
}
