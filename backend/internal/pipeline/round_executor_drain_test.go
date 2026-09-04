package pipeline

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

// 本文件覆盖 runPool 的 cleanup 排空与提交确认门：
//   - 排空：worker 已跑完批次（副作用落库、batchHandler 回调成功）但结果尚未
//     被收集时发生取消，结果不得被丢弃——否则计数与断点永久丢失；
//   - 悬挂：results 缓冲满且有 worker 阻塞在发送上时，cleanup 必须先排空再
//     wg.Wait()，否则 runPool 永不返回；
//   - 提交确认门：调用方 ctx 已死时回调成功不代表副作用落库，该批不得计数、
//     不得登记断点（伪造断点会让非翻译轮恢复时永久跳过该段）。

// holdingReporter 在 BatchComplete 处阻塞，用来把收集点按住，制造
// 「结果已产出但未被收集」的窗口。其余回调复用计数语义。
type holdingReporter struct {
	mu             sync.Mutex
	segmentDones   int
	resolved       []int
	batchCompletes int

	hold     chan struct{} // 关闭后放行收集点
	firstHit chan struct{} // 首次进入 BatchComplete 时关闭
	once     sync.Once
}

func newHoldingReporter() *holdingReporter {
	return &holdingReporter{
		hold:     make(chan struct{}),
		firstHit: make(chan struct{}),
	}
}

func (r *holdingReporter) StageStart(string, int) {}
func (r *holdingReporter) SegmentDone()           { r.mu.Lock(); r.segmentDones++; r.mu.Unlock() }
func (r *holdingReporter) SegmentResolved(docIndex int) {
	r.mu.Lock()
	r.resolved = append(r.resolved, docIndex)
	r.mu.Unlock()
}

func (r *holdingReporter) BatchComplete() {
	r.mu.Lock()
	r.batchCompletes++
	r.mu.Unlock()
	r.once.Do(func() { close(r.firstHit) })
	<-r.hold
}

func (r *holdingReporter) StageDone()   {}
func (r *holdingReporter) Close() error { return nil }

func (r *holdingReporter) release() { close(r.hold) }

func (r *holdingReporter) snapshot() (dones int, resolved []int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.segmentDones, append([]int(nil), r.resolved...)
}

