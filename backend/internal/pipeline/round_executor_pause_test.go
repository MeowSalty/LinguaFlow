package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

// pauseProbeHandler 是暂停/站位行为专用的可配置 RoundHandler：
// BuildBatches 按 segsPerBatch 把 doc 全部段落（或给定 pending）切批，
// ProcessBatch 记录派发批次并委托 processFn 产生结果。
type pauseProbeHandler struct {
	modeName     string
	segsPerBatch int

	mu              sync.Mutex
	dispatched      [][]int // 已派发的批次（ProcessBatch 收到的 idxs 副本）
	buildCalls      int
	finalizeCalls   int
	finalUnresolved []int

	processFn func(h *pauseProbeHandler, idxs []int) batchResult
}

func (h *pauseProbeHandler) ModeName() string { return h.modeName }

func (h *pauseProbeHandler) BuildBatches(_ context.Context, doc *Document, pending []int, _ int) ([][]int, error) {
	h.mu.Lock()
	h.buildCalls++
	h.mu.Unlock()

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

func (h *pauseProbeHandler) ProcessBatch(_ context.Context, _ *Document, idxs []int, _ int, _ *slog.Logger) batchResult {
	h.mu.Lock()
	h.dispatched = append(h.dispatched, append([]int(nil), idxs...))
	h.mu.Unlock()
	if h.processFn != nil {
		return h.processFn(h, idxs)
	}
	return batchResult{}
}

func (h *pauseProbeHandler) Finalize(_ context.Context, _ *Document, unresolved []int) error {
	h.mu.Lock()
	h.finalizeCalls++
	h.finalUnresolved = append([]int(nil), unresolved...)
	h.mu.Unlock()
	return nil
}

func (h *pauseProbeHandler) dispatchCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.dispatched)
}

func (h *pauseProbeHandler) snapshotDispatched() [][]int {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([][]int, len(h.dispatched))
	for i, batch := range h.dispatched {
		out[i] = append([]int(nil), batch...)
	}
	return out
}

func (h *pauseProbeHandler) buildCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.buildCalls
}

func (h *pauseProbeHandler) finalizeSnapshot() (calls int, unresolved []int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.finalizeCalls, append([]int(nil), h.finalUnresolved...)
}

// sortedCopy 返回升序副本，便于与期望集合比较。
func sortedCopy(in []int) []int {
	out := append([]int(nil), in...)
	sort.Ints(out)
	return out
}

