package pipeline

import (
	"context"
	"testing"
	"time"
)

// --- PauseGate 单元测试 ---

// TestPauseGate_InitiallyNotPaused 新建闸门未处于暂停态，Done() 未关闭。
func TestPauseGate_InitiallyNotPaused(t *testing.T) {
	g := NewPauseGate()
	if g.Paused() {
		t.Fatal("新建闸门 Paused() = true, want false")
	}
	// Done() 在首次 Pause() 前保持打开：非阻塞接收必须命中 default
	//（若命中 case 说明 channel 已关闭，事件语义错误）。
	select {
	case <-g.Done():
		t.Fatal("未 Pause 时 Done() 不应关闭")
	default:
	}
}

// TestPauseGate_PauseClosesDoneIdempotent Pause 后 Paused() 为真、Done() 恒关；
// 重复 Pause 幂等（二次关闭 channel 会 panic，pauseOnce 必须兜住）。
func TestPauseGate_PauseClosesDoneIdempotent(t *testing.T) {
	g := NewPauseGate()
	g.Pause()
	g.Pause() // 幂等：不应 panic
	if !g.Paused() {
		t.Fatal("Pause 后 Paused() = false, want true")
	}
	// 已关闭的 channel：非阻塞接收立即返回零值。
	select {
	case <-g.Done():
	default:
		t.Fatal("Pause 后 Done() 应已关闭")
	}
}

// TestPauseGate_NilGateDoneReturnsNilChannel nil 闸门的 Done() 返回 nil channel，
// select 中该 case 永久阻塞（等效于无暂停语义）——handler 退避重试的
// `case <-h.Gate.Done():` 依赖此 nil 安全性。
func TestPauseGate_NilGateDoneReturnsNilChannel(t *testing.T) {
	var g *PauseGate
	ch := g.Done()
	if ch != nil {
		t.Fatalf("nil gate Done() = %v, want nil channel", ch)
	}
	// nil channel 的接收永久阻塞：非阻塞 select 必须命中 default。
	select {
	case <-ch:
		t.Fatal("nil channel 不应有事件就绪")
	default:
	}
}

// --- Station 单元测试 ---

// TestStation_AcquireReleaseCapacity 站位容量会计：填满后下一个 Acquire 阻塞，
// Release 后放行。
func TestStation_AcquireReleaseCapacity(t *testing.T) {
	s := NewStation(2)
	ctx := context.Background()
	if !s.Acquire(ctx) || !s.Acquire(ctx) {
		t.Fatal("容量 2 的空站位应允许连续两次 Acquire")
	}

	acquired := make(chan bool, 1)
	go func() { acquired <- s.Acquire(ctx) }()

	// 容量已满：第三个 Acquire 应阻塞（不占位、不成功）。
	select {
	case ok := <-acquired:
		t.Fatalf("容量满时 Acquire 不应成功 (got %v)", ok)
	case <-time.After(50 * time.Millisecond):
	}

	s.Release()
	select {
	case ok := <-acquired:
		if !ok {
			t.Fatal("Release 后 Acquire 应成功")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Release 后 Acquire 未放行")
	}
	// 归还全部槽位，保持站位状态干净。
	s.Release()
	s.Release()
}

// TestStation_AcquireCancelledCtx 容量满且 ctx 已取消时 Acquire 返回 false，
// 且不消耗槽位（Release 一次即恢复空位）。
// 注：空站位 + 已取消 ctx 时 select 两个 case 均就绪、结果随机（与
// golang.org/x/sync/semaphore 的"获取成功优先"语义一致），故先填满容量
// 使 sem 发送阻塞、取消的 ctx 成为唯一就绪 case，保证判定确定。
func TestStation_AcquireCancelledCtx(t *testing.T) {
	s := NewStation(1)
	if !s.Acquire(context.Background()) {
		t.Fatal("空站位 Acquire 应成功")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if s.Acquire(ctx) {
		t.Fatal("容量满且 ctx 已取消时 Acquire 应返回 false")
	}
	// 失败的 Acquire 未消费槽位：Release 一次后应可再次获取。
	s.Release()
	if !s.Acquire(context.Background()) {
		t.Fatal("取消的 Acquire 不应消耗槽位")
	}
	s.Release()
}
