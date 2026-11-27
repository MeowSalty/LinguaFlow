package pipeline

import (
	"context"
	"log/slog"
)

// RoundHandler 定义单轮执行的策略接口。
// 每种操作模式（translate、extract、consolidate 等）实现此接口，
// 编排器（RunRound）只负责并发 + 重试，不知道具体操作语义。
type RoundHandler interface {
	// BuildBatches 返回待处理的批次列表。
	// pending==nil 时 handler 自行扫描 doc（池 0），应排除 doc.ResolvedIndices 中的段（跨轮增量）；
	// pending!=nil 时仅对给定索引按 poolIndex 缩放后重切。
	// 返回的 [][]int 是抽象索引——翻译模式是段落索引，抽取模式也可以是段落索引。
	BuildBatches(ctx context.Context, doc *Document, pending []int, poolIndex int) ([][]int, error)

	// ModeName 返回模式名称，用于进度上报和日志。
	ModeName() string

	// Finalize 在所有批次完成后执行，接收未解决的索引列表。
	// 可用于汇总、写回失败索引等操作。
	Finalize(ctx context.Context, doc *Document, unresolved []int) error
}

// batchRoundHandler processes stored batches that do not produce staged candidates.
type batchRoundHandler interface {
	RoundHandler
	ProcessBatch(ctx context.Context, doc *Document, idxs []int, attempt int, logger *slog.Logger) batchResult
}
