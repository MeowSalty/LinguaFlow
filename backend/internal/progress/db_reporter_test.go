package progress

import (
	"log/slog"
	"sync"
	"testing"
	"time"
)

// flushRecorder 记录 flushFn 的调用次数和参数。
type flushRecorder struct {
	mu        sync.Mutex
	calls     int
	lastBatch []segmentUpdate
}

func (r *flushRecorder) record(updates []segmentUpdate) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.lastBatch = updates
	return nil
}

func (r *flushRecorder) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *flushRecorder) lastUpdates() []segmentUpdate {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastBatch
}

// newTestReporter 创建一个用于测试的 DBReporter，注入 flushFn mock。
// tickerDur 控制定时器安全网间隔；调用方必须 defer r.Close()。
func newTestReporter(tickerDur time.Duration) (*DBReporter, *flushRecorder) {
	rec := &flushRecorder{}
	r := &DBReporter{
		logger: slog.Default(),
		ticker: time.NewTicker(tickerDur),
		done:   make(chan struct{}),
	}
	r.flushFn = rec.record
	go r.runTicker()
	return r, rec
}

// simulateStageStart 模拟 StageStart 的内存状态变更（跳过 DB 写入）。
func simulateStageStart(r *DBReporter, name string, total int) {
	r.mu.Lock()
	r.stageName = name
	r.stageTotal = total
	r.stageDone.Store(0)
	r.mu.Unlock()

	r.flushMu.Lock()
	r.pending = r.pending[:0]
	r.flushMu.Unlock()
}

// TestDBReporter_BasicFlow 测试基本流程：StageStart → SegmentDone × 3 → BatchComplete → Close。
// 验证 flushFn 在 BatchComplete 时被正确调用，Close 时缓冲区已空不再触发。
func TestDBReporter_BasicFlow(t *testing.T) {
	r, rec := newTestReporter(10 * time.Second) // 长间隔避免 ticker 干扰
	defer r.Close()

	simulateStageStart(r, "translate", 10)

	// 3 次 SegmentDone 仅追加缓冲区
	r.SegmentDone()
	r.SegmentDone()
	r.SegmentDone()

	// BatchComplete 立即触发 flush
	r.BatchComplete()

	if got := rec.callCount(); got != 1 {
		t.Fatalf("expected flushFn called 1 time after BatchComplete, got %d", got)
	}
	updates := rec.lastUpdates()
	if len(updates) != 3 {
		t.Fatalf("expected 3 updates in flush batch, got %d", len(updates))
	}
	if last := updates[len(updates)-1].done; last != 3 {
		t.Errorf("expected last done=3, got %d", last)
	}

	// Close 时缓冲区已空，不应再触发 flushFn
	if err := r.Close(); err != nil {
		t.Fatalf("Close err: %v", err)
	}
	if got := rec.callCount(); got != 1 {
		t.Errorf("expected flushFn still 1 after Close, got %d", got)
	}
}

// TestDBReporter_TickerFlush 测试定时器安全网 flush：SegmentDone 后不调 BatchComplete，
// 依赖 ticker 自动触发 flush。
func TestDBReporter_TickerFlush(t *testing.T) {
	r, rec := newTestReporter(100 * time.Millisecond)
	defer r.Close()

	simulateStageStart(r, "translate", 10)
	r.SegmentDone()

	// 等待足够时间让 ticker 触发 flush（100ms ticker，250ms 等待）
	time.Sleep(250 * time.Millisecond)

	if got := rec.callCount(); got < 1 {
		t.Fatalf("expected flushFn called at least 1 time by ticker, got %d", got)
	}

	if err := r.Close(); err != nil {
		t.Fatalf("Close err: %v", err)
	}
}

// TestDBReporter_SegmentDoneBuffer 测试 SegmentDone 只追加缓冲区，
// 没有 BatchComplete/StageDone 时不触发 flush。
func TestDBReporter_SegmentDoneBuffer(t *testing.T) {
	r, rec := newTestReporter(10 * time.Second) // 长间隔避免 ticker 干扰
	defer r.Close()

	simulateStageStart(r, "translate", 10)

	// 多次 SegmentDone 只追加缓冲区
	for i := 0; i < 5; i++ {
		r.SegmentDone()
	}

	// 没有 BatchComplete / StageDone，ticker 间隔 10s 不会触发
	if got := rec.callCount(); got != 0 {
		t.Errorf("expected flushFn not called without BatchComplete, got %d", got)
	}

	// 验证缓冲区中有 5 条记录
	r.flushMu.Lock()
	pendingLen := len(r.pending)
	r.flushMu.Unlock()
	if pendingLen != 5 {
		t.Errorf("expected 5 pending updates, got %d", pendingLen)
	}

	// Close 会做最后一次 flush，清空缓冲区
	if err := r.Close(); err != nil {
		t.Fatalf("Close err: %v", err)
	}
	if got := rec.callCount(); got != 1 {
		t.Errorf("expected flushFn called 1 time (by Close), got %d", got)
	}
}

// TestDBReporter_StageDoneFlush 测试 StageDone 触发 flush。
func TestDBReporter_StageDoneFlush(t *testing.T) {
	r, rec := newTestReporter(10 * time.Second)
	defer r.Close()

	simulateStageStart(r, "translate", 10)

	r.SegmentDone()
	r.SegmentDone()

	// StageDone 触发 flush
	r.StageDone()

	if got := rec.callCount(); got != 1 {
		t.Fatalf("expected flushFn called 1 time after StageDone, got %d", got)
	}
	updates := rec.lastUpdates()
	if len(updates) != 2 {
		t.Fatalf("expected 2 updates in flush batch, got %d", len(updates))
	}
	if last := updates[len(updates)-1].done; last != 2 {
		t.Errorf("expected last done=2, got %d", last)
	}

	// Close 时缓冲区已空
	if err := r.Close(); err != nil {
		t.Fatalf("Close err: %v", err)
	}
	if got := rec.callCount(); got != 1 {
		t.Errorf("expected flushFn still 1 after Close, got %d", got)
	}
}

// TestDBReporter_EmptyFlush 测试空缓冲区不触发 flushFn。
func TestDBReporter_EmptyFlush(t *testing.T) {
	r, rec := newTestReporter(10 * time.Second)

	// 直接调用 flush，缓冲区为空
	if err := r.flush(); err != nil {
		t.Fatalf("flush err: %v", err)
	}
	if got := rec.callCount(); got != 0 {
		t.Errorf("expected flushFn not called on empty buffer, got %d", got)
	}

	// Close 时缓冲区仍为空
	if err := r.Close(); err != nil {
		t.Fatalf("Close err: %v", err)
	}
	if got := rec.callCount(); got != 0 {
		t.Errorf("expected flushFn still not called after Close, got %d", got)
	}
}
