package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

type handoffStore struct {
	*MemoryRoundStore
	onSave func(context.Context, *Candidate) error
	onPage func(int, int)
}

func (s *handoffStore) Save(ctx context.Context, c *Candidate) error {
	if s.onSave != nil {
		if err := s.onSave(ctx, c); err != nil {
			return err
		}
	}
	return s.MemoryRoundStore.Save(ctx, c)
}
func (s *handoffStore) Candidates(ctx context.Context, after, limit int) ([]*Candidate, int, error) {
	if s.onPage != nil {
		s.onPage(after, limit)
	}
	return s.MemoryRoundStore.Candidates(ctx, after, limit)
}

func stagedTestRuntime(t *testing.T, limits CandidateLimits) *ExecutionRuntime {
	t.Helper()
	a, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 1}, AlignmentConcurrency: 2})
	if err != nil {
		t.Fatal(err)
	}
	r := NewExecutionRuntime(a, limits, nil)
	r.saveRetryDelay = time.Millisecond
	t.Cleanup(r.Close)
	return r
}
func stagedTestHandler(t *testing.T, r *ExecutionRuntime, b backend.Backend) *TranslateHandler {
	t.Helper()
	return &TranslateHandler{Backend: BindRequestBackend(b, 1, 0, r), Renderer: newTestRenderer(t), BatchSize: 2, Logger: quietLogger()}
}
func awaitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("barrier was not reached")
	}
}

func TestStagedPauseWaitsForHandoffAndResumeCommitsDraft(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r := stagedTestRuntime(t, DefaultCandidateLimits())
	entered, release := make(chan struct{}), make(chan struct{})
	main := &runtimeTestBackend{call: func(ctx context.Context) (*backend.Response, error) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &backend.Response{Text: `{"translations":{"1":"translated"}}`}, nil
	}}
	store := &handoffStore{MemoryRoundStore: NewMemoryRoundStore(nil)}
	saveEntered, saveRelease := make(chan struct{}), make(chan struct{})
	store.onSave = func(ctx context.Context, _ *Candidate) error {
		close(saveEntered)
		select {
		case <-saveRelease:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	doc := newTestDoc(1)
	done := make(chan error, 1)
	go func() {
		_, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: stagedTestHandler(t, r, main)}, doc, store, r, quietLogger(), progress.Nop{})
		done <- err
	}()
	awaitSignal(t, entered)
	r.Gate.Pause()
	close(release)
	awaitSignal(t, saveEntered)
	select {
	case err := <-done:
		t.Fatalf("pause returned before saving: %v", err)
	default:
	}
	if doc.Segments[0].Target != "" {
		t.Fatal("handoff was exposed as accepted translation")
	}
	close(saveRelease)
	if err := awaitRuntimeResult(t, done); err != nil {
		t.Fatal(err)
	}
	state, _ := store.Load(ctx)
	if len(state.Completed) != 0 {
		t.Fatalf("handoff counted as complete: %+v", state)
	}
	page, _, err := store.Candidates(ctx, 0, 16)
	if err != nil || len(page) != 1 {
		t.Fatalf("missing saved draft: %d %v", len(page), err)
	}
	r2 := stagedTestRuntime(t, DefaultCandidateLimits())
	store.onSave = nil
	result, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: stagedTestHandler(t, r2, main)}, doc, store, r2, quietLogger(), progress.Nop{})
	if err != nil || len(result.Resolved) != 1 || main.calls.Load() != 1 || doc.Segments[0].Target != "translated" {
		t.Fatalf("resume=%+v err=%v calls=%d target=%q", result, err, main.calls.Load(), doc.Segments[0].Target)
	}
}

