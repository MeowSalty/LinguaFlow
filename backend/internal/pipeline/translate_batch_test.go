package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

type recordingBatchObserver struct {
	events []progress.BatchEvent
}

func (r *recordingBatchObserver) StageStart(string, int) {}
func (r *recordingBatchObserver) SegmentDone()           {}
func (r *recordingBatchObserver) BatchComplete()         {}
func (r *recordingBatchObserver) StageDone()             {}
func (r *recordingBatchObserver) Close() error           { return nil }
func (r *recordingBatchObserver) OnBatchEvent(e progress.BatchEvent) {
	r.events = append(r.events, e)
}

func TestTranslateHandler_ProcessBatch_BackendErrorEmitsBatch(t *testing.T) {
	doc := newTestDoc(1)
	rep := &recordingBatchObserver{}
	err429 := &backend.StatusError{StatusCode: 429, Err: errors.New("too many requests")}
	fb := &fakeBackend{name: "fake", errs: []error{err429}}

	h := &TranslateHandler{
		Backend:   fb,
		BatchSize: 1,
		Renderer:  newTestRenderer(t),
		Reporter:  rep,
		Repair:    defaultRepairOpts(),
		Logger:    quietLogger(),
	}

	// BuildBatches to get the batch structure
	batches, err := h.BuildBatches(context.Background(), doc, nil, 0)
	if err != nil {
		t.Fatalf("build batches: %v", err)
	}
	if len(batches) == 0 {
		t.Fatal("expected at least one batch")
	}

	result := h.ProcessBatch(context.Background(), doc, batches[0], 0, quietLogger())

	// 429 error triggers retry (not unresolved)
	if result.retry == nil {
		t.Fatal("expected retry for 429 error")
	}
	if result.retry.attempt != 1 {
		t.Fatalf("retry.attempt=%d want 1", result.retry.attempt)
	}
	if len(rep.events) != 1 {
		t.Fatalf("batch events=%d want 1", len(rep.events))
	}
	evt := rep.events[0]
	if evt.Status != "failed" || evt.ErrorType != "backend_error" {
		t.Fatalf("evt=%+v", evt)
	}
	if evt.HTTPStatus != 429 {
		t.Fatalf("http_status=%d want 429", evt.HTTPStatus)
	}
}

// TestTranslateHandler_ProcessBatch_RubyConservationIssues 注音守恒信号透传：
// 还原不全时 BatchResult 携带 ruby_restore_incomplete warning；完整还原 /
// preserve_kinds 全排除不携带；重译成功段不复活 DB 重载的旧 issues。
func TestTranslateHandler_ProcessBatch_RubyConservationIssues(t *testing.T) {
	alignedItem := func(id, sourceBase, sourceText, targetBase string) ruby.Item {
		return ruby.Item{ID: id, SourceBase: sourceBase, SourceText: sourceText,
			TargetBase: targetBase, TargetText: sourceText, Kind: "phonetic", Aligned: true}
	}

	cases := []struct {
		name          string
		preserveKinds []string          // nil 展开为全集；空切片为全剥离
		items         []ruby.Item       // 段落携带的注音条目
		staleIssues   []qa.QualityIssue // 模拟 DB 重载的旧 issues
		wantCount     int               // 期望 BatchResult 段上的 issue 总数
		wantMsg       string            // 期望消息（有 issue 时）
	}{
		{
			name: "还原完整不产生 issue",
			items: []ruby.Item{
				alignedItem("1", "我", "wǒ", "I"),
				alignedItem("2", "想", "xiǎng", "want"),
			},
			wantCount: 0,
		},
		{
			name: "还原不全产生 warning",
			items: []ruby.Item{
				alignedItem("1", "我", "wǒ", "I"),
				{ID: "2", SourceBase: "想", SourceText: "xiǎng"}, // 未对齐且无重试后端
			},
			wantCount: 1,
			wantMsg:   "注音还原不完整：应还原 2 条，实际 1 条",
		},
		{
			name:          "preserve_kinds 全排除不产生 issue",
			preserveKinds: []string{},
			items: []ruby.Item{
				{ID: "1", SourceBase: "我", SourceText: "wǒ", TargetBase: "I", TargetText: "wǒ", Kind: "phonetic", Aligned: true},
				{ID: "2", SourceBase: "想", SourceText: "xiǎng", Kind: "creative"},
			},
			wantCount: 0,
		},
		{
			name:        "旧裁决不跨文本存活",
			staleIssues: []qa.QualityIssue{{SegmentIndex: 0, Severity: qa.SeverityError, Code: qa.CheckXMLTagMismatch, Message: "旧问题"}},
			items: []ruby.Item{
				alignedItem("1", "我", "wǒ", "I"),
				alignedItem("2", "想", "xiǎng", "want"),
			},
			wantCount: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := newTestDoc(1)
			doc.Segments[0].Meta = map[string]any{"ruby_items": tc.items}
			doc.Segments[0].Issues = tc.staleIssues

			fb := &fakeBackend{name: "fake", responses: []string{`{"translations":{"1":"I want"}}`}}
			h := &TranslateHandler{
				Backend:           fb,
				BatchSize:         1,
				Renderer:          newTestRenderer(t),
				Repair:            defaultRepairOpts(),
				Logger:            quietLogger(),
				RubyEnabled:       true,
				RubyPreserveKinds: tc.preserveKinds,
			}

			res := h.ProcessBatch(context.Background(), doc, []int{0}, 0, quietLogger())
			if len(res.unresolved) != 0 {
				t.Fatalf("segment unexpectedly unresolved: %v", res.unresolved)
			}
			if res.callbackResult == nil {
				t.Fatal("expected callbackResult from BuildBatchResult")
			}
			ts := res.callbackResult.Segments[0]
			if len(ts.Issues) != tc.wantCount {
				t.Fatalf("issues = %+v, want %d issue(s)", ts.Issues, tc.wantCount)
			}
			if tc.wantCount == 0 {
				return
			}
			iss := ts.Issues[0]
			if iss.Code != qa.CodeRubyRestoreIncomplete {
				t.Errorf("code = %q, want %q", iss.Code, qa.CodeRubyRestoreIncomplete)
			}
			if iss.Severity != qa.SeverityWarning {
				t.Errorf("severity = %q, want %q", iss.Severity, qa.SeverityWarning)
			}
			if iss.SegmentIndex != ts.Index {
				t.Errorf("segment_index = %d, want %d", iss.SegmentIndex, ts.Index)
			}
			if iss.Message != tc.wantMsg {
				t.Errorf("message = %q, want %q", iss.Message, tc.wantMsg)
			}
			if iss.Span != nil {
				t.Errorf("span = %+v, want nil", iss.Span)
			}
		})
	}
}
