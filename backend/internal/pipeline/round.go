package pipeline

import "github.com/MeowSalty/LinguaFlow/backend/internal/backend"

// Round 描述一轮执行的编排器配置。
// 编排器只关心并发、重试策略和 handler，不关心具体操作模式。
type Round struct {
	Concurrency int
	Retry       backend.RetryPolicy
	Context     *ContextConfig
	// Shrink 是池化缩批系数 (0,1)。>0 且 <1 时启用多池；0 表示单池。
	// 池 N 的批次约束 = floor(原始 * Shrink^N)。
	Shrink  float64
	Handler RoundHandler
}
