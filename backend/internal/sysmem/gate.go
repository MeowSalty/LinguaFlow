package sysmem

import (
	"log/slog"
	"sync"
)

const (
	// highWatermarkPercent 是触发熔断的高水位，占 limitBytes 的百分比：
	// RSS ≥ 高水位时拒绝新资源准入（只出不进）。
	highWatermarkPercent = 85

	// lowWatermarkPercent 是恢复准入的低水位，占 limitBytes 的百分比：
	// RSS ≤ 低水位时自动恢复正常。
	lowWatermarkPercent = 70

	// maxConsecutiveRSSErrors 是连续读取 RSS 失败后停用闸门的阈值：
	// 达到该次数后视为闸门失效（始终放行），直到某次读取成功后自动恢复。
	maxConsecutiveRSSErrors = 10
)

// Gate 是进程级 RSS 双水位准入闸门：高于高水位时拒绝新资源准入（只出不进），
// 回落到低水位后自动恢复。它不是任务状态——任务保持 running，只是资源排队。
//
// 水位与状态机（hysteresis，迟滞）：
//   - 高水位 = limitBytes 的 85%，低水位 = limitBytes 的 70%；
//   - RSS ≥ 高水位 → 进入熔断态；RSS ≤ 低水位 → 恢复正常态（边界值含等号）；
//   - 介于两水位之间时保持原状态，避免在水位附近反复抖动。
//
// RSS 读取失败的处理（保守但不吵闹）：
//   - 单次失败按"无法读取 → 允许准入"处理，Allow 返回 true，但不改变水位
//     状态机的熔断标记（下次成功读取时按实际 RSS 重新评估）；
//   - 每段连续失败仅在第一次记录一条 Warn；连续失败达到
//     maxConsecutiveRSSErrors（10）次后闸门停用（始终放行）并记录一条
//     Error，直到某次读取成功后自动重新启用（记录一条 Info）。
//
// Gate 是拉模式：不做后台定时刷新，仅在每次 Allow() 调用时读取一次 RSS 并
// 推进状态机，准入频率由调用方控制，本包不产生额外轮询开销。
//
// Gate 并发安全；Tripped() 为只读快照，不触发 RSS 读取。
type Gate struct {
	limitBytes uint64
	rssFn      func() (uint64, error)
	log        *slog.Logger

	mu        sync.Mutex
	tripped   bool
	errStreak int
	errOff    bool
}

// NewGate 创建准入闸门。
//
//   - limitBytes：进程 RSS 上限，0 表示禁用闸门（Allow 恒为 true）；
//   - rssFn：RSS 读取函数，nil 时使用 ReadRSS；
//   - log：日志器，nil 时使用 slog.Default()。
func NewGate(limitBytes uint64, rssFn func() (uint64, error), log *slog.Logger) *Gate {
	if rssFn == nil {
		rssFn = ReadRSS
	}
	if log == nil {
		log = slog.Default()
	}
	return &Gate{
		limitBytes: limitBytes,
		rssFn:      rssFn,
		log:        log,
	}
}

// Allow 检查当前是否允许新资源准入：未熔断时返回 true。
// 每次调用读取一次 RSS 并推进水位状态机（拉模式），调用方控制调用频率。
func (g *Gate) Allow() bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	// 未启用：恒放行，不读取 RSS。
	if g.limitBytes == 0 {
		return true
	}

	rss, err := g.rssFn()
	if err != nil {
		g.errStreak++
		if g.errStreak == 1 {
			// 每段连续失败只记录一条 Warn，避免日志刷屏。
			g.log.Warn("sysmem: 读取 RSS 失败，本次按允许准入处理",
				"error", err, "consecutive_errors", g.errStreak)
		}
		if g.errStreak >= maxConsecutiveRSSErrors && !g.errOff {
			g.errOff = true
			g.log.Error("sysmem: 连续读取 RSS 失败，闸门停用（始终放行），恢复读取后自动重新启用",
				"consecutive_errors", g.errStreak, "limit_bytes", g.limitBytes)
		}
		return true
	}
	if g.errStreak > 0 {
		g.errStreak = 0
		if g.errOff {
			g.errOff = false
			g.log.Info("sysmem: RSS 读取恢复，闸门重新启用",
				"rss_bytes", rss, "limit_bytes", g.limitBytes)
		}
	}

	high := g.limitBytes * highWatermarkPercent / 100
	low := g.limitBytes * lowWatermarkPercent / 100

	switch {
	case rss >= high:
		if !g.tripped {
			g.tripped = true
			g.log.Warn("sysmem: RSS 达到高水位，熔断：暂停新资源准入（只出不进）",
				"rss_bytes", rss, "limit_bytes", g.limitBytes, "watermark", high)
		}
	case rss <= low:
		if g.tripped {
			g.tripped = false
			g.log.Info("sysmem: RSS 回落至低水位，恢复新资源准入",
				"rss_bytes", rss, "limit_bytes", g.limitBytes, "watermark", low)
		}
	default:
		// 介于两水位之间：保持原状态（迟滞），不做任何变更。
	}
	return !g.tripped
}

// Tripped 返回当前是否处于熔断态（只读快照，不触发 RSS 读取）。
// 注意：RSS 读取失败期间 Allow 可能放行而熔断标记仍保留，两者可能短暂不一致。
func (g *Gate) Tripped() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.tripped
}
