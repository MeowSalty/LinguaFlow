package pipeline

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
)

// recordingPoolObserver 实现 Reporter + PoolObserver，记录池级事件。
type recordingPoolObserver struct {
	mu      sync.Mutex
	events  []progress.PoolEvent
	batches []progress.BatchEvent
}

func (r *recordingPoolObserver) StageStart(string, int) {}
func (r *recordingPoolObserver) SegmentDone()           {}
func (r *recordingPoolObserver) BatchComplete()         {}
func (r *recordingPoolObserver) StageDone()             {}
func (r *recordingPoolObserver) Close() error           { return nil }
func (r *recordingPoolObserver) OnBatchEvent(e progress.BatchEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.batches = append(r.batches, e)
}
func (r *recordingPoolObserver) OnPoolEvent(e progress.PoolEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

// TestRunRound_PoolEventsEmittedOnShrink 启用缩批时，pool_start 与 pool_advance 事件按序发布。
func TestRunRound_PoolEventsEmittedOnShrink(t *testing.T) {
	doc := newTestDoc(4)
	rep := &recordingPoolObserver{}

	fb := &fakeBackend{
		name: "fake",
		responses: []string{
			"not-json-at-all", // pool0: parse fail → unresolved whole batch
			// pool1 MaxSegments=2；concurrency=1 保证响应顺序确定
			`{"translations":{"1":"a","2":"b"}}`,
			`{"translations":{"1":"c","2":"d"}}`,
		},
	}
	opts := defaultRepairOpts()
	opts.PromptUpgrade = false
	h := newTestTranslateHandler(fb, 4, 1, func(h *TranslateHandler) {
		h.Reporter = rep
		h.Repair = opts
		h.FallbackShrink = 0.5
		h.Retry = backend.RetryPolicy{MaxAttempts: 2} // maxPools=3
	})
	h.Renderer = newTestRenderer(t)

	round := Round{
		Concurrency: 1,
		Retry:       h.Retry,
		Shrink:      h.FallbackShrink,
		Handler:     h,
	}
	if _, err := RunRound(context.Background(), round, doc, nil, quietLogger(), rep); err != nil {
		t.Fatalf("RunRound: %v", err)
	}

	rep.mu.Lock()
	defer rep.mu.Unlock()

	// 期望事件序列：pool0_start → pool0_advance → pool1_start
	// （pool1 全部解决，无 pool1_advance）
	if len(rep.events) != 3 {
		t.Fatalf("pool events=%d want 3: %+v", len(rep.events), rep.events)
	}
	if rep.events[0].Phase != "pool_start" || rep.events[0].PoolIndex != 0 {
		t.Fatalf("events[0]=%+v want pool_start/0", rep.events[0])
	}
	if rep.events[0].Pending != 4 {
		t.Fatalf("events[0].Pending=%d want 4", rep.events[0].Pending)
	}
	if rep.events[1].Phase != "pool_advance" || rep.events[1].PoolIndex != 0 {
		t.Fatalf("events[1]=%+v want pool_advance/0", rep.events[1])
	}
	if rep.events[1].Pending != 4 {
		t.Fatalf("events[1].Pending=%d want 4 (unresolved from pool0)", rep.events[1].Pending)
	}
	if rep.events[2].Phase != "pool_start" || rep.events[2].PoolIndex != 1 {
		t.Fatalf("events[2]=%+v want pool_start/1", rep.events[2])
	}

	for i, e := range rep.events {
		if e.MaxPools != 3 {
			t.Fatalf("events[%d].MaxPools=%d want 3", i, e.MaxPools)
		}
		if e.Mode != "translate" {
			t.Fatalf("events[%d].Mode=%q want translate", i, e.Mode)
		}
		if e.ShrinkRate != 0.5 {
			t.Fatalf("events[%d].ShrinkRate=%v want 0.5", i, e.ShrinkRate)
		}
	}
}

// TestRunRound_PoolEventsEmittedWithoutShrink shrink=0 时仍发池级事件（池数恒由 max_attempts+1 决定）。
// 第一池全部成功即终止，故仅一个 pool_start 事件。
func TestRunRound_PoolEventsEmittedWithoutShrink(t *testing.T) {
	doc := newTestDoc(2)
	rep := &recordingPoolObserver{}

	fb := &fakeBackend{
		name: "fake",
		responses: []string{
			`{"translations":{"1":"a","2":"b"}}`,
		},
	}
	h := newTestTranslateHandler(fb, 2, 1, func(h *TranslateHandler) {
		h.Reporter = rep
		h.Retry = backend.RetryPolicy{MaxAttempts: 1}
	})
	h.Renderer = newTestRenderer(t)
	// Shrink=0 → 多池同尺寸重切（池数=max_attempts+1=2），仍发池事件
	round := Round{
		Concurrency: 1,
		Retry:       h.Retry,
		Shrink:      0,
		Handler:     h,
	}
	if _, err := RunRound(context.Background(), round, doc, nil, quietLogger(), rep); err != nil {
		t.Fatalf("RunRound: %v", err)
	}

	rep.mu.Lock()
	defer rep.mu.Unlock()
	// 第一池全部成功，循环终止；仅 pool0_start 事件。
	if len(rep.events) != 1 {
		t.Fatalf("pool events=%d want 1 (pool0_start only): %+v", len(rep.events), rep.events)
	}
	if rep.events[0].Phase != "pool_start" || rep.events[0].PoolIndex != 0 {
		t.Fatalf("events[0]=%+v want pool_start/0", rep.events[0])
	}
	if rep.events[0].MaxPools != 2 {
		t.Fatalf("events[0].MaxPools=%d want 2 (max_attempts+1)", rep.events[0].MaxPools)
	}
}

// TestRunRound_PoolObserverNotImplemented 确保 Reporter 未实现 PoolObserver 时不 panic。
func TestRunRound_PoolObserverNotImplemented(t *testing.T) {
	doc := newTestDoc(2)
	// countingReporter 未实现 PoolObserver，仅作为回归测试确保不 panic。
	rep := &countingReporter{}

	fb := &fakeBackend{
		name: "fake",
		errs: []error{errors.New("net err")},
	}
	opts := defaultRepairOpts()
	opts.PromptUpgrade = false
	h := newTestTranslateHandler(fb, 2, 1, func(h *TranslateHandler) {
		h.Reporter = rep
		h.Repair = opts
		h.FallbackShrink = 0.5
		h.Retry = backend.RetryPolicy{MaxAttempts: 1}
	})
	h.Renderer = newTestRenderer(t)

	round := Round{
		Concurrency: 1,
		Retry:       h.Retry,
		Shrink:      h.FallbackShrink,
		Handler:     h,
	}
	// 不应 panic
	if _, err := RunRound(context.Background(), round, doc, nil, quietLogger(), rep); err != nil {
		// 错误可接受，重点是未 panic
		_ = err
	}
}
