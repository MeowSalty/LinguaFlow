package pipeline

import (
	"context"
	"log/slog"
	"reflect"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
)

func memoryTestRuntime(t *testing.T, concurrency int, gate *PauseGate) *ExecutionRuntime {
	t.Helper()
	runtime, err := NewMemoryExecutionRuntime(map[int]int{0: max(1, concurrency)}, max(1, concurrency), gate)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	})
	return runtime
}

func runMemoryTestRound(t *testing.T, ctx context.Context, round Round, doc *Document, apply func(context.Context, BatchResult) error, logger *slog.Logger, reporter progress.Reporter) (RunRoundResult, error) {
	t.Helper()
	if round.Runtime == nil {
		round.Runtime = memoryTestRuntime(t, round.Concurrency, round.Gate)
	}
	bind := func(b backend.Backend) backend.Backend {
		return BindRequestBackend(b, 1, 0, round.Runtime)
	}
	bindAlignment := func(backends []backend.Backend) []backend.Backend {
		bound := make([]backend.Backend, len(backends))
		for i, b := range backends {
			bound[i] = BindRequestBackend(b, i+2, 0, round.Runtime)
		}
		return bound
	}
	switch h := round.Handler.(type) {
	case *TranslateHandler:
		copy := *h
		copy.Backend = bind(h.Backend)
		copy.RubyRetryBackends = bindAlignment(h.RubyRetryBackends)
		round.Handler = &copy
	case *ReviseHandler:
		copy := *h
		copy.Backend = bind(h.Backend)
		copy.RubyRetryBackends = bindAlignment(h.RubyRetryBackends)
		round.Handler = &copy
	case *AdjudicateHandler:
		copy := *h
		copy.Backend = bind(h.Backend)
		round.Handler = &copy
	case *SemanticQAHandler:
		copy := *h
		copy.Backend = bind(h.Backend)
		round.Handler = &copy
	}
	return RunRound(ctx, round, doc, apply, logger, reporter)
}

func TestRunRoundRequiresRuntime(t *testing.T) {
	h := &CorrectHandler{}
	_, err := RunRound(t.Context(), Round{Handler: h}, newTestDoc(1), nil, nil, nil)
	if err == nil {
		t.Fatal("RunRound accepted a missing runtime")
	}
}

// TestRunRound_MaxAttemptsZeroSinglePool 验证 max_attempts=0 时单池（无重试）。
// 成功一次即终止，仅一个 pool_start 事件。
func TestRunRound_MaxAttemptsZeroSinglePool(t *testing.T) {
	doc := newTestDoc(2)
	rep := &recordingPoolObserver{}
	fb := &fakeBackend{
		name: "fake",
		responses: []string{
			`{"translations":{"1":"a","2":"b"}}`,
		},
	}
	opts := defaultRepairOpts()
	opts.PromptUpgrade = false
	h := newTestTranslateHandler(fb, 2, 1, func(h *TranslateHandler) {
		h.Reporter = rep
		h.Repair = opts
		h.Retry = backend.RetryPolicy{MaxAttempts: 0} // 单池
	})
	h.Renderer = newTestRenderer(t)

	round := Round{
		Concurrency: 1,
		Retry:       h.Retry,
		Shrink:      1.0,
		Handler:     h,
	}
	result, err := runMemoryTestRound(t, context.Background(), round, doc, nil, quietLogger(), rep)
	if err != nil {
		t.Fatalf("RunRound: %v", err)
	}
	if len(result.Unresolved) != 0 {
		t.Fatalf("Unresolved=%v want empty", result.Unresolved)
	}

	rep.mu.Lock()
	defer rep.mu.Unlock()
	// max_attempts=0 → maxPools=1，仅 pool0_start
	if len(rep.events) != 1 {
		t.Fatalf("pool events=%d want 1 (single pool): %+v", len(rep.events), rep.events)
	}
	if rep.events[0].MaxPools != 1 {
		t.Fatalf("events[0].MaxPools=%d want 1", rep.events[0].MaxPools)
	}
}

