package pipeline

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
)

// 本文件覆盖 executor 收集点的计数/断点登记语义：
// runPool 在批次终态时从 job.idxs 推导 resolved 子集，逐段调用
// SegmentDone（计数）+ SegmentResolved（断点，仅实现 SegmentResolvedNotifier
// 的 Reporter），随后 BatchComplete 触发 flush；handler 不再触碰进度计数。

// resolvedCountingReporter 实现 Reporter + SegmentResolvedNotifier，
// 记录 SegmentDone 次数、SegmentResolved 索引序列与 BatchComplete 次数，
// 用于断言「计数与断点两序列配对」的 checkpoint 不变式。
type resolvedCountingReporter struct {
	mu             sync.Mutex
	segmentDones   int
	resolved       []int // SegmentResolved 收到的 docIndex，按调用顺序
	batchCompletes int
}

func (r *resolvedCountingReporter) StageStart(string, int) {}
func (r *resolvedCountingReporter) SegmentDone()           { r.mu.Lock(); r.segmentDones++; r.mu.Unlock() }
func (r *resolvedCountingReporter) SegmentResolved(docIndex int) {
	r.mu.Lock()
	r.resolved = append(r.resolved, docIndex)
	r.mu.Unlock()
}
func (r *resolvedCountingReporter) BatchComplete() { r.mu.Lock(); r.batchCompletes++; r.mu.Unlock() }
func (r *resolvedCountingReporter) StageDone()     {}
func (r *resolvedCountingReporter) Close() error   { return nil }

func (r *resolvedCountingReporter) snapshot() (dones int, resolved []int, completes int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.segmentDones, append([]int(nil), r.resolved...), r.batchCompletes
}

// resolvedSubsetHandler 是计数语义专用的最小 RoundHandler：
// BuildBatches 把 doc 全部段落（或给定 pending）切成 segsPerBatch 大小的批次，
// ProcessBatch 委托 processFn 产生结果。
type resolvedSubsetHandler struct {
	modeName     string
	segsPerBatch int
	processFn    func(idxs []int, attempt int) batchResult
}

func (h *resolvedSubsetHandler) ModeName() string { return h.modeName }

func (h *resolvedSubsetHandler) BuildBatches(_ context.Context, doc *Document, pending []int, _ int) ([][]int, error) {
	idxs := pending
	if idxs == nil {
		idxs = make([]int, len(doc.Segments))
		for i := range doc.Segments {
			idxs[i] = i
		}
	}
	size := h.segsPerBatch
	if size < 1 {
		size = 1
	}
	var batches [][]int
	for start := 0; start < len(idxs); start += size {
		end := start + size
		if end > len(idxs) {
			end = len(idxs)
		}
		batches = append(batches, idxs[start:end])
	}
	return batches, nil
}

func (h *resolvedSubsetHandler) ProcessBatch(_ context.Context, _ *Document, idxs []int, attempt int, _ *slog.Logger) batchResult {
	if h.processFn != nil {
		return h.processFn(idxs, attempt)
	}
	return batchResult{}
}

func (h *resolvedSubsetHandler) Finalize(_ context.Context, _ *Document, _ []int) error { return nil }

// TestRunRound_SuccessBatchCountsAndRegistersResolved 成功批次：reporter 收到
// SegmentDone×N 与 SegmentResolved×N，且两序列配对（同一判定驱动）；
// 每批一个 BatchComplete（逐批 flush）。
func TestRunRound_SuccessBatchCountsAndRegistersResolved(t *testing.T) {
	doc := newTestDoc(4) // segsPerBatch=2 → 两批
	rep := &resolvedCountingReporter{}
	h := &resolvedSubsetHandler{modeName: "translate", segsPerBatch: 2}
	round := Round{
		Concurrency: 1,
		Retry:       backend.RetryPolicy{MaxAttempts: 0},
		Shrink:      1.0,
		Handler:     h,
	}
	result, err := RunRound(context.Background(), round, doc, nil, quietLogger(), rep)
	if err != nil {
		t.Fatalf("RunRound: %v", err)
	}
	if len(result.Resolved) != 4 {
		t.Fatalf("Resolved=%v want 4 段", result.Resolved)
	}
	dones, resolved, completes := rep.snapshot()
	if dones != 4 {
		t.Fatalf("SegmentDone=%d want 4（每段一次）", dones)
	}
	if len(resolved) != 4 {
		t.Fatalf("SegmentResolved=%v want 4 项（与 SegmentDone 配对）", resolved)
	}
	// 两序列配对：计数增量与断点登记由同一推导产生。
	if dones != len(resolved) {
		t.Fatalf("SegmentDone=%d 与 SegmentResolved=%d 不配对", dones, len(resolved))
	}
	if completes != 2 {
		t.Fatalf("BatchComplete=%d want 2（逐批 flush）", completes)
	}
}

