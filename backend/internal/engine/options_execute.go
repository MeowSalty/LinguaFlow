package engine

import (
	"context"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

// ExecuteOption 函数式选项。
type ExecuteOption func(*executeConfig)

type executeConfig struct {
	batchHandler    func(ctx context.Context, result pipeline.BatchResult) error
	segmentFilter   map[int]struct{} // 非空时仅翻译这些索引
	resolvedIndices map[int]struct{} // 本轮池 0 应跳过的已解决段（跨轮增量载体）
}

// WithBatchHandler 注入每批完成后的回调。
// 回调可能被并发调用（多个批同时完成时），实现必须并发安全。
// 回调返回 error 时翻译中止。nil 表示不需要中间持久化。
func WithBatchHandler(fn func(ctx context.Context, result pipeline.BatchResult) error) ExecuteOption {
	return func(c *executeConfig) {
		c.batchHandler = fn
	}
}

// WithSegmentFilter 仅翻译指定索引的段落。
func WithSegmentFilter(indexes []int) ExecuteOption {
	return func(c *executeConfig) {
		if len(indexes) == 0 {
			return
		}
		c.segmentFilter = make(map[int]struct{}, len(indexes))
		for _, idx := range indexes {
			if idx >= 0 {
				c.segmentFilter[idx] = struct{}{}
			}
		}
	}
}

// WithResolvedIndices 注入"已解决段索引集合"（跨轮增量载体，in-memory）。
// handler 的 BuildBatches 在 pending==nil（池 0）时从中排除已解决段，避免跨轮全量重扫。
// translate 轮不使用（由 DB status 驱动增量）。
//
// 注意：空集合是合法的"首轮"语义。ExecuteRound 无条件把 cfg.resolvedIndices 赋给
// doc.ResolvedIndices（含 nil/空），所以这里保留 len>0 守卫即可——首轮空集合时
// cfg.resolvedIndices 保持 nil，ExecuteRound 会据此清空 doc 上一轮残留。
func WithResolvedIndices(set map[int]struct{}) ExecuteOption {
	return func(c *executeConfig) {
		if len(set) > 0 {
			c.resolvedIndices = set
		}
	}
}

// crossRoundResolvedModes 是参与跨轮 in-memory 增量的非翻译模式集合。
// translate 由 DB status 驱动增量，不在此列。
// correct 纳入：它扫描 translated/edited 段，必须跨轮跳过已解决段（与 adjudicate 同理），
// 否则新增非翻译模式时跨轮增量会静默丢失。
var crossRoundResolvedModes = []string{
	pipeline.RoundModeExtract,
	pipeline.RoundModeAdjudicate,
	pipeline.RoundModeSemanticQA,
	pipeline.RoundModeRevise,
	pipeline.RoundModeCorrect,
}

// NewResolvedByMode 返回 per-mode 已解决段索引集合，用于非翻译轮的跨轮增量。
// 调用方在轮次循环中：非翻译轮注入对应集合，轮后用 AccumulateResolved 累加成功段。
//
// 集中定义模式集，避免在 job_runner/preview/quick_translate/cli 多处复制字面量
// 导致新增 handler 模式时静默丢失跨轮增量。
func NewResolvedByMode() map[string]map[int]struct{} {
	out := make(map[string]map[int]struct{}, len(crossRoundResolvedModes))
	for _, m := range crossRoundResolvedModes {
		out[m] = make(map[int]struct{})
	}
	return out
}

// AccumulateResolved 将本轮成功段（result.Resolved）累加到对应模式的 resolved 集合。
// 未知模式（如 translate）被忽略——其跨轮增量由 DB status 驱动。
func AccumulateResolved(resolvedByMode map[string]map[int]struct{}, mode string, resolved []int) {
	if set, ok := resolvedByMode[mode]; ok {
		for _, idx := range resolved {
			set[idx] = struct{}{}
		}
	}
}
