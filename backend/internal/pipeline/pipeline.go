package pipeline

import (
	"context"
	"fmt"
	"log/slog"
)

// Pipeline 顺序执行一组 Stage。任一 Stage 返回错误立即终止。
type Pipeline struct {
	stages           []Stage
	logger           *slog.Logger
	postSegmentHooks []PostSegmentHook
}

func New(logger *slog.Logger, stages ...Stage) *Pipeline {
	if logger == nil {
		logger = slog.Default()
	}
	return &Pipeline{stages: stages, logger: logger}
}

// NewWithHooks 创建带 post-segment hooks 的 Pipeline。
// hooks 在 Translate stage 的每段翻译确认后按注册顺序执行。
func NewWithHooks(logger *slog.Logger, hooks []PostSegmentHook, stages ...Stage) *Pipeline {
	if logger == nil {
		logger = slog.Default()
	}
	return &Pipeline{stages: stages, logger: logger, postSegmentHooks: hooks}
}

func (p *Pipeline) Run(ctx context.Context, doc *Document) error {
	// 注入 postSegment 回调到 Translate stage
	if len(p.postSegmentHooks) > 0 {
		for _, s := range p.stages {
			if t, ok := s.(*Translate); ok {
				t.postSegment = func(ctx context.Context, doc *Document, seg *Segment) error {
					return p.runPostSegmentHooks(ctx, doc, seg)
				}
			}
		}
	}

	for _, s := range p.stages {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		p.logger.Info("stage start", "stage", s.Name(), "segments", len(doc.Segments))
		if err := s.Run(ctx, doc); err != nil {
			return fmt.Errorf("stage %q: %w", s.Name(), err)
		}
		p.logger.Info("stage done", "stage", s.Name())
	}
	return nil
}

// Stages 暴露已注册的 stage 列表，用于日志或诊断。
func (p *Pipeline) Stages() []Stage { return p.stages }

// SetBatchHandler 将回调注入 Pipeline 中的 Translate stage。
// 由 Engine 在调用 Pipeline.Run 前设置，Pipeline 内部的 Translate.Run 调用该回调。
func (p *Pipeline) SetBatchHandler(fn func(ctx context.Context, result BatchResult) error) {
	for _, s := range p.stages {
		if t, ok := s.(*Translate); ok {
			t.BatchHandler = fn
		}
	}
}

// runPostSegmentHooks 按注册顺序执行所有 post-segment hooks。
func (p *Pipeline) runPostSegmentHooks(ctx context.Context, doc *Document, seg *Segment) error {
	for _, hook := range p.postSegmentHooks {
		if err := hook(ctx, doc, seg); err != nil {
			return err
		}
	}
	return nil
}