// equalInts 比较两个切片作为集合是否一致（长度与逐元素）。
func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestRunRound_PauseMidPoolStopsDispatch 池内派发前暂停：首个批次派发后置位
// 暂停闸门，其余批次不再派发（直接落入 Unresolved），不推进到下一池，
// Finalize 仍执行，且暂停不是错误。
//
// 有界等待兼作悬挂回归：runPool 的暂停让行分支必须发出 deferred 空结果（保持
// active 计数平衡），否则主循环既等不到结果、又不监听 Gate.Done，会永久阻塞
// （runPool 悬挂 → 资源 goroutine 悬挂 → processJob 的 wg.Wait 悬挂）。
// 超时即失败，不再 Skip。
func TestRunRound_PauseMidPoolStopsDispatch(t *testing.T) {
	gate := NewPauseGate()
	doc := newTestDoc(6)

	h := &pauseProbeHandler{modeName: "translate", segsPerBatch: 1}
	h.processFn = func(_ *pauseProbeHandler, _ []int) batchResult {
		// 首个批次派发后立即暂停（并发 1 下派发顺序确定：先段 0）。
		gate.Pause()
		return batchResult{}
	}
	rep := &countingReporter{}

	round := Round{
		Concurrency: 1,
		Retry:       backend.RetryPolicy{MaxAttempts: 1}, // maxPools=2，验证不推进到池 1
		Shrink:      1.0,
		Handler:     h,
		Gate:        gate,
	}

	type runOutcome struct {
		result RunRoundResult
		err    error
	}
	done := make(chan runOutcome, 1)
	go func() {
		result, err := RunRound(context.Background(), round, doc, nil, quietLogger(), rep)
		done <- runOutcome{result, err}
	}()

	select {
	case out := <-done:
		if out.err != nil {
			t.Fatalf("RunRound: %v（暂停不是错误）", out.err)
		}
		result := out.result

		// 仅首批（段 0）被派发，其余批次直接落入 Unresolved。
		dispatched := h.snapshotDispatched()
		if len(dispatched) != 1 || len(dispatched[0]) != 1 || dispatched[0][0] != 0 {
			t.Fatalf("dispatched=%v want [[0]]（暂停后不再派发）", dispatched)
		}
		wantUnresolved := []int{1, 2, 3, 4, 5}
		if got := sortedCopy(result.Unresolved); !equalInts(got, wantUnresolved) {
			t.Fatalf("Unresolved=%v want %v", got, wantUnresolved)
		}
		// 已派发的段不在 Unresolved 中。
		for _, u := range result.Unresolved {
			if u == 0 {
				t.Fatalf("已派发段 0 不应出现在 Unresolved=%v", result.Unresolved)
			}
		}
		// Finalize 仍执行（收到的正是未解决集合），且未进入池 1（BuildBatches 仅一次）。
		if calls, unresolved := h.finalizeSnapshot(); calls != 1 || !equalInts(sortedCopy(unresolved), wantUnresolved) {
			t.Fatalf("Finalize calls=%d unresolved=%v, want 1 次 且 %v", calls, unresolved, wantUnresolved)
		}
		if h.buildCount() != 1 {
			t.Fatalf("BuildBatches calls=%d want 1（池推进前的暂停检查应跳出循环）", h.buildCount())
		}
		// StageStart 仅在池 0 触发一次。
		if n := atomic.LoadInt32(&rep.stageStartCalls); n != 1 {
			t.Fatalf("StageStart calls=%d want 1", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunRound 未在 5s 内返回：暂停让行路径必须发出 deferred 空结果，" +
			"否则池主循环的 active 计数永不归零（悬挂回归）")
	}
}

// TestRunRound_PausedBeforeStart 启动前已暂停：不进入任何池（零建批、零派发），
// 无 StageStart/Finalize，返回空结果且无错误——段落未被触碰，天然保持未解决
// 状态（未解决语义由任务层断点集合与 resume 流程覆盖，不经过本返回值）。
func TestRunRound_PausedBeforeStart(t *testing.T) {
	gate := NewPauseGate()
	gate.Pause()
	doc := newTestDoc(4)

	h := &pauseProbeHandler{modeName: "translate", segsPerBatch: 2}
	round := Round{
		Concurrency: 2,
		Retry:       backend.RetryPolicy{MaxAttempts: 1},
		Shrink:      1.0,
		Handler:     h,
		Gate:        gate,
	}
	rep := &countingReporter{}

	result, err := RunRound(context.Background(), round, doc, nil, quietLogger(), rep)
	if err != nil {
		t.Fatalf("RunRound: %v（暂停不是错误）", err)
	}
	if len(result.Unresolved) != 0 {
		t.Fatalf("Unresolved=%v want 空（未进入任何池，结果不携带段落）", result.Unresolved)
	}
	if n := h.dispatchCount(); n != 0 {
		t.Fatalf("ProcessBatch 派发=%d want 0", n)
	}
	if n := h.buildCount(); n != 0 {
		t.Fatalf("BuildBatches calls=%d want 0（循环顶暂停检查先于建批）", n)
	}
	if calls, _ := h.finalizeSnapshot(); calls != 0 {
		t.Fatalf("Finalize calls=%d want 0（stageStarted=false 提前返回）", calls)
	}
	if n := atomic.LoadInt32(&rep.stageStartCalls); n != 0 {
		t.Fatalf("StageStart calls=%d want 0", n)
	}
}

// inflightTracker 以互斥锁记录并发进入 ProcessBatch 的峰值。
type inflightTracker struct {
	mu       sync.Mutex
	inflight int
	max      int
}

func (t *inflightTracker) enter() {
	t.mu.Lock()
	t.inflight++
	if t.inflight > t.max {
		t.max = t.inflight
	}
	t.mu.Unlock()
}

func (t *inflightTracker) exit() {
	t.mu.Lock()
	t.inflight--
	t.mu.Unlock()
}

func (t *inflightTracker) peak() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.max
}

// TestRunRound_SharedStationSerializesAcrossResources 两个 RunRound 共享容量 1
// 的站位：跨"资源"串行——任一时刻至多一个批次在 ProcessBatch 中（站位信号量
// 把同轮所有在途资源的批次共享到同一并发预算上）。
func TestRunRound_SharedStationSerializesAcrossResources(t *testing.T) {
	station := NewStation(1)
	var tracker inflightTracker

	h := &pauseProbeHandler{modeName: "extract", segsPerBatch: 1}
	h.processFn = func(_ *pauseProbeHandler, _ []int) batchResult {
		tracker.enter()
		time.Sleep(30 * time.Millisecond) // 制造重叠窗口：若未串行化必然观测到并发 > 1
		tracker.exit()
		return batchResult{}
	}

	runOne := func() error {
		doc := newTestDoc(3)
		round := Round{
			Concurrency: 2, // 池内并发 > 站位容量：并发余量由站位钳制
			Retry:       backend.RetryPolicy{MaxAttempts: 0},
			Shrink:      1.0,
			Handler:     h,
			Slots:       station,
		}
		_, err := RunRound(context.Background(), round, doc, nil, quietLogger(), nil)
		return err
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- runOne()
		}()
	}
	wg.Wait()
	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("RunRound: %v", err)
		}
	}

	if got := h.dispatchCount(); got != 6 {
		t.Fatalf("dispatched=%d want 6（两个资源各 3 批全部完成）", got)
	}
	if peak := tracker.peak(); peak != 1 {
		t.Fatalf("并发峰值=%d want 1（容量 1 的站位跨资源串行批次）", peak)
	}
}

