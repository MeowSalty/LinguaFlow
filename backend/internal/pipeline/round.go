package pipeline

import "github.com/MeowSalty/LinguaFlow/backend/internal/backend"

// Round 描述一轮执行的编排器配置。
// 编排器只关心并发、重试策略和 handler，不关心具体操作模式。
type Round struct {
	Concurrency int
	Retry       backend.RetryPolicy
	Context     *ContextConfig
	// Shrink 是池缩比系数 (0,1]。
	// 池数恒由 Retry.MaxAttempts+1 决定（与 Shrink 解耦）。
	// Shrink 控制每池批次约束的缩比：1.0 = 多池同尺寸重切；(0,1) = 每池缩小。
	// 池 N 的批次约束 = floor(原始 × Shrink^N)。0 在快照加载时规范化为 1.0。
	Shrink  float64
	Handler RoundHandler
}
