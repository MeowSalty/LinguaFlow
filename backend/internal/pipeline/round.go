package pipeline

import "github.com/MeowSalty/LinguaFlow/backend/internal/backend"

// Round 描述一轮执行的编排器配置。
// 编排器只关心并发、重试策略和 handler，不关心具体操作模式。
type Round struct {
	Concurrency int
	Retry       backend.RetryPolicy
	Context     *ContextConfig
	Handler     RoundHandler
}