// rendezvous 是双方栅栏：两方都调用 wait 后同时放行；超时兜底避免悬挂。
// 用于证明"并发确实发生了"——若并发被压制，先到者超时返回 false。
type rendezvous struct {
	arrive chan struct{}
	open   chan struct{}
}

func newRendezvous() *rendezvous {
	return &rendezvous{arrive: make(chan struct{}, 2), open: make(chan struct{})}
}

// wait 登记到达并等待放行；返回 false 表示超时（对方未到）。
func (r *rendezvous) wait(timeout time.Duration) bool {
	select {
	case r.arrive <- struct{}{}:
	default:
	}
	select {
	case <-r.open:
		return true
	case <-time.After(timeout):
		return false
	}
}

// run 等待两方到齐后放行；超时也放行（让测试以断言失败而非悬挂收场）。
func (r *rendezvous) run(timeout time.Duration) {
	deadline := time.After(timeout)
	for i := 0; i < 2; i++ {
		select {
		case <-r.arrive:
		case <-deadline:
			close(r.open)
			return
		}
	}
	close(r.open)
}

// TestRunRound_NilSlotsKeepsRoundConcurrency 对照组：nil Slots（单资源路径）
// 下并发等于 round.Concurrency——两个批次必须能同时在 ProcessBatch 中。
// 用双方栅栏强制重叠，避免 sleep 时序假设。
func TestRunRound_NilSlotsKeepsRoundConcurrency(t *testing.T) {
	var tracker inflightTracker
	rv := newRendezvous()
	go rv.run(3 * time.Second)

	var overlapOK int32
	h := &pauseProbeHandler{modeName: "extract", segsPerBatch: 1}
	h.processFn = func(_ *pauseProbeHandler, _ []int) batchResult {
		tracker.enter()
		if rv.wait(3 * time.Second) {
			atomic.AddInt32(&overlapOK, 1)
		}
		tracker.exit()
		return batchResult{}
	}

	doc := newTestDoc(2)
	round := Round{
		Concurrency: 2, // 无 Slots：并发由 round.Concurrency 决定
		Retry:       backend.RetryPolicy{MaxAttempts: 0},
		Shrink:      1.0,
		Handler:     h,
	}
	if _, err := RunRound(context.Background(), round, doc, nil, quietLogger(), nil); err != nil {
		t.Fatalf("RunRound: %v", err)
	}
	if got := h.dispatchCount(); got != 2 {
		t.Fatalf("dispatched=%d want 2", got)
	}
	if n := atomic.LoadInt32(&overlapOK); n != 2 {
		t.Fatalf("两批次未同时在 ProcessBatch 中（overlap=%d/2，峰值=%d）", n, tracker.peak())
	}
	if peak := tracker.peak(); peak != 2 {
		t.Fatalf("并发峰值=%d want 2（nil Slots 时并发 = round.Concurrency）", peak)
	}
}