// TestRunRound_FailureShapesExcludedFromCounting 各失败形态（unresolved /
// fatalUnresolved / failedSegments）的段不计数、不断点登记。
func TestRunRound_FailureShapesExcludedFromCounting(t *testing.T) {
	doc := newTestDoc(4)
	rep := &resolvedCountingReporter{}
	h := &resolvedSubsetHandler{modeName: "translate", segsPerBatch: 4}
	h.processFn = func(idxs []int, _ int) batchResult {
		// 段 0 unresolved（进下一池重切）、段 1 fatalUnresolved（跨轮传播）、
		// 段 2 failedSegments（终态扫描失败软警告）、段 3 成功。
		return batchResult{
			unresolved:      []int{0},
			fatalUnresolved: []int{1},
			failedSegments:  []int{2},
		}
	}
	round := Round{
		Concurrency: 1,
		Retry:       backend.RetryPolicy{MaxAttempts: 0}, // 单池：unresolved 并入 finalUnresolved
		Shrink:      1.0,
		Handler:     h,
	}
	result, err := RunRound(context.Background(), round, doc, nil, quietLogger(), rep)
	if err != nil {
		t.Fatalf("RunRound: %v", err)
	}
	dones, resolved, completes := rep.snapshot()
	if dones != 1 {
		t.Fatalf("SegmentDone=%d want 1（仅段 3 resolved）", dones)
	}
	if len(resolved) != 1 || resolved[0] != 3 {
		t.Fatalf("SegmentResolved=%v want [3]", resolved)
	}
	if completes != 1 {
		t.Fatalf("BatchComplete=%d want 1", completes)
	}
	// result.Resolved 与计数口径一致。
	if len(result.Resolved) != 1 || result.Resolved[0] != 3 {
		t.Fatalf("Resolved=%v want [3]", result.Resolved)
	}
}

// TestRunRound_PoolAdvanceNoDoubleCount 池推进不重复计数：池 0 全部 unresolved
// → 池 1 成功 → 每段 SegmentDone/SegmentResolved 恰好一次。
func TestRunRound_PoolAdvanceNoDoubleCount(t *testing.T) {
	doc := newTestDoc(3)
	rep := &resolvedCountingReporter{}
	h := &resolvedSubsetHandler{modeName: "translate", segsPerBatch: 3}
	pool := 0
	h.processFn = func(idxs []int, attempt int) batchResult {
		// BuildBatches 在池 0/池 1 各调用一次（无在途重试，attempt 恒 0），
		// 以 pending 是否为 nil 无法区分（均为 nil 起始），改用外部计数器。
		pool++
		if pool == 1 {
			return batchResult{unresolved: idxs} // 池 0 全失败
		}
		return batchResult{} // 池 1 全成功
	}
	round := Round{
		Concurrency: 1,
		Retry:       backend.RetryPolicy{MaxAttempts: 1}, // maxPools=2
		Shrink:      1.0,
		Handler:     h,
	}
	result, err := RunRound(context.Background(), round, doc, nil, quietLogger(), rep)
	if err != nil {
		t.Fatalf("RunRound: %v", err)
	}
	if len(result.Resolved) != 3 {
		t.Fatalf("Resolved=%v want 3 段", result.Resolved)
	}
	dones, resolved, _ := rep.snapshot()
	if dones != 3 {
		t.Fatalf("SegmentDone=%d want 3（池 0 不计数，池 1 每段一次）", dones)
	}
	if len(resolved) != 3 {
		t.Fatalf("SegmentResolved=%v want 3 项", resolved)
	}
}

