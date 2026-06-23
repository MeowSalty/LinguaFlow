package stages

import (
	"context"
	"log/slog"

	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/protect"
)

// RubyRestore 在 Unprotect 之后执行，将 LLM 输出的注音还原为 <ruby> 标签。
type RubyRestore struct {
	Restorer *protect.RubyRestorer
	Logger   *slog.Logger
}

// NewRubyRestore 创建 RubyRestore stage 实例。
func NewRubyRestore(restorer *protect.RubyRestorer, logger *slog.Logger) *RubyRestore {
	return &RubyRestore{Restorer: restorer, Logger: logger}
}

func (*RubyRestore) Name() string { return "ruby_restore" }

func (s *RubyRestore) Run(_ context.Context, doc *pipeline.Document) error {
	logger := s.Logger
	if logger == nil {
		logger = slog.Default()
	}
	for i := range doc.Segments {
		seg := &doc.Segments[i]
		rubyOutput := extractRubyOutputFromSeg(seg)
		if len(rubyOutput) > 0 {
			if err := s.Restorer.Restore(seg, rubyOutput); err != nil {
				logger.Warn("ruby restore failed, keeping translated text as-is",
					"seg", seg.ID, "err", err)
				// 不阻塞 pipeline，保留译文原样
			}
		}
	}
	return nil
}

// extractRubyOutputFromSeg 从 Segment.Meta 中提取 ruby_output。
func extractRubyOutputFromSeg(seg *pipeline.Segment) []protect.RubyOutputEntry {
	raw, ok := seg.Meta["ruby_output"]
	if !ok {
		return nil
	}
	entries, ok := raw.([]protect.RubyOutputEntry)
	if !ok {
		return nil
	}
	return entries
}