// TestRunRound_ShrinkOneMultiPoolSameSize 验证 shrink=1.0 + max_attempts=2 → 三池同尺寸重切。
// 第一池全失败（parse）→ 进池 1 → 进池 2；shrink=1.0 不缩批。
func TestRunRound_ShrinkOneMultiPoolSameSize(t *testing.T) {
	doc := newTestDoc(2)
	rep := &recordingPoolObserver{}
	fb := &fakeBackend{
		name: "fake",
		responses: []string{
			"not-json", // pool0 全失败
			// pool1/pool2 同尺寸（shrink=1.0）仍两段一批
			`{"translations":{"1":"a","2":"b"}}`, // pool1 成功
		},
	}
	opts := defaultRepairOpts()
	opts.PromptUpgrade = false
	h := newTestTranslateHandler(fb, 2, 1, func(h *TranslateHandler) {
		h.Reporter = rep
		h.Repair = opts
		h.FallbackShrink = 1.0
		h.Retry = backend.RetryPolicy{MaxAttempts: 2} // maxPools=3
	})
	h.Renderer = newTestRenderer(t)

	round := Round{
		Concurrency: 1,
		Retry:       h.Retry,
		Shrink:      1.0,
		Handler:     h,
	}
	_, err := runMemoryTestRound(t, context.Background(), round, doc, nil, quietLogger(), rep)
	if err != nil {
		t.Fatalf("RunRound: %v", err)
	}

	rep.mu.Lock()
	defer rep.mu.Unlock()
	// 三池：pool0_start → pool0_advance → pool1_start（pool1 成功即终止）
	if len(rep.events) != 3 {
		t.Fatalf("pool events=%d want 3: %+v", len(rep.events), rep.events)
	}
	for i, e := range rep.events {
		if e.MaxPools != 3 {
			t.Fatalf("events[%d].MaxPools=%d want 3", i, e.MaxPools)
		}
		if e.ShrinkRate != 1.0 {
			t.Fatalf("events[%d].ShrinkRate=%v want 1.0", i, e.ShrinkRate)
		}
	}
}

func TestStoredRoundUsesSealedMembership(t *testing.T) {
	runtime := storedTestExecutionRuntime(t)
	store := NewMemoryRoundStore(nil)
	defer store.Close()
	provider := newSuccessfulStoredQAProvider()
	handler := newNativeStoredQA(t, runtime, provider, backend.RetryPolicy{})
	doc := semanticQADoc([]string{"translated", "translated", "translated"}, nil)
	doc.ResolvedIndices = map[int]struct{}{0: {}, 2: {}}
	ctx := t.Context()
	result, err := RunRound(ctx, Round{Runtime: runtime, Store: store, Handler: handler}, doc, nil, quietLogger(), nil)
	if err != nil || !reflect.DeepEqual(result.Resolved, []int{1}) || provider.calls.Load() != 1 {
		t.Fatalf("selected membership: result=%+v err=%v calls=%d", result, err, provider.calls.Load())
	}
	state, err := store.Load(ctx)
	if err != nil || !reflect.DeepEqual(state.Members, []int{1}) {
		t.Fatalf("sealed membership=%+v, err=%v", state, err)
	}
	restarted := semanticQADoc([]string{"translated", "translated", "translated"}, nil)
	restarted.ResolvedIndices = map[int]struct{}{1: {}}
	result, err = RunRound(ctx, Round{Runtime: runtime, Store: store, Handler: handler}, restarted, nil, quietLogger(), nil)
	if err != nil || !reflect.DeepEqual(result.Resolved, []int{1}) || provider.calls.Load() != 1 {
		t.Fatalf("recovered membership: result=%+v err=%v calls=%d", result, err, provider.calls.Load())
	}
}