// TestRunRound_HandlerBackoffAbortsOnPause 退避重试等待中的暂停中止：
// 后端返回可重试错误后 handler 进入退避（最小 5s，见 minRateLimitBackoff），
// Pause 置位应经 Gate.Done() 立即中止等待——批次按 unresolved 收场而非等满
// 退避时长，且暂停后不再发起重试调用。
func TestRunRound_HandlerBackoffAbortsOnPause(t *testing.T) {
	gate := NewPauseGate()
	doc := newTestDoc(1)
	fb := &fakeBackend{
		name: "fake",
		errs: []error{errors.New("network down")}, // 无 HTTP 状态码 → backend.IsRetryable = true
	}
	rep := &countingReporter{}
	h := newTestTranslateHandler(fb, 1, 1, func(h *TranslateHandler) {
		h.Reporter = rep
		h.Gate = gate
		h.Retry = backend.RetryPolicy{MaxAttempts: 0} // 单池
	})
	h.Renderer = newTestRenderer(t)

	// 后端被调用即暂停：此刻 handler 正卡在退避 timer 的 select 上。
	go func() {
		deadline := time.Now().Add(3 * time.Second)
		for fb.idx.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(2 * time.Millisecond)
		}
		gate.Pause()
	}()

	start := time.Now()
	round := Round{Concurrency: 1, Retry: h.Retry, Shrink: 1.0, Handler: h, Gate: gate}
	result, err := RunRound(context.Background(), round, doc, nil, quietLogger(), rep)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("RunRound: %v", err)
	}
	// 未中止时退避最小 5s；Gate.Done() 中止应显著快于它（4s 留足裕量）。
	if elapsed >= 4*time.Second {
		t.Fatalf("RunRound 耗时 %v（已达最小退避 5s 量级），暂停未中止退避等待", elapsed)
	}
	if got := sortedCopy(result.Unresolved); !equalInts(got, []int{0}) {
		t.Fatalf("Unresolved=%v want [0]（段保持未解决，由断点集合覆盖）", got)
	}
	if calls := fb.idx.Load(); calls != 1 {
		t.Fatalf("backend calls=%d want 1（暂停后不再发起重试）", calls)
	}
}

// TestRunRound_PauseWhileWaitingForStation 暂停到达时，阻塞在共享站位
// Acquire 上的排队 worker 在获得槽位后必须复查暂停并跳过执行——否则
// 排队批次会在槽位释放后照常执行一整批 LLM 调用，排空时间被放大为
// 排队深度、暂停后仍持续产生调用与计费。
func TestRunRound_PauseWhileWaitingForStation(t *testing.T) {
	gate := NewPauseGate()
	station := NewStation(1) // 容量 1：第二个 worker 必然阻塞在 Acquire
	doc := newTestDoc(2)

	h := &pauseProbeHandler{modeName: "translate", segsPerBatch: 1}
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	h.processFn = func(_ *pauseProbeHandler, _ []int) batchResult {
		// 首批持槽执行：通知主流程后阻塞，直到主流程确认第二个 worker
		// 已登记 inflight（必然阻塞在 Acquire）并触发暂停后才放行。
		startOnce.Do(func() { close(started) })
		<-release
		return batchResult{}
	}
	rep := &countingReporter{}

	round := Round{
		Concurrency: 2,
		Retry:       backend.RetryPolicy{MaxAttempts: 1},
		Shrink:      1.0,
		Handler:     h,
		Gate:        gate,
		Slots:       station,
	}

	type runOutcome struct {
		result RunRoundResult
		err    error
	}
	done := make(chan runOutcome, 1)
	go func() {
		result, err := RunRound(context.Background(), round, doc, nil, quietLogger(), rep)
		done <- runOutcome{result, err}
	}()

	<-started
	// 等待第二个 worker 通过派发前暂停检查并登记 inflight（即已阻塞在
	// 站位 Acquire 上）——确保测试确定性命中「Acquire 后复查」分支，
	// 而非派发前检查分支。
	deadline := time.Now().Add(2 * time.Second)
	for {
		gate.mu.Lock()
		n := gate.inflight
		gate.mu.Unlock()
		if n >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("第二个 worker 未在 2s 内阻塞到站位 Acquire（inflight=%d）", n)
		}
		time.Sleep(2 * time.Millisecond)
	}
	// 暂停后释放首批：排队 worker 获得槽位，应复查暂停并跳过执行。
	gate.Pause()
	close(release)

	select {
	case out := <-done:
		if out.err != nil {
			t.Fatalf("RunRound: %v（暂停不是错误）", out.err)
		}
		// 两个 worker 抢 jobs channel：哪个批次持槽执行、哪个阻塞在
		// Acquire 取决于调度——本测试曾假设段 0 必然持槽（Unresolved 恒
		// [1]），该假设在 goroutine 调度下间歇失效。断言调度无关的
		// 互补性：恰一批派发，未派发段进入 unresolved。
		if n := h.dispatchCount(); n != 1 {
			t.Fatalf("ProcessBatch 派发=%d want 1（阻塞在站位的排队批次不应在暂停后执行）", n)
		}
		dispatched := h.snapshotDispatched()
		if len(dispatched) != 1 || len(dispatched[0]) != 1 {
			t.Fatalf("派发批次=%v want 恰一个单段批次", dispatched)
		}
		got := sortedCopy(out.result.Unresolved)
		want := dispatched[0][0] ^ 1 // {0,1} 中与派发段互补的另一个
		if len(got) != 1 || got[0] != want {
			t.Fatalf("Unresolved=%v want [%d]（与派发段 %d 互补）", got, want, dispatched[0][0])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunRound 未在 5s 内返回（暂停排空悬挂）")
	}
}
