package pipeline

import (
	"context"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

// truncatedFakeBackend 在 fakeBackend 之上把每次响应标记为截断
// (模拟适配器检测 finish_reason=MAX_TOKENS/length 后置位的 backend.Response.Truncated)。
type truncatedFakeBackend struct {
	fakeBackend
}

func (f *truncatedFakeBackend) Translate(ctx context.Context, req backend.Request) (*backend.Response, error) {
	resp, err := f.fakeBackend.Translate(ctx, req)
	if resp != nil {
		resp.Truncated = true
	}
	return resp, err
}

// TestTranslateHandler_ProcessBatch_EmitsTruncationSignals 验证截断信号接线：
// 后端响应标记 Truncated 且响应 JSON 缺失收尾括号（经 json.close-braces 修复成功）时，
// 发出的成功 BatchEvent 携带 Truncated==true 与 Repaired 修复算子链。
// 事件捕获复用 translate_batch_test.go 的 recordingBatchObserver。
func TestTranslateHandler_ProcessBatch_EmitsTruncationSignals(t *testing.T) {
	doc := newTestDoc(1)
	rec := &recordingBatchObserver{}

	// 缺失最外层收尾括号 → pickEnvelopeBody 拿不到平衡对象 →
	// closeUnbalancedBraces 补齐并记 "json.close-braces"，随后解析成功
	// （尾随逗号对应的是另一算子 json.trailing-comma，此处不涉及）。
	fb := &truncatedFakeBackend{
		fakeBackend: fakeBackend{
			name:      "fake",
			responses: []string{`{"translations":{"1":"新译文-0"}`},
		},
	}
	h := &TranslateHandler{
		Backend:   fb,
		BatchSize: 1,
		Renderer:  newTestRenderer(t),
		Repair:    defaultRepairOpts(),
		Reporter:  rec,
		Logger:    quietLogger(),
	}

	res := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if len(res.unresolved) != 0 {
		t.Fatalf("unresolved = %v, want empty (close-braces repair should recover)", res.unresolved)
	}

	if len(rec.events) != 1 {
		t.Fatalf("emitted %d batch events, want 1", len(rec.events))
	}
	evt := rec.events[0]
	if evt.Status != "success" {
		t.Fatalf("status = %q, want success", evt.Status)
	}
	if !evt.Truncated {
		t.Errorf("Truncated = false, want true (backend response marked truncated)")
	}
	found := false
	for _, op := range evt.Repaired {
		if op == "json.close-braces" {
			found = true
		}
	}
	if !found {
		t.Errorf("Repaired = %v, want contains \"json.close-braces\"", evt.Repaired)
	}
}

// TestTranslateHandler_ProcessBatch_ParseErrorEmitsTruncatedSignal 验证 parse_error
// 事件同样携带截断信号：后端响应标记 Truncated 且响应完全无法解析（无完整值
// 闭合点，抢救失败）时，失败事件必须记录 Truncated==true——「截断且解析失败」
// 正是最需要截断信号的事件，与 success 事件的诊断口径一致（DB metadata 的
// truncated 不再失真为 false）。
func TestTranslateHandler_ProcessBatch_ParseErrorEmitsTruncatedSignal(t *testing.T) {
	doc := newTestDoc(1)
	rec := &recordingBatchObserver{}

	// 值中途截断且无任何完整值闭合点（首个值即被截断）→ 全链修复失败 → parse_error。
	fb := &truncatedFakeBackend{
		fakeBackend: fakeBackend{
			name:      "fake",
			responses: []string{`{"translations":{"1":"残缺译文`},
		},
	}
	h := &TranslateHandler{
		Backend:   fb,
		BatchSize: 1,
		Renderer:  newTestRenderer(t),
		Repair:    defaultRepairOpts(),
		Reporter:  rec,
		Logger:    quietLogger(),
	}

	res := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
	if len(res.unresolved) == 0 {
		t.Fatal("unresolved is empty, want non-empty (unparseable truncated response)")
	}

	if len(rec.events) != 1 {
		t.Fatalf("emitted %d batch events, want 1", len(rec.events))
	}
	evt := rec.events[0]
	if evt.Status != "failed" || evt.ErrorType != "parse_error" {
		t.Fatalf("status = %q, error_type = %q, want failed/parse_error", evt.Status, evt.ErrorType)
	}
	if !evt.Truncated {
		t.Errorf("Truncated = false, want true on parse_error event (truncated backend response)")
	}
}