func TestStagedPartialHandoffFailureRetainsAcceptedDraft(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r := stagedTestRuntime(t, DefaultCandidateLimits())
	main := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
		return &backend.Response{Text: `{"translations":{"1":"first","2":"second"}}`}, nil
	}}
	store := &handoffStore{MemoryRoundStore: NewMemoryRoundStore(nil), onSave: func(_ context.Context, c *Candidate) error {
		if c.Index == 1 {
			return terminalRuntimeStoreError{errors.New("disk unavailable")}
		}
		return nil
	}}
	doc := newTestDoc(2)
	_, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: stagedTestHandler(t, r, main)}, doc, store, r, quietLogger(), progress.Nop{})
	var storageErr *StorageError
	if !errors.As(err, &storageErr) || main.calls.Load() != 1 {
		t.Fatalf("storage error became model retry: %v calls=%d", err, main.calls.Load())
	}
	page, _, err := store.Candidates(ctx, 0, 16)
	if err != nil || len(page) != 1 || page[0].Index != 0 {
		t.Fatalf("partial handoff lost: %+v %v", page, err)
	}
	store.onSave = nil
	r2 := stagedTestRuntime(t, DefaultCandidateLimits())
	// The second call is a single-item request whose prompt ID restarts at 1.
	main.call = func(context.Context) (*backend.Response, error) {
		return &backend.Response{Text: `{"translations":{"1":"second"}}`}, nil
	}
	result, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: stagedTestHandler(t, r2, main)}, doc, store, r2, quietLogger(), progress.Nop{})
	if err != nil || len(result.Resolved) != 2 || main.calls.Load() != 2 || doc.Segments[0].Target != "first" || doc.Segments[1].Target != "second" {
		t.Fatalf("resume redid saved segment: %+v %v calls=%d doc=%+v", result, err, main.calls.Load(), doc.Segments)
	}
}

func TestStagedRecoveryPagesDrainBeforeDecodingNextPage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const n = 65
	doc := newTestDoc(n)
	store := &handoffStore{MemoryRoundStore: NewMemoryRoundStore(nil)}
	ids := make([]int, n)
	for i := range ids {
		ids[i] = i
	}
	if _, err := store.Seal(ctx, ids); err != nil {
		t.Fatal(err)
	}
	for i := range ids {
		c := newCandidate(doc.Segments[i], i, RoundModeTranslate, "text")
		c.Ready = true
		c.Segment.Target = fmt.Sprintf("saved %d", i)
		if err := store.Save(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	var pages atomic.Int64
	store.onPage = func(after, limit int) {
		pages.Add(1)
		state, _ := store.Load(ctx)
		if limit > 16 || len(state.Completed) != after {
			t.Errorf("next recovery page loaded before old page drained: after=%d completed=%d limit=%d", after, len(state.Completed), limit)
		}
	}
	r := stagedTestRuntime(t, CandidateLimits{Segments: 1, Bytes: 1 << 20, ItemBytes: 1 << 20})
	main := &runtimeTestBackend{}
	result, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: stagedTestHandler(t, r, main)}, doc, store, r, quietLogger(), progress.Nop{})
	if err != nil || len(result.Resolved) != n || main.calls.Load() != 0 || pages.Load() != 6 {
		t.Fatalf("recovery=%+v %v calls=%d pages=%d", result, err, main.calls.Load(), pages.Load())
	}
	if usage := r.Window.Snapshot(); usage.Bytes != 0 || usage.Segments != 0 {
		t.Fatalf("candidate capacity leaked: %+v", usage)
	}
}

func TestStagedResourceYieldsWhenAnotherResourceOwnsWindow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	doc := newTestDoc(2)
	store := NewMemoryRoundStore(nil)
	if _, err := store.Seal(ctx, []int{0, 1}); err != nil {
		t.Fatal(err)
	}
	c := newCandidate(doc.Segments[0], 0, RoundModeTranslate, "text")
	c.Ready = true
	c.Segment.Target = "saved"
	if err := store.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	r := stagedTestRuntime(t, CandidateLimits{Segments: 1, Bytes: 1 << 20, ItemBytes: 1 << 20})
	if err := r.Window.Restore("other-resource", 128); err != nil {
		t.Fatal(err)
	}
	main := &runtimeTestBackend{}
	result, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: stagedTestHandler(t, r, main)}, doc, store, r, quietLogger(), progress.Nop{})
	if !errors.Is(err, ErrResourceYield) || len(result.Resolved) != 1 || main.calls.Load() != 0 {
		t.Fatalf("did not yield: %+v %v calls=%d", result, err, main.calls.Load())
	}
	if usage := r.Window.Snapshot(); usage.Segments != 1 || usage.Bytes != 128 {
		t.Fatalf("yield lost foreign capacity: %+v", usage)
	}
	state, _ := store.Load(ctx)
	if len(state.Completed) != 1 || state.Cursors[1].Pool != 0 {
		t.Fatalf("yield discarded cursor: %+v", state)
	}
}

