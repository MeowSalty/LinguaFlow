package sysmem

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// capturedRecord 记录单条被捕获的 slog 记录。
type capturedRecord struct {
	level slog.Level
	msg   string
	attrs map[string]any
}

// captureHandler 实现 slog.Handler，将记录缓存到内存便于断言。
// Handle 内部加锁，可在并发测试中使用。
type captureHandler struct {
	mu      sync.Mutex
	level   slog.Level
	records []capturedRecord
}

func newCaptureHandler() *captureHandler { return &captureHandler{level: slog.LevelDebug} }

func (h *captureHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	attrs := make(map[string]any, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, capturedRecord{level: r.Level, msg: r.Message, attrs: attrs})
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

// snapshot 返回已捕获记录的快照。
func (h *captureHandler) snapshot() []capturedRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.records)
}

// count 统计级别为 level 且消息包含 substr 的记录数。
func (h *captureHandler) count(level slog.Level, substr string) int {
	n := 0
	for _, r := range h.snapshot() {
		if r.level == level && strings.Contains(r.msg, substr) {
			n++
		}
	}
	return n
}

func TestGateWatermarkTransitions(t *testing.T) {
	const limit = uint64(1000) // 高水位 850，低水位 700
	tests := []struct {
		name            string
		rssSeq          []uint64
		wantAllow       []bool
		wantTripped     []bool
		wantTransitions int // 期望的熔断/恢复日志条数（即状态变化次数）
	}{
		{
			name:            "低水位以下-正常放行",
			rssSeq:          []uint64{100},
			wantAllow:       []bool{true},
			wantTripped:     []bool{false},
			wantTransitions: 0,
		},
		{
			name:            "两水位之间-迟滞保持正常",
			rssSeq:          []uint64{100, 750, 849},
			wantAllow:       []bool{true, true, true},
			wantTripped:     []bool{false, false, false},
			wantTransitions: 0,
		},
		{
			name:            "达到高水位-熔断",
			rssSeq:          []uint64{100, 850, 900},
			wantAllow:       []bool{true, false, false},
			wantTripped:     []bool{false, true, true},
			wantTransitions: 1,
		},
		{
			name:            "熔断后介于两水位之间-迟滞保持熔断",
			rssSeq:          []uint64{900, 750, 701},
			wantAllow:       []bool{false, false, false},
			wantTripped:     []bool{true, true, true},
			wantTransitions: 1,
		},
		{
			name:            "回落至低水位-恢复准入",
			rssSeq:          []uint64{900, 700, 100},
			wantAllow:       []bool{false, true, true},
			wantTripped:     []bool{true, false, false},
			wantTransitions: 2,
		},
		{
			name:            "边界值-恰好等于高/低水位",
			rssSeq:          []uint64{850, 700},
			wantAllow:       []bool{false, true},
			wantTripped:     []bool{true, false},
			wantTransitions: 2,
		},
		{
			name:            "反复穿越水位-每次状态变化记录一次",
			rssSeq:          []uint64{900, 100, 900, 100},
			wantAllow:       []bool{false, true, false, true},
			wantTripped:     []bool{true, false, true, false},
			wantTransitions: 4,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newCaptureHandler()
			call := 0
			g := NewGate(limit, func() (uint64, error) {
				v := tt.rssSeq[call]
				call++
				return v, nil
			}, slog.New(h))

			for idx, rss := range tt.rssSeq {
				if got := g.Allow(); got != tt.wantAllow[idx] {
					t.Errorf("第 %d 次 Allow() = %v, 期望 %v (rss=%d)", idx+1, got, tt.wantAllow[idx], rss)
				}
				if got := g.Tripped(); got != tt.wantTripped[idx] {
					t.Errorf("第 %d 次 Tripped() = %v, 期望 %v", idx+1, got, tt.wantTripped[idx])
				}
			}
			if got := len(h.snapshot()); got != tt.wantTransitions {
				t.Errorf("状态转换日志条数 = %d, 期望 %d（记录: %+v）", got, tt.wantTransitions, h.snapshot())
			}
		})
	}
}

