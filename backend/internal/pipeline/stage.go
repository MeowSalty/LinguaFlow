package pipeline

import "context"

// Stage 是流水线中的一个处理步骤。Run 在 Document 上原地修改。
type Stage interface {
	Name() string
	Run(ctx context.Context, doc *Document) error
}