// TestRunRound_InFlightRetryNoDoubleCount 池内在途退避重试不计数：retry 批次
// 非终态跳过计数，重试后的终态批次再计，每段恰好一次。
func TestRunRound_InFlightRetryNoDoubleCount(t *testing.T) {
	doc := newTestDoc(2)
	rep := &resolvedCountingReporter{}
	h := &resolvedSubsetHandler{modeName: "translate", segsPerBatch: 2}
	calls := 0
	h.processFn = func(idxs []int, attempt int) batchResult {
		calls++
		if attempt == 0 {
			// 首次尝试：池内 in-flight 重试（attempt 递增，executor 预算内重排）。
			return batchResult{retry: &batchJob{idxs: idxs, attempt: attempt + 1}}
		}
		return batchResult{} // 重试成功
	}
	round := Round{
		Concurrency: 1,
		Retry:       backend.RetryPolicy{MaxAttempts: 2, Backoff: 0},
		Shrink:      1.0,
		Handler:     h,
	}
	// backoffDuration 最小 5s：在途重试会真实退避，直接注入 transientBudget
	// 不可行——走 RunRound 全程需接受一次 5s sleep，-short 模式下跳过。
	if testing.Short() {
		t.Skip("in-flight retry backoff sleeps 5s")
	}
	if _, err := RunRound(context.Background(), round, doc, nil, quietLogger(), rep); err != nil {
		t.Fatalf("RunRound: %v", err)
	}
	if calls != 2 {
		t.Fatalf("ProcessBatch calls=%d want 2（首次 + 在途重试）", calls)
	}
	dones, resolved, _ := rep.snapshot()
	if dones != 2 {
		t.Fatalf("SegmentDone=%d want 2（retry 批次不计数，终态批次每段一次）", dones)
	}
	if len(resolved) != 2 {
		t.Fatalf("SegmentResolved=%v want 2 项", resolved)
	}
}

// TestRunRound_DeferredResultNotCounted 暂停让行（deferred result）：idxs 已进
// nextPending（未解决），收集点跳过一切计数——空 result 不得被误判为全批成功。
func TestRunRound_DeferredResultNotCounted(t *testing.T) {
	doc := newTestDoc(2)
	rep := &resolvedCountingReporter{}
	// 直接构造 deferred result（模拟 runPool 让行点的产物），验证收集点分支：
	// 未暂停场景下 handler 返回 deferred=true 的 result 等价于让行点行为。
	h := &resolvedSubsetHandler{modeName: "translate", segsPerBatch: 2}
	h.processFn = func(_ []int, _ int) batchResult {
		return batchResult{deferred: true}
	}
	round := Round{
		Concurrency: 1,
		Retry:       backend.RetryPolicy{MaxAttempts: 0},
		Shrink:      1.0,
		Handler:     h,
	}
	_, err := RunRound(context.Background(), round, doc, nil, quietLogger(), rep)
	if err != nil {
		t.Fatalf("RunRound: %v", err)
	}
	dones, resolved, completes := rep.snapshot()
	if dones != 0 {
		t.Fatalf("SegmentDone=%d want 0（deferred 批次不计数）", dones)
	}
	if len(resolved) != 0 {
		t.Fatalf("SegmentResolved=%v want 空", resolved)
	}
	if completes != 0 {
		t.Fatalf("BatchComplete=%d want 0（未通知 resolved 即不 flush）", completes)
	}
}