func TestGateTransitionLogFields(t *testing.T) {
	t.Run("熔断日志字段", func(t *testing.T) {
		h := newCaptureHandler()
		g := NewGate(1000, func() (uint64, error) { return 900, nil }, slog.New(h))
		if g.Allow() {
			t.Fatal("rss=900 ≥ 高水位 850，应熔断")
		}
		recs := h.snapshot()
		if len(recs) != 1 {
			t.Fatalf("期望 1 条日志，实际 %d 条", len(recs))
		}
		r := recs[0]
		if r.level != slog.LevelWarn {
			t.Errorf("熔断日志级别 = %v, 期望 Warn", r.level)
		}
		for _, key := range []string{"rss_bytes", "limit_bytes", "watermark"} {
			if _, ok := r.attrs[key]; !ok {
				t.Errorf("熔断日志缺少字段 %q: %+v", key, r.attrs)
			}
		}
		if r.attrs["rss_bytes"] != uint64(900) {
			t.Errorf("rss_bytes = %v, 期望 900", r.attrs["rss_bytes"])
		}
		if r.attrs["limit_bytes"] != uint64(1000) {
			t.Errorf("limit_bytes = %v, 期望 1000", r.attrs["limit_bytes"])
		}
		if r.attrs["watermark"] != uint64(850) {
			t.Errorf("watermark = %v, 期望 850", r.attrs["watermark"])
		}
	})

	t.Run("恢复日志字段", func(t *testing.T) {
		h := newCaptureHandler()
		seq := []uint64{900, 700}
		call := 0
		g := NewGate(1000, func() (uint64, error) {
			v := seq[call]
			call++
			return v, nil
		}, slog.New(h))
		g.Allow() // 熔断
		g.Allow() // 恢复

		recs := h.snapshot()
		if len(recs) != 2 {
			t.Fatalf("期望 2 条日志（熔断+恢复），实际 %d 条: %+v", len(recs), recs)
		}
		r := recs[1]
		if r.level != slog.LevelInfo {
			t.Errorf("恢复日志级别 = %v, 期望 Info", r.level)
		}
		if r.attrs["rss_bytes"] != uint64(700) {
			t.Errorf("rss_bytes = %v, 期望 700", r.attrs["rss_bytes"])
		}
		if r.attrs["watermark"] != uint64(700) {
			t.Errorf("watermark = %v, 期望 700", r.attrs["watermark"])
		}
	})
}

func TestGateDisabled(t *testing.T) {
	h := newCaptureHandler()
	calls := 0
	g := NewGate(0, func() (uint64, error) {
		calls++
		return 0, errors.New("boom")
	}, slog.New(h))
	for i := 0; i < 5; i++ {
		if !g.Allow() {
			t.Fatalf("limit=0 时应始终放行（第 %d 次）", i+1)
		}
		if g.Tripped() {
			t.Fatalf("limit=0 时不应熔断（第 %d 次）", i+1)
		}
	}
	if calls != 0 {
		t.Errorf("禁用模式下不应调用 rssFn，实际调用 %d 次", calls)
	}
	if recs := h.snapshot(); len(recs) != 0 {
		t.Errorf("禁用模式不应产生日志，实际: %+v", recs)
	}
}

func TestGateErrorStreak(t *testing.T) {
	h := newCaptureHandler()
	fail := true
	g := NewGate(1000, func() (uint64, error) {
		if fail {
			return 0, errors.New("read failed")
		}
		return 100, nil
	}, slog.New(h))

	// 前 N-1 次失败：放行，仅第一条 Warn。
	for i := 0; i < maxConsecutiveRSSErrors-1; i++ {
		if !g.Allow() {
			t.Fatalf("读取失败期间应放行（第 %d 次失败）", i+1)
		}
		if g.Tripped() {
			t.Fatalf("读取失败期间不应熔断（第 %d 次失败）", i+1)
		}
	}
	if got := h.count(slog.LevelWarn, "读取 RSS 失败"); got != 1 {
		t.Errorf("错误连击期间 Warn 日志数 = %d, 期望 1（每次连击段只记一条）", got)
	}
	if got := h.count(slog.LevelError, ""); got != 0 {
		t.Errorf("未达阈值前不应有 Error 日志，实际 %d 条", got)
	}

	// 第 N 次失败：闸门停用，记录一条 Error，仍然放行。
	if !g.Allow() {
		t.Fatal("闸门因连续错误停用后应放行")
	}
	if got := h.count(slog.LevelError, "闸门停用"); got != 1 {
		t.Errorf("达到阈值时 Error 日志数 = %d, 期望 1", got)
	}

	// 继续失败：不再产生新日志（保持安静）。
	for i := 0; i < 5; i++ {
		if !g.Allow() {
			t.Fatal("闸门停用期间应放行")
		}
	}
	if got := len(h.snapshot()); got != 2 {
		t.Errorf("停用后继续失败不应产生新日志，日志总数 = %d, 期望 2: %+v", got, h.snapshot())
	}

	// 恢复读取：自动重新启用并评估水位（100 < 低水位 700 → 放行）。
	fail = false
	if !g.Allow() {
		t.Fatal("RSS 读取恢复且低于低水位，应放行")
	}
	if got := h.count(slog.LevelInfo, "重新启用"); got != 1 {
		t.Errorf("恢复读取应记录一条 Info，实际 %d 条", got)
	}

	// 再次失败：新一轮连击（计数已重置），重新出现一条 Warn。
	fail = true
	if !g.Allow() {
		t.Fatal("读取失败期间应放行")
	}
	if got := h.count(slog.LevelWarn, "读取 RSS 失败"); got != 2 {
		t.Errorf("恢复后再次失败应开启新的连击段（Warn 总数 = %d, 期望 2）", got)
	}
}

