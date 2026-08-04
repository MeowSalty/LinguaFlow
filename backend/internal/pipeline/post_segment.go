package pipeline

import "context"

// PostSegmentHook 是每段翻译确认后执行的回调。
// Pipeline 按注册顺序依次调用；任一 hook 返回 error 时终止后续 hook。
type PostSegmentHook func(ctx context.Context, doc *Document, seg *Segment) error
