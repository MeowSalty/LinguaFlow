package pipeline

import (
	"context"
	"log/slog"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
)

// lookupHints 为 idxs 中每段查 glossary / TM 并合并去重。
// glossary 以 source+target 为键；TM 同样以 source+target 去重，保留最高分。
func (s *RoundExecutor) lookupHints(ctx context.Context, doc *Document, idxs []int, logger *slog.Logger) ([]prompt.GlossaryEntry, []prompt.TMHint) {
	if ctx.Err() != nil {
		return nil, nil
	}
	var (
		glosOrder []string
		glosMap   = map[string]prompt.GlossaryEntry{}
		tmOrder   []string
		tmMap     = map[string]prompt.TMHint{}
	)
	for _, idx := range idxs {
		seg := &doc.Segments[idx]
		if s.Glossary != nil {
			hits, err := s.Glossary.Lookup(ctx, seg.Source, doc.SourceLang, doc.TargetLang)
			if err != nil {
				logger.Warn("glossary lookup failed", "err", err, "seg", seg.ID)
			}
			for _, h := range hits {
				key := h.Source + "\x00" + h.Target
				if _, ok := glosMap[key]; !ok {
					glosOrder = append(glosOrder, key)
				}
				glosMap[key] = prompt.GlossaryEntry{Source: h.Source, Target: h.Target, Notes: h.Notes}
			}
		}
		if s.TM != nil {
			ms, err := s.TM.Search(ctx, seg.Source, doc.SourceLang, doc.TargetLang)
			if err != nil {
				logger.Warn("tm search failed", "err", err, "seg", seg.ID)
			}
			for _, m := range ms {
				key := m.Source + "\x00" + m.Target
				if old, ok := tmMap[key]; !ok {
					tmOrder = append(tmOrder, key)
					tmMap[key] = prompt.TMHint{Source: m.Source, Target: m.Target, Score: m.Score}
				} else if m.Score > old.Score {
					tmMap[key] = prompt.TMHint{Source: m.Source, Target: m.Target, Score: m.Score}
				}
			}
		}
	}
	glos := make([]prompt.GlossaryEntry, 0, len(glosOrder))
	for _, k := range glosOrder {
		glos = append(glos, glosMap[k])
	}
	hints := make([]prompt.TMHint, 0, len(tmOrder))
	for _, k := range tmOrder {
		hints = append(hints, tmMap[k])
	}
	return glos, hints
}

// absorbInlineGlossary 把 LLM 在 translate 响应中携带的 glossary 条目写入运行时 Glossary，
// 并在并发冲突时就地修正本批 translations，避免文档内同一术语翻译不一致。
//
// 工作流：过滤候选 → 批量 Add → 处理 Skipped。FileGlossary 的 First-Wins 严格合并会让
// 后到 worker 提交的 source 被丢弃，但其本批译文已经写了被丢弃的 target；这里通过
// glossary.SafeReplace 把这些字面值改写为权威表里的版本。CJK 直替、拉丁系按词边界、
// 歧义场景仅 Warn 不动。InlineConflictStrategy == off 时跳过修正，沿用旧行为。
//
// translations 会被原地改写——调用方必须在拿到本函数返回后再写回 doc.Segments[*].Target。
func (s *RoundExecutor) absorbInlineGlossary(
	ctx context.Context,
	entries []prompt.BootstrapEntry,
	translations map[string]string,
	targetLang string,
	logger *slog.Logger,
) {
	if !s.InlineBootstrap || len(entries) == 0 || s.Glossary == nil {
		return
	}
	minLen := s.MinBootstrapSourceLen
	if minLen < 1 {
		minLen = 2
	}
	candidates := make([]glossary.Entry, 0, len(entries))
	for _, e := range entries {
		if len([]rune(e.Source)) < minLen {
			continue
		}
		if e.Source == "" || e.Target == "" {
			continue
		}
		candidates = append(candidates, glossary.Entry{
			Source: e.Source,
			Target: e.Target,
			Notes:  e.Notes,
		})
	}
	if len(candidates) == 0 {
		return
	}
	result, err := s.Glossary.Add(ctx, candidates...)
	if err != nil {
		// FileGlossary 现实现不会返 error，但为接口健壮考虑保留分支：err 不阻断翻译。
		logger.Warn("inline glossary add failed", "err", err)
	}
	if len(result.Added) > 0 {
		logger.Debug("inline glossary absorbed",
			"added", len(result.Added),
			"skipped", len(result.Skipped),
			"received", len(entries))
	}

	if s.InlineConflictStrategy != config.InlineConflictRewriteLocal {
		return
	}
	if len(result.Skipped) == 0 || len(translations) == 0 {
		return
	}
	s.rewriteConflictsInBatch(result.Skipped, translations, targetLang, logger)
}

// rewriteConflictsInBatch 遍历 Skipped 列表，把本批译文里 worker 自己用的 target 字面值
// 替换为权威表里已有的版本。仅处理 Reason == SkipReasonExists 且 target 不同的项。
func (s *RoundExecutor) rewriteConflictsInBatch(
	skipped []glossary.SkippedEntry,
	translations map[string]string,
	targetLang string,
	logger *slog.Logger,
) {
	for _, sk := range skipped {
		if sk.Reason != glossary.SkipReasonExists {
			continue
		}
		from := sk.Proposed.Target
		to := sk.Existing.Target
		if from == "" || from == to {
			continue
		}
		rewrote := 0
		var warns []string
		for id, text := range translations {
			newText, replaced, warn := glossary.SafeReplace(text, from, to, targetLang)
			if replaced {
				translations[id] = newText
				rewrote++
			}
			if warn != "" {
				warns = append(warns, warn)
			}
		}
		if rewrote > 0 {
			logger.Info("inline glossary conflict: rewrote local target",
				"source", sk.Proposed.Source,
				"from", from,
				"to", to,
				"rewrites", rewrote)
		}
		if len(warns) > 0 {
			logger.Warn("inline glossary conflict: ambiguous match",
				"source", sk.Proposed.Source,
				"proposed_target", from,
				"authoritative_target", to,
				"details", warns)
		}
	}
}
