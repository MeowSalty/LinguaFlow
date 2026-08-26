package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/correct"
	"github.com/MeowSalty/LinguaFlow/backend/internal/model"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

// mockWidthMixRule 是 width_mix 改写规则的极简替身（γ 合入前的自洽测试桩，
// 不依赖 correct 包中尚未存在的 WidthMixNormalizeRule）。
// 语义与正式规则一致：由 pending 的 width_mix issue 触发（dismissed 不触发），
// 把 Target 中全部半角 ! ? 转全角，声明 Changed 并消费 CheckWidthMix。
type mockWidthMixRule struct{}

func (r *mockWidthMixRule) Name() string { return "width_mix_normalize_mock" }

func (r *mockWidthMixRule) ResolvedCodes() []string { return []string{qa.CheckWidthMix} }

func (r *mockWidthMixRule) Apply(seg *model.Segment) correct.CorrectionResult {
	trigger := false
	for _, iss := range seg.Issues {
		if iss.Code == qa.CheckWidthMix && iss.IsPending() {
			trigger = true
			break
		}
	}
	if !trigger {
		return correct.CorrectionResult{Reason: "no pending width_mix issue"}
	}
	newTarget := strings.Map(func(r rune) rune {
		switch r {
		case '!':
			return '！'
		case '?':
			return '？'
		}
		return r
	}, seg.Target)
	if newTarget == seg.Target {
		return correct.CorrectionResult{Reason: "nothing to normalize"}
	}
	return correct.CorrectionResult{
		Changed:       true,
		NewTarget:     newTarget,
		Op:            "width_mix.normalize",
		ResolvedCodes: []string{qa.CheckWidthMix},
	}
}

// newWidthMixHandler 构造带真实幂等引擎的 CorrectHandler，构造方式与生产路径
// 同构（options.go buildCorrectPipelineRound）：Checks=ConsumedIssueCodes 且
// 携带 TargetLang。TargetLang="zh" 使 WidthMixChecker 走 CJK 分支——若 langs
// 通路回退为空串，checker 会走拉丁分支把改写后的全角 ！？ 当问题报出、触发回滚，
// 本测试即失败。
func newWidthMixHandler() *CorrectHandler {
	rules := correct.NewWithRules([]correct.Rule{&mockWidthMixRule{}})
	return &CorrectHandler{
		Rules: rules,
		Idempotency: qa.NewEngine(qa.Config{
			Enabled:    true,
			Checks:     rules.ConsumedIssueCodes(),
			TargetLang: "zh",
		}, quietLogger()),
		Logger: quietLogger(),
	}
}

// TestCorrectHandler_WidthMixCJKAppliedNotReverted 回归：CJK 目标下 width_mix
// 规则的改写（!? → ！？）必须通过幂等复验并清掉 pending issue，而不是被语言
// 不敏感的空 lang checker 判为"拉丁译文混入全角字符"而回滚（规则静默失效成环）。
func TestCorrectHandler_WidthMixCJKAppliedNotReverted(t *testing.T) {
	h := newWidthMixHandler()
	doc := &Document{Segments: []model.Segment{
		{
			ID:     "0",
			Source: "这是什么？！",
			Target: "那是什么!?",
			Issues: []qa.QualityIssue{{
				Code:     qa.CheckWidthMix,
				Severity: qa.SeverityWarning,
				Span:     &qa.Span{MatchedText: "!"},
			}},
			Status: "translated",
		},
		{
			ID:     "1",
			Source: "没有问题的段落",
			Target: "没有width_mix issue的段落!?",
			Status: "translated", // 无 pending issue：触发门不开，不得改写
		},
	}}

	result := h.ProcessBatch(context.Background(), doc, []int{0, 1}, 0, quietLogger())
	if result.callbackResult == nil {
		t.Fatal("expected callbackResult")
	}

	// 段 0：改写生效且未被幂等回滚
	if got := doc.Segments[0].Target; got != "那是什么！？" {
		t.Fatalf("segment 0 Target=%q, want %q（改写应生效而非被回滚）",
			got, "那是什么！？")
	}
	// 段 0：pending width_mix issue 应被 FilterOutPendingByCodes 清掉
	if len(doc.Segments[0].Issues) != 0 {
		t.Fatalf("segment 0 issues=%+v, want empty（pending width_mix 应被清除）",
			doc.Segments[0].Issues)
	}

	// 段 1：无 pending issue，保持原样
	if got := doc.Segments[1].Target; got != "没有width_mix issue的段落!?" {
		t.Fatalf("segment 1 Target=%q, want untouched %q", got, "没有width_mix issue的段落!?")
	}

	seg := result.callbackResult.Segments[0]
	if seg.TargetText != "那是什么！？" {
		t.Errorf("callback TargetText=%q, want %q", seg.TargetText, "那是什么！？")
	}
	if len(seg.Issues) != 0 {
		t.Errorf("callback issues=%+v, want empty", seg.Issues)
	}
}