func TestStagedRejectedRevisionRetiresPayloadBeforeSuccessor(t *testing.T) {
	ctx := context.Background()
	r := stagedTestRuntime(t, DefaultCandidateLimits())
	store := NewMemoryRoundStore(nil)
	_, _ = store.Seal(ctx, []int{0})
	c := newCandidate(Segment{ID: "one", Source: "source", Target: "accepted", Status: "edited"}, 0, RoundModeRevise, "text")
	c.Segment.Target = "unmapped"
	c.Ready = true
	c.Alignment, _ = ruby.NewAlignmentState("unmapped", "text", []ruby.Item{{ID: "1", SourceBase: "original"}}, nil, ruby.ProtocolV2)
	if err := store.Save(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := r.Window.Restore(c.ID, c.StoredBytes); err != nil {
		t.Fatal(err)
	}
	e := processCandidate(ctx, Round{Handler: &ReviseHandler{}, Retry: backend.RetryPolicy{MaxAttempts: 2}}, c, store, r, quietLogger(), progress.Nop{})
	if e.err != nil || e.outcome != CommitRejected || e.cursors[0].Pool != 1 {
		t.Fatalf("bad rejected successor: %+v", e)
	}
	page, _, err := store.Candidates(ctx, 0, 16)
	if err != nil || len(page) != 0 {
		t.Fatalf("retired payload still active: %d %v", len(page), err)
	}
	state, _ := store.Load(ctx)
	if len(state.Completed) != 0 || state.Cursors[0].Pool != 1 {
		t.Fatalf("rejection counted complete or lost cursor: %+v", state)
	}
}

func TestStagedMainContinuesWhileAlignmentIsBlocked(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 1}, AlignmentConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	r := NewExecutionRuntime(a, DefaultCandidateLimits(), nil)
	defer r.Close()
	secondMain, alignmentEntered, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var mainCalls, alignmentCalls atomic.Int64
	main := &runtimeTestBackend{call: func(context.Context) (*backend.Response, error) {
		if mainCalls.Add(1) == 2 {
			close(secondMain)
		}
		return &backend.Response{Text: `{"translations":{"1":"alpha beta"}}`}, nil
	}}
	align := &runtimeTestBackend{call: func(ctx context.Context) (*backend.Response, error) {
		if alignmentCalls.Add(1) == 1 {
			close(alignmentEntered)
		}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &backend.Response{Text: `{"ruby_output":[{"id":"1","base":"alpha","text":"a","kind":"creative","occurrence":1}]}`}, nil
	}}
	doc := newTestDoc(2)
	for i := range doc.Segments {
		doc.Segments[i].Meta = map[string]any{"ruby_items": []ruby.Item{{ID: "1", SourceBase: "original"}}}
	}
	h := stagedTestHandler(t, r, main)
	h.BatchSize = 1
	h.RubyEnabled = true
	h.RubyProtocolVersion = 2
	h.RubyRetryAttempts = 1
	h.RubyRetryBackends = []backend.Backend{BindRequestBackend(align, 2, 0, r)}
	h.RubyTemplates = testRubyTemplates()
	done := make(chan error, 1)
	go func() {
		_, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: h}, doc, NewMemoryRoundStore(nil), r, quietLogger(), progress.Nop{})
		done <- err
	}()
	awaitSignal(t, alignmentEntered)
	awaitSignal(t, secondMain)
	// Repeated capacity notifications cannot admit the active candidate twice.
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("wake-%d", i)
		if r.Window.TryReserve(id, 1) {
			r.Window.Release(id)
		}
	}
	if alignmentCalls.Load() != 1 {
		t.Fatalf("same-budget candidate was dispatched twice: %d", alignmentCalls.Load())
	}
	close(release)
	if err := awaitRuntimeResult(t, done); err != nil {
		t.Fatal(err)
	}
	if mainCalls.Load() != 2 || alignmentCalls.Load() != 2 {
		t.Fatalf("unexpected retries: main=%d alignment=%d", mainCalls.Load(), alignmentCalls.Load())
	}
}