// TestRunRound_PauseMidPoolDoesNotCountSuccess 端到端暂停语义：池内派发前暂停，
// 未派发段落入 Unresolved 且不计数；已派发段成功后正常计数一次。
func TestRunRound_PauseMidPoolDoesNotCountSuccess(t *testing.T) {
	gate := NewPauseGate()
	doc := newTestDoc(3)
	rep := &resolvedCountingReporter{}
	h := &pauseProbeHandler{modeName: "translate", segsPerBatch: 1}
	h.processFn = func(_ *pauseProbeHandler, _ []int) batchResult {
		// 首个批次派发后立即暂停（并发 1 下派发顺序确定）：段 0 在暂停前执行。
		gate.Pause()
		return batchResult{}
	}
	round := Round{
		Concurrency: 1,
		Retry:       backend.RetryPolicy{MaxAttempts: 1},
		Shrink:      1.0,
		Handler:     h,
		Gate:        gate,
	}
	result, err := RunRound(context.Background(), round, doc, nil, quietLogger(), rep)
	if err != nil {
		t.Fatalf("RunRound: %v", err)
	}
	dones, resolved, completes := rep.snapshot()
	// 段 0 成功（计数一次）；段 1、2 暂停让行（deferred，不计数）。
	if dones != 1 {
		t.Fatalf("SegmentDone=%d want 1（仅暂停前成功的段 0）", dones)
	}
	if len(resolved) != 1 || resolved[0] != 0 {
		t.Fatalf("SegmentResolved=%v want [0]", resolved)
	}
	if completes != 1 {
		t.Fatalf("BatchComplete=%d want 1", completes)
	}
	if got := sortedCopy(result.Unresolved); !equalInts(got, []int{1, 2}) {
		t.Fatalf("Unresolved=%v want [1 2]（暂停让行段保持未解决）", got)
	}
}

// TestRunRound_NonNotifierReporterSkipsResolved 仅实现 Reporter 的 reporter
// （如 MemoryCollector/Nop）：类型断言失败则跳过断点登记，计数照常。
func TestRunRound_NonNotifierReporterSkipsResolved(t *testing.T) {
	doc := newTestDoc(2)
	rep := &countingReporter{} // 不实现 SegmentResolvedNotifier
	h := &resolvedSubsetHandler{modeName: "translate", segsPerBatch: 2}
	round := Round{
		Concurrency: 1,
		Retry:       backend.RetryPolicy{MaxAttempts: 0},
		Shrink:      1.0,
		Handler:     h,
	}
	if _, err := RunRound(context.Background(), round, doc, nil, quietLogger(), rep); err != nil {
		t.Fatalf("RunRound: %v", err)
	}
	if got := atomic.LoadInt32(&rep.segmentDones); got != 2 {
		t.Fatalf("SegmentDone=%d want 2", got)
	}
	// 编译期确认 countingReporter 不实现 SegmentResolvedNotifier。
	var _ interface{} = rep
	if _, ok := interface{}(rep).(progress.SegmentResolvedNotifier); ok {
		t.Fatal("countingReporter 不应实现 SegmentResolvedNotifier（否则断言无意义）")
	}
}

// TestRunRound_RevisePartialCountsReturnedOnly revise 部分成功组合：callback
// 段 resolved 计数、missing 段经 unresolved 排除——每段至多通知一次。
func TestRunRound_RevisePartialCountsReturnedOnly(t *testing.T) {
	doc := newTestDoc(3)
	rep := &resolvedCountingReporter{}
	h := &resolvedSubsetHandler{modeName: "revise", segsPerBatch: 3}
	h.processFn = func(idxs []int, _ int) batchResult {
		// 模拟 revise 部分成功：段 0 成功回调，段 1、2 漏返进 unresolved。
		return batchResult{
			callbackResult: &BatchResult{Segments: []TranslatedSegment{{Index: idxs[0], ID: doc.Segments[idxs[0]].ID}}},
			unresolved:     []int{idxs[1], idxs[2]},
		}
	}
	round := Round{
		Concurrency: 1,
		Retry:       backend.RetryPolicy{MaxAttempts: 0},
		Shrink:      1.0,
		Handler:     h,
	}
	result, err := RunRound(context.Background(), round, doc, nil, quietLogger(), rep)
	if err != nil {
		t.Fatalf("RunRound: %v", err)
	}
	dones, resolved, _ := rep.snapshot()
	if dones != 1 {
		t.Fatalf("SegmentDone=%d want 1（仅回调段）", dones)
	}
	if len(resolved) != 1 || resolved[0] != 0 {
		t.Fatalf("SegmentResolved=%v want [0]", resolved)
	}
	if got := sortedCopy(result.Unresolved); !equalInts(got, []int{1, 2}) {
		t.Fatalf("Unresolved=%v want [1 2]", got)
	}
}