// TestGateErrorThenRecoveryTrip 验证错误恢复后的首次成功读取会按实际 RSS
// 重新评估水位（即使恢复读取值高于高水位，也立即熔断）。
func TestGateErrorThenRecoveryTrip(t *testing.T) {
	h := newCaptureHandler()
	fail := true
	g := NewGate(1000, func() (uint64, error) {
		if fail {
			return 0, errors.New("read failed")
		}
		return 950, nil
	}, slog.New(h))

	// 制造一小段失败（未达停用阈值）。
	for i := 0; i < 3; i++ {
		g.Allow()
	}
	// 恢复读取且 RSS 高于高水位：应直接熔断。
	fail = false
	if g.Allow() {
		t.Fatal("恢复读取后 RSS 高于高水位，应熔断")
	}
	if !g.Tripped() {
		t.Fatal("Tripped() 应为 true")
	}
	if got := h.count(slog.LevelWarn, "高水位"); got != 1 {
		t.Errorf("恢复后熔断应记录一条 Warn，实际 %d 条", got)
	}
}

// TestGateErrorDuringTripped 验证熔断期间读取失败：Allow 按约定放行
// （无法读取 → 允许），但熔断标记保留，二者可能短暂不一致。
func TestGateErrorDuringTripped(t *testing.T) {
	fail := false
	g := NewGate(1000, func() (uint64, error) {
		if fail {
			return 0, errors.New("read failed")
		}
		return 900, nil
	}, slog.New(newCaptureHandler()))

	g.Allow() // 熔断
	fail = true
	if !g.Allow() {
		t.Fatal("读取失败期间应放行（无法读取 → 允许）")
	}
	if !g.Tripped() {
		t.Fatal("读取失败不应改变熔断标记")
	}
}

func TestGateDefaultsToReadRSS(t *testing.T) {
	// rssFn 为 nil 时回退到 ReadRSS；上限 1TB，实际进程 RSS 远低于高水位，
	// 无论读数如何（含读取失败按允许处理）都应放行。
	g := NewGate(1<<40, nil, nil)
	if !g.Allow() {
		t.Fatal("RSS 远低于水位（或读取失败），应放行")
	}
	if g.Tripped() {
		t.Fatal("不应熔断")
	}
}

func TestGateNilLogger(t *testing.T) {
	// log 为 nil 时应回退 slog.Default() 且不 panic。
	g := NewGate(1000, func() (uint64, error) { return 900, nil }, nil)
	if g.Allow() {
		t.Fatal("rss=900 ≥ 高水位 850，应熔断")
	}
}

// TestGateConcurrent 并发冒烟测试：配合 -race 检测 Gate 内部状态的
// 数据竞争（多次调用 Allow/Tripped，同时并发修改 RSS 读数）。
func TestGateConcurrent(t *testing.T) {
	h := newCaptureHandler()
	var current atomic.Uint64
	current.Store(500)
	g := NewGate(1000, func() (uint64, error) {
		return current.Load(), nil
	}, slog.New(h))

	const workers = 16
	const iters = 500
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				if j%7 == 0 {
					// 在低水位以下与高水位以上之间摆动，驱动状态机双向转换。
					current.Store(uint64((seed*iters + j*40) % 1000))
				}
				_ = g.Allow()
				_ = g.Tripped()
			}
		}(i)
	}
	wg.Wait()
}
