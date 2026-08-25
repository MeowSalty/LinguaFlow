package pipeline

import (
	"context"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

// TestTranslateHandler_ProcessBatch_MissingSegmentDropsStaleState 验证 LLM
// 批量响应缺失某段 id 时，漏译段以「本轮无产出」形态流入 BatchResult：
// TargetText 空串、Failed=true、Issues 为 nil，不携带 DB 重载的旧译文与旧裁决。
//
// 触发路径：重译存量段（status_filter=all/skip_approved 或显式圈选首轮跳过过滤）
// 时，doc.Segments 已被 buildSegmentInputs 载入旧 target_text + 旧 quality_issues；
// 若 LLM partial response 漏掉该段，旧译文非空会绕过下游空串守卫，旧裁决连同
// 对未变更文本新扫的同指纹 pending issue 一并落库，产生 dismissed + pending
// 孪生条目（与本提交「translate 轮旧裁决不跨文本存活」不变量矛盾）。
func TestTranslateHandler_ProcessBatch_MissingSegmentDropsStaleState(t *testing.T) {
	doc := newTestDoc(2)
	// 模拟 DB 重载：两段均已有旧译文与旧裁决（含用户 dismissed）。
	doc.Segments[0].Target = "旧译文-0"
	doc.Segments[0].Issues = []qa.QualityIssue{{
		SegmentIndex: 0, Severity: qa.SeverityError, Code: qa.CheckXMLTagMismatch,
		Message: "旧问题-0", Disposition: qa.DispositionDismissed,
	}}
	doc.Segments[1].Target = "旧译文-1"
	doc.Segments[1].Issues = []qa.QualityIssue{{
		SegmentIndex: 1, Severity: qa.SeverityWarning, Code: qa.CheckXMLTagMismatch,
		Message: "旧问题-1", Disposition: qa.DispositionPending,
	}}

	// LLM 只返回段 0 的译文，段 1 缺失（partial response）。
	fb := &fakeBackend{name: "fake", responses: []string{`{"translations":{"1":"新译文-0"}}`}}
	h := &TranslateHandler{
		Backend:   fb,
		BatchSize: 2,
		Renderer:  newTestRenderer(t),
		Repair:    defaultRepairOpts(),
		Logger:    quietLogger(),
	}

	res := h.ProcessBatch(context.Background(), doc, []int{0, 1}, 0, quietLogger())

	// 段 1 应进入 unresolved（LLM 漏译）。
	foundMissing := false
	for _, idx := range res.unresolved {
		if idx == 1 {
			foundMissing = true
		}
	}
	if !foundMissing {
		t.Fatalf("unresolved = %v, want segment 1 (LLM missing)", res.unresolved)
	}
	if res.callbackResult == nil {
		t.Fatal("expected callbackResult from BuildBatchResult")
	}

	// 段 0（成功）：新译文写入，旧裁决已清空（不跨文本存活）。
	ts0 := res.callbackResult.Segments[0]
	if ts0.TargetText != "新译文-0" {
		t.Errorf("seg0 TargetText = %q, want %q", ts0.TargetText, "新译文-0")
	}
	if ts0.Failed {
		t.Errorf("seg0 Failed = true, want false")
	}
	if len(ts0.Issues) != 0 {
		t.Errorf("seg0 Issues = %+v, want empty (stale dismissed must not survive)", ts0.Issues)
	}

	// 段 1（漏译）：本轮无产出 —— 空译文、Failed=true、无旧 issues 携带。
	// 这是核心不变量：旧译文非空不得绕过下游空串守卫复活旧裁决。
	ts1 := res.callbackResult.Segments[1]
	if ts1.TargetText != "" {
		t.Errorf("seg1 TargetText = %q, want empty (missing segment must not carry stale target)", ts1.TargetText)
	}
	if !ts1.Failed {
		t.Errorf("seg1 Failed = false, want true")
	}
	if len(ts1.Issues) != 0 {
		t.Errorf("seg1 Issues = %+v, want empty (stale issues must not leak via missing segment)", ts1.Issues)
	}
}
