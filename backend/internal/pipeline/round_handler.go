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
	// handler 负责：收集待处理项、模式特定过滤、分批策略、上下文扩展。
	// 返回的 [][]int 是抽象索引——翻译模式是段落索引，抽取模式也可以是段落索引。
	BuildBatches(ctx context.Context, doc *Document) ([][]int, error)

	// ProcessBatch 处理单个批次。
	// idxs 是批次内的索引列表，attempt 是当前重试次数（从 0 开始）。
	// 返回 batchResult 指示未解决/重试状态。
	ProcessBatch(ctx context.Context, doc *Document, idxs []int, attempt int, logger *slog.Logger) batchResult

	// ModeName 返回模式名称，用于进度上报和日志。
	ModeName() string

	// Finalize 在所有批次完成后执行，接收未解决的索引列表。
	// 可用于汇总、写回失败索引等操作。
	Finalize(ctx context.Context, doc *Document, unresolved []int) error
}
