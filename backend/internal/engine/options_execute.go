package engine

import (
	"context"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
)

// ExecuteOption 函数式选项。
type ExecuteOption func(*executeConfig)

type executeConfig struct {
	batchHandler  func(ctx context.Context, result pipeline.BatchResult) error
	segmentFilter map[int]struct{} // 非空时仅翻译这些索引
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