// TestRunPool_CleanupDrainKeepsConfirmedBatches 取消竞态下已确认副作用的批次
// 必须仍被计数与登记断点。
//
// 构造：9 批 × 1 段全并发；前 8 批的 batchHandler 记录 ack 后返回 nil（副作用
// 视为已落库），第 9 批等前 8 批 ack 齐、且等测试放行后才返回错误触发
// runCancel。收集点被 holdingReporter 按在首个 BatchComplete 上，因此取消发生时
// 另外 7 批的结果还躺在 results 缓冲里。
//
// 取消由测试放行而非在 ack 齐时立即触发，是为了与提交确认门解耦：门看
// ctx/runCtx，fail-fast 的 runCancel 若抢在已确认批次过门之前落地，那些批次会被
// 门合法让行（副作用未确认），期望值随之不可预测。放行前的等待窗口让前 8 批
// 走完门并把 result 送进缓冲，本测试才只钉「已过门的确认批次不得在排空时丢账」。
//
// 断言的不变式与调度无关：**凡 batchHandler 确认过的段，必须出现在断点登记
// 序列里**。旧实现主循环一见 runCtx.Done() 就 goto cleanup，缓冲里的结果连同
// 计数与断点一起被丢弃 → 段落已在库里、断点却缺失；放行后 select 每轮有一半
// 概率命中 Done，7 条缓冲结果全部逃过丢弃的概率仅 2⁻⁷。
func TestRunPool_CleanupDrainKeepsConfirmedBatches(t *testing.T) {
	const batches = 9
	doc := newTestDoc(batches)
	rep := newHoldingReporter()

	var ackMu sync.Mutex
	acked := map[int]struct{}{}
	allAcked := make(chan struct{})

	h := &resolvedSubsetHandler{modeName: "adjudicate", segsPerBatch: 1}
	h.processFn = func(idxs []int, _ int) batchResult {
		cb := BatchResult{}
		return batchResult{callbackResult: &cb}
	}

	handlerErr := errors.New("persist failed")
	cancelNow := make(chan struct{})
	runCtxCh := make(chan context.Context, 1)
	var captureOnce sync.Once
	batchHandler := func(hctx context.Context, _ BatchResult) error {
		// hctx 即 runPool 的 runCtx：捕获一次供测试观察 fail-fast 取消是否落地，
		// 免去「等取消生效」的定时 sleep。
		captureOnce.Do(func() { runCtxCh <- hctx })
		// callbackResult 不携带段索引，改用 ack 序号区分「前 8 批」与「触发批」。
		ackMu.Lock()
		n := len(acked)
		if n < batches-1 {
			acked[n] = struct{}{}
			if len(acked) == batches-1 {
				close(allAcked)
			}
			ackMu.Unlock()
			return nil
		}
		ackMu.Unlock()
		// 触发批：等前 8 批全部 ack，再等测试放行（那段窗口让前 8 批走完提交
		// 确认门、结果进入缓冲），最后报错取消。
		<-allAcked
		<-cancelNow
		return handlerErr
	}

	round := Round{
		Concurrency: batches,
		Retry:       backend.RetryPolicy{MaxAttempts: 0},
		Shrink:      1.0,
		Handler:     h,
	}

	done := make(chan error, 1)
	go func() {
		_, err := RunRound(context.Background(), round, doc, batchHandler, quietLogger(), rep)
		done <- err
	}()

	// 等收集点被按住并等取消触发，再放行——保证取消时缓冲里确有未收集结果。
	select {
	case <-rep.firstHit:
	case <-time.After(5 * time.Second):
		t.Fatal("收集点未进入 BatchComplete，构造失败")
	}
	select {
	case <-allAcked:
	case <-time.After(5 * time.Second):
		t.Fatal("前 8 批未全部 ack，构造失败")
	}
	// 前 8 批 ack 后还要走完提交确认门、把 result 送进 results 缓冲，之后才该轮到
	// 取消：留一小段窗口，避免把「已确认但未过门」的批次算进期望（门会正确地
	// 让行它们）。
	time.Sleep(100 * time.Millisecond)
	close(cancelNow)
	// 取消已落地（runCtx 关闭）后才放行收集点——否则收集点会在 runCtx 仍存活时
	// 把缓冲消化干净，构造出的场景与「取消时缓冲里还有结果」无关，回归意义随之消失。
	runCtx := <-runCtxCh
	select {
	case <-runCtx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("fail-fast 取消未落地，构造失败")
	}
	rep.release()

	select {
	case err := <-done:
		if !errors.Is(err, handlerErr) {
			t.Fatalf("RunRound err=%v，want batchHandler 错误上抛", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunRound 未在 10s 内返回（cleanup 排空缺失导致悬挂）")
	}

	dones, resolved := rep.snapshot()
	ackMu.Lock()
	wantAcked := len(acked)
	ackMu.Unlock()
	if len(resolved) != wantAcked {
		t.Fatalf("SegmentResolved=%d 项，want %d（每个已确认批次一段，取消不得丢账）",
			len(resolved), wantAcked)
	}
	if dones != wantAcked {
		t.Fatalf("SegmentDone=%d，want %d（与断点登记配对）", dones, wantAcked)
	}
}

// TestRunPool_CleanupDrainNoDeadlockWhenResultsFull results 缓冲满 + 多个 worker
// 阻塞在发送上时取消，runPool 必须仍在有界时间内返回。
//
// 旧实现的 cleanup 是 close(done) → submitWg.Wait() → close(jobs) → wg.Wait()：
// 没有任何一方再从 results 接收，阻塞在发送上的 worker 永远醒不过来，wg.Wait()
// 永不返回 → runPool 悬挂 → 资源 goroutine 与 processJob 一起卡死。
//
// 构造：concurrency=4（results 容量 8）、20 批 × 1 段，收集点被按在首个
// BatchComplete 上 → 1 条被收集、8 条填满缓冲、4 个 worker 阻塞在发送。此时
// 取消父 ctx 并放行收集点：旧实现只要在消化完这 12 条之前命中过一次
// runCtx.Done()（概率 1−2⁻¹²）就会带着阻塞的 worker 进入 wg.Wait() 并永久悬挂。
func TestRunPool_CleanupDrainNoDeadlockWhenResultsFull(t *testing.T) {
	const (
		batches     = 20
		concurrency = 4
		// 1 条被收集 + 缓冲 concurrency*2 条 + concurrency 个 worker 阻塞在发送
		producedBeforeBlock = 1 + concurrency*2 + concurrency
	)
	doc := newTestDoc(batches)
	rep := newHoldingReporter()

	dispatched := make(chan struct{}, batches)
	h := &resolvedSubsetHandler{modeName: "translate", segsPerBatch: 1}
	h.processFn = func(idxs []int, _ int) batchResult {
		dispatched <- struct{}{}
		return batchResult{}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	round := Round{
		Concurrency: concurrency,
		Retry:       backend.RetryPolicy{MaxAttempts: 0},
		Shrink:      1.0,
		Handler:     h,
	}

	done := make(chan struct{})
	go func() {
		_, _ = RunRound(ctx, round, doc, nil, quietLogger(), rep)
		close(done)
	}()

	select {
	case <-rep.firstHit:
	case <-time.After(5 * time.Second):
		t.Fatal("收集点未进入 BatchComplete，构造失败")
	}
	for i := 0; i < producedBeforeBlock; i++ {
		select {
		case <-dispatched:
		case <-time.After(5 * time.Second):
			t.Fatalf("仅派发 %d 批（需要 %d），无法填满 results 缓冲，构造失败",
				i, producedBeforeBlock)
		}
	}
	// ProcessBatch 在返回前发信号，最后一批此刻才刚要发送 result：留一小段时间
	// 让它真正阻塞在 results 上，否则构造出的只是「缓冲满」而非「有阻塞发送方」。
	time.Sleep(100 * time.Millisecond)
	cancel()
	rep.release()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("runPool 悬挂：cleanup 必须先排空 results 再 wg.Wait()")
	}
}

// TestRunPool_UnconfirmedBatchNotCounted 调用方 ctx 已死时 batchHandler 返回 nil
// 不代表副作用落库（各 handler 对 context.Canceled 的处理是静默跳过该段），
// 该批必须按让行处理：不计数、不登记断点、段落回未解决。
func TestRunPool_UnconfirmedBatchNotCounted(t *testing.T) {
	doc := newTestDoc(2)
	rep := &resolvedCountingReporter{}

	h := &resolvedSubsetHandler{modeName: "adjudicate", segsPerBatch: 2}
	h.processFn = func(idxs []int, _ int) batchResult {
		cb := BatchResult{}
		return batchResult{callbackResult: &cb}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 回调内取消调用方 ctx 并返回 nil：模拟「取消抵达落库循环，段被静默跳过，
	// 但回调本身不报错」的生产形态。
	batchHandler := func(_ context.Context, _ BatchResult) error {
		cancel()
		return nil
	}

	round := Round{
		Concurrency: 1,
		Retry:       backend.RetryPolicy{MaxAttempts: 0},
		Shrink:      1.0,
		Handler:     h,
	}

	result, err := RunRound(ctx, round, doc, batchHandler, quietLogger(), rep)
	if err != nil {
		t.Fatalf("RunRound: %v（取消不经 error 上抛）", err)
	}
	dones, resolved, _ := rep.snapshot()
	if dones != 0 {
		t.Fatalf("SegmentDone=%d want 0（副作用未确认不得计数）", dones)
	}
	if len(resolved) != 0 {
		t.Fatalf("SegmentResolved=%v want 空（绝不伪造断点）", resolved)
	}
	if got := sortedCopy(result.Unresolved); !equalInts(got, []int{0, 1}) {
		t.Fatalf("Unresolved=%v want [0 1]（未确认批次的段回到未解决）", got)
	}
	if len(result.Resolved) != 0 {
		t.Fatalf("Resolved=%v want 空", result.Resolved)
	}
}
