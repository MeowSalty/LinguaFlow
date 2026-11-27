package pipeline

import (
	"context"
	"log/slog"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/correct"
	"github.com/MeowSalty/LinguaFlow/backend/internal/markup"
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
			if !correctEligible(seg) {
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

func correctEligible(seg *Segment) bool {
	return seg.Translate && (seg.Status == "translated" || seg.Status == "edited") && seg.Target != ""
}

// ProcessBatch applies rules per segment; on a change idempotency-reruns the
// consumed checker(s). Revert + keep issues if the rewrite fails to clear the code.
func (h *CorrectHandler) ProcessBatch(ctx context.Context, doc *Document, idxs []int, _ int, logger *slog.Logger) batchResult {
	if logger == nil {
		logger = h.logger()
	}
	callbackSegs := make([]TranslatedSegment, 0, len(idxs))
	for _, idx := range idxs {
		seg := &doc.Segments[idx]
		result, err := h.correctSegment(ctx, nil, doc.Format, idx, *seg, logger)
		if err != nil {
			return batchResult{err: err}
		}
		seg.Target, seg.Issues = result.TargetText, result.Issues
		result.Issues = append([]qa.QualityIssue(nil), result.Issues...)
		callbackSegs = append(callbackSegs, result)
	}
	return batchResult{callbackResult: &BatchResult{Segments: callbackSegs}}
}

func (h *CorrectHandler) correctSegment(ctx context.Context, bounds *backend.BuildBounds, format string, index int, seg Segment, logger *slog.Logger) (TranslatedSegment, error) {
	result := TranslatedSegment{Index: index, ID: seg.ID, SourceText: seg.Source, TargetText: seg.Target, Issues: seg.Issues, Meta: seg.Meta, Protected: seg.Protected}
	if h.Rules == nil || !h.Rules.Enabled() {
		return result, nil
	}
	res := h.Rules.Apply(&seg)
	if !res.Changed || res.NewTarget == seg.Target {
		return result, nil
	}
	if markup.RequiresWellFormedTargets(format) {
		if err := markup.TargetRegression(seg.Target, res.NewTarget); err != nil {
			logger.Warn("correct rewrite broke markup structure", "op", res.Op, "segment_id", seg.ID, "err", err)
			return result, nil
		}
	}
	resolvedSet := codeSet(h.Rules.ConsumedIssueCodes())
	if h.Idempotency != nil && len(resolvedSet) > 0 {
		inputs := []qa.CheckInput{{Index: index, SourceText: seg.Source, TargetText: res.NewTarget, Meta: seg.Meta, Protected: seg.Protected}}
		var issues []qa.QualityIssue
		if bounds == nil {
			issues = h.Idempotency.Run(ctx, inputs)
		} else {
			var err error
			issues, err = h.Idempotency.RunBounded(ctx, bounds, inputs)
			if err != nil {
				return result, err
			}
		}
		for _, issue := range issues {
			if _, consumed := resolvedSet[issue.Code]; consumed && !correctDismissed(seg.Issues, issue) {
				logger.Warn("correct idempotency failed", "op", res.Op, "segment_id", seg.ID)
				return result, nil
			}
		}
	}
	if bounds != nil {
		if err := bounds.ReserveText(int64(len(res.NewTarget)), int64(len(res.NewTarget))); err != nil {
			return result, err
		}
		if err := backend.ReserveSlice[qa.QualityIssue](bounds, int64(len(seg.Issues))); err != nil {
			return result, err
		}
	}
	result.TargetText = res.NewTarget
	result.Issues = qa.FilterOutPendingByCodes(seg.Issues, resolvedSet)
	if bounds != nil {
		var owned []qa.QualityIssue
		if err := bounds.CloneInto(result.Issues, &owned); err != nil {
			return result, err
		}
		result.Issues = owned
	}
	return result, nil
}

func correctDismissed(existing []qa.QualityIssue, fresh qa.QualityIssue) bool {
	for _, issue := range existing {
		if issue.Dismissed() && issue.Code == fresh.Code && qa.MatchedText(issue) == qa.MatchedText(fresh) {
			return true
		}
	}
	return false
}

func codeSet(codes []string) map[string]struct{} {
	m := make(map[string]struct{}, len(codes))
	for _, c := range codes {
		m[c] = struct{}{}
	}
	return m
}
