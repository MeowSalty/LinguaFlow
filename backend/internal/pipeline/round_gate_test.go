package pipeline

import "testing"

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
