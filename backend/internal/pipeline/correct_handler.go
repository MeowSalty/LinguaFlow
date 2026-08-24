package pipeline

import (
	"context"
	"log/slog"

	"github.com/MeowSalty/LinguaFlow/backend/internal/correct"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

// CorrectHandler is a RoundHandler that mechanically rewrites segment targets
// to resolve quality issues. It is a pure-local, no-LLM round: an issue-stream
// consumer that may write (unlike QA which is read-only). Idempotency is enforced
// by re-running the SAME checker(s) the rules claim to resolve; if the rewrite
// would not clear the issue, the change is reverted and the original issues kept.
//
// 契约：correct 规则只能由 pending issue 触发，dismissed issue 对规则不可见
// （规则实现必须过滤 dismissed；幂等检查豁免已裁决指纹；
// qa.FilterOutPendingByCodes 保留 dismissed 记录）。未来新增规则必须遵守此约束，
// 不得以 dismissed 为触发依据。
type CorrectHandler struct {
	Rules       *correct.Engine
	Idempotency *qa.Engine // rebuilt once for the union of rules' ResolvedCodes
	Reporter    progress.Reporter
	Logger      *slog.Logger
}

func (h *CorrectHandler) ModeName() string { return RoundModeCorrect }

func (h *CorrectHandler) Finalize(_ context.Context, _ *Document, _ []int) error {
	return nil
}

func (h *CorrectHandler) logger() *slog.Logger {
	if h.Logger == nil {
		return slog.Default()
	}
	return h.Logger
}

func (h *CorrectHandler) reporter() progress.Reporter {
	if h.Reporter == nil {
		return progress.Nop{}
	}
	return h.Reporter
}

// BuildBatches scans segments with status translated/edited and non-empty Target
// (pool 0, excluding doc.ResolvedIndices cross-round increments). Returns a single
// batch (local work, no word/segment budget). pending!=nil re-slices the given indices.
func (h *CorrectHandler) BuildBatches(_ context.Context, doc *Document, pending []int, _ int) ([][]int, error) {
	logger := h.logger()
	if h.Rules == nil || !h.Rules.Enabled() {
		logger.Info("correct handler: no enabled rules, skipping")
		return nil, nil
	}
	var scan []int
	if pending != nil {
		scan = pending
	} else {
		for i := range doc.Segments {
			seg := &doc.Segments[i]
			// 尊重段落选择（Translate 标记）：与 TranslateHandler 一致，
			// 用户仅选中部分段落时，未选段不进入 correct 扫描，避免误改未选译文。
			if !seg.Translate {
				continue
			}
			if seg.Status != "translated" && seg.Status != "edited" {
				continue
			}
			if seg.Target == "" {
				continue
			}
			if doc.ResolvedIndices != nil {
				if _, ok := doc.ResolvedIndices[i]; ok {
					continue
				}
			}
			scan = append(scan, i)
		}
	}
	if len(scan) == 0 {
		logger.Info("correct handler: no segments to process")
		return nil, nil
	}
	return [][]int{scan}, nil
}

// ProcessBatch applies rules per segment; on a change idempotency-reruns the
// consumed checker(s). Revert + keep issues if the rewrite fails to clear the code.
func (h *CorrectHandler) ProcessBatch(ctx context.Context, doc *Document, idxs []int, _ int, logger *slog.Logger) batchResult {
	rep := h.reporter()
	if logger == nil {
		logger = h.logger()
	}
	resolvedSet := codeSet(h.Rules.ConsumedIssueCodes())

	callbackSegs := make([]TranslatedSegment, 0, len(idxs))
	for _, idx := range idxs {
		seg := &doc.Segments[idx]
		// 收集该段已 dismissed issue 的指纹，用于幂等检查豁免：
		// 用户已判定非问题的模式，即使在新译文上仍存在，也不计为"未修干净"。
		dismissedFPs := make(map[string]struct{}, len(seg.Issues))
		for _, iss := range seg.Issues {
			if iss.Dismissed() {
				dismissedFPs[qa.Fingerprint(iss)] = struct{}{}
			}
		}
		res := h.Rules.Apply(seg)
		if !res.Changed {
			callbackSegs = append(callbackSegs, TranslatedSegment{
				Index:      idx,
				ID:         seg.ID,
				SourceText: seg.Source,
				TargetText: seg.Target,
				Issues:     append([]qa.QualityIssue(nil), seg.Issues...),
				Protected:  seg.Protected,
			})
			rep.SegmentDone()
			continue
		}
		oldTarget := seg.Target
		seg.Target = res.NewTarget
		// Idempotency: rerun consumed checker(s) on the rewritten single segment.
		var stillHas []qa.QualityIssue
		if h.Idempotency != nil && len(resolvedSet) > 0 {
			inputs := []qa.CheckInput{{
				Index:      idx,
				SourceText: seg.Source,
				TargetText: res.NewTarget,
				Meta:       seg.Meta,
				Protected:  seg.Protected,
			}}
			issues := h.Idempotency.Run(ctx, inputs)
			for _, iss := range issues {
				if _, ok := resolvedSet[iss.Code]; !ok {
					continue
				}
				if _, dismissed := dismissedFPs[qa.Fingerprint(iss)]; dismissed {
					continue // 用户已裁决非问题，豁免
				}
				stillHas = append(stillHas, iss)
			}
		}
		if len(stillHas) > 0 {
			// Revert; keep original issues (the warning stays).
			seg.Target = oldTarget
			logger.Warn("correct idempotency failed, reverted",
				"op", res.Op, "segment_id", seg.ID)
			callbackSegs = append(callbackSegs, TranslatedSegment{
				Index:      idx,
				ID:         seg.ID,
				SourceText: seg.Source,
				TargetText: seg.Target,
				Issues:     append([]qa.QualityIssue(nil), seg.Issues...),
				Protected:  seg.Protected,
			})
			rep.SegmentDone()
			continue
		}
		// Apply: drop resolved codes from seg.Issues.
		seg.Issues = qa.FilterOutPendingByCodes(seg.Issues, resolvedSet)
		logger.Info("correct applied", "op", res.Op, "segment_id", seg.ID)
		callbackSegs = append(callbackSegs, TranslatedSegment{
			Index:      idx,
			ID:         seg.ID,
			SourceText: seg.Source,
			TargetText: seg.Target,
			Issues:     append([]qa.QualityIssue(nil), seg.Issues...),
			Protected:  seg.Protected,
		})
		rep.SegmentDone()
	}
	return batchResult{callbackResult: &BatchResult{Segments: callbackSegs}}
}

func codeSet(codes []string) map[string]struct{} {
	m := make(map[string]struct{}, len(codes))
	for _, c := range codes {
		m[c] = struct{}{}
	}
	return m
}
