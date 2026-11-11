package pipeline

import (
	"context"
	"sync"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

// Round 描述一轮执行的编排器配置。
// 编排器只关心并发、重试策略和 handler，不关心具体操作模式。
type Round struct {
	Runtime     *ExecutionRuntime
	Store       RoundStore
	Concurrency int
	Retry       backend.RetryPolicy
	Context     *ContextConfig
	// Shrink 是池缩比系数 (0,1]。
	// 池数恒由 Retry.MaxAttempts+1 决定（与 Shrink 解耦）。
	// Shrink 控制每池批次约束的缩比：1.0 = 多池同尺寸重切；(0,1) = 每池缩小。
	// 池 N 的批次约束 = floor(原始 × Shrink^N)。0 在快照加载时规范化为 1.0。
	Shrink  float64
	Handler RoundHandler

	// Slots 是站位信号量（CPU 工位模型）：容量 = 本轮次快照的 concurrency，
	// 同一轮次所有在途资源的批次共享该轮并发预算。nil 时退化为
	// 单资源模式（runPool 仅按 round.Concurrency 自建 goroutine 池，
	// 不做跨资源槽位仲裁）——preview/quick-translate/cli 等单资源调用方
	// 保持原行为不受影响。
	// 注意：Slots 由 PipelineRunner 注入，engine_factory 构造 Round 时不填。
	Slots *Station
	// Gate 是任务级暂停闸门（与 ctx 取消分离）。非 nil 时：
	//   - 批次派发前检查 Paused()，暂停中不派发新批次；
	//   - 在途批次完成后 Gate.ReleaseInflight() 注销；
	//   - 各 handler 的退避重试 timer 处中止等待（直接返回 unresolved，
	//     由断点集合覆盖，避免暂停时被 5s 起步的退避拖长排空）。
	// nil 时全部退化为现状。
	Gate *PauseGate
}

// Station 是站位信号量：容量即该轮次的并发预算。
// 由 worker 层注入（跨资源共享同一实例）；pipeline 层只 acquire/release。
type Station struct {
	mu                 sync.Mutex
	capacity, inflight int
	changed            chan struct{}
}

func NewStation(capacity int) *Station {
	if capacity < 1 {
		capacity = 1
	}
	return &Station{capacity: capacity, changed: make(chan struct{})}
}
func (s *Station) Acquire(ctx context.Context) bool {
	for {
		if ctx.Err() != nil {
			return false
		}
		s.mu.Lock()
		if s.inflight < s.capacity {
			s.inflight++
			s.mu.Unlock()
			return true
		}
		wake := s.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return false
		case <-wake:
		}
	}
}
func (s *Station) TryAcquire() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inflight >= s.capacity {
		return false
	}
	s.inflight++
	return true
}
func (s *Station) Release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inflight < 1 {
		panic("station released without an owner")
	}
	s.inflight--
	close(s.changed)
	s.changed = make(chan struct{})
}

// PauseGate 是任务级暂停闸门（与 ctx 取消分离的优雅排空信号）。
// 由 worker 层注入；pipeline 层只读检查与在途计数维护。
// 排空由 worker 侧 wg.Wait 等待资源 goroutine 退出实现，闸门自身不提供
// Drain 等待原语。
type PauseGate struct {
	mu       sync.Mutex
	paused   bool
	inflight int
	// done 在首次 Pause() 时关闭并永久保持关闭：
	//   - 准入等待中的资源 goroutine 据此退出（未入线资源不受排空影响）；
	//   - handler 退避 timer 的 select 据此中止（暂停不被 5s 起步退避拖长）。
	done      chan struct{}
	pauseOnce sync.Once
}

// NewPauseGate 创建闸门。
func NewPauseGate() *PauseGate {
	return &PauseGate{
		done: make(chan struct{}),
	}
}

// Paused 返回当前是否处于暂停请求态。
func (g *PauseGate) Paused() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.paused
}

// Done 返回暂停信号 channel（首次 Pause() 时关闭，之后恒关）。
// nil gate 返回 nil channel——select 中该 case 永久阻塞，等效于无暂停语义。
func (g *PauseGate) Done() <-chan struct{} {
	if g == nil {
		return nil
	}
	return g.done
}

// Pause 置位暂停请求。幂等；不等待排空。
func (g *PauseGate) Pause() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.paused = true
	g.pauseOnce.Do(func() { close(g.done) })
}

// TryStartDispatch atomically arbitrates pause against a nonblocking joint
// capacity/RPM decision. A request admitted first may finish after Pause; a pause
// that wins prevents the callback from consuming any capacity or request token.
func (g *PauseGate) TryStartDispatch(admit func() bool) bool {
	if g == nil {
		return admit()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.paused || !admit() {
		return false
	}
	g.inflight++
	return true
}

// Inflight counts admitted requests only. Safe pause still requires the caller
// to join response parsing, candidate saves, and transaction callbacks.
func (g *PauseGate) Inflight() int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.inflight
}

// AcquireInflight 登记一个在途批次（派发前调用）。
func (g *PauseGate) AcquireInflight() {
	g.mu.Lock()
	g.inflight++
	g.mu.Unlock()
}

// ReleaseInflight 注销一个在途批次，计数钳位非负。
func (g *PauseGate) ReleaseInflight() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.inflight--
	if g.inflight < 0 {
		g.inflight = 0
	}
	g.mu.Unlock()
}
