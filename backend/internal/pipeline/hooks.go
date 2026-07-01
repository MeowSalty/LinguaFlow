package pipeline

import (
	"context"
	"log/slog"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/protect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tm"
)

// UnprotectHook 返回一个 PostSegmentHook，将译文中的占位符还原为原片段。
func UnprotectHook(protector protect.Protector, logger *slog.Logger) PostSegmentHook {
	return func(_ context.Context, _ *Document, seg *Segment) error {
		if protector == nil {
			return nil
		}
		if err := protector.Unprotect(seg); err != nil {
			logger.Warn("unprotect failed", "seg", seg.ID, "err", err)
		}
		return nil
	}
}

// RubyRestoreHook 返回一个 PostSegmentHook，还原 ruby 注音。
// keepSet 为 nil 时保留全部 kind。
func RubyRestoreHook(
	restorer *protect.RubyRestorer,
	keepKinds []string,
	retryBackends []backend.Backend,
	retryPolicy backend.RetryPolicy,
	reporter progress.Reporter,
	logger *slog.Logger,
) PostSegmentHook {
	keep := kindSet(keepKinds)
	return func(ctx context.Context, _ *Document, seg *Segment) error {
		if restorer == nil {
			return nil
		}
		restoreSegmentRuby(ctx, seg, restorer, keep, retryBackends, retryPolicy, logger, reporter)
		return nil
	}
}

// TMAddHook 返回一个 PostSegmentHook，将翻译结果写入翻译记忆库。
func TMAddHook(translationMemory tm.TranslationMemory, logger *slog.Logger) PostSegmentHook {
	return func(ctx context.Context, doc *Document, seg *Segment) error {
		if translationMemory == nil {
			return nil
		}
		if err := translationMemory.Add(ctx, seg.Source, seg.Target, doc.SourceLang, doc.TargetLang); err != nil {
			logger.Debug("tm add failed", "err", err)
		}
		return nil
	}
}
