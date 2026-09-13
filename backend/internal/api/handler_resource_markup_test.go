package api

import (
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/parser"
)

// TestTargetMarkupProblemDetail 锁定导出预检 409 的 detail 文案：前端
// buildRequestFailureError 直接把 problem.detail 当 toast 展示，因此必须单行、
// 点名段号、并给出首个错误的定位与原因，用户才能据此找到并修正坏段。
func TestTargetMarkupProblemDetail(t *testing.T) {
	t.Run("单个缺陷点名段号与定位", func(t *testing.T) {
		detail := targetMarkupProblemDetail([]parser.TargetDefect{{
			SegmentID: "879",
			Location:  "item/xhtml/p-003.xhtml body/p[12]",
			Reason:    "XML syntax error on line 1: element <ruby> closed by </lf-fragment>",
		}})
		for _, want := range []string{"#879", "item/xhtml/p-003.xhtml body/p[12]", "element <ruby>"} {
			if !strings.Contains(detail, want) {
				t.Errorf("detail 缺少 %q：%s", want, detail)
			}
		}
		if strings.ContainsAny(detail, "\n\r") {
			t.Errorf("detail 必须单行：%q", detail)
		}
	})

	t.Run("缺陷过多时截断段号但保留总数", func(t *testing.T) {
		defects := make([]parser.TargetDefect, 8)
		for i := range defects {
			defects[i] = parser.TargetDefect{SegmentID: string(rune('1' + i)), Location: "f.xhtml body/p[0]", Reason: "boom"}
		}
		detail := targetMarkupProblemDetail(defects)
		if !strings.Contains(detail, "共 8 个段落") {
			t.Errorf("detail 应包含缺陷总数：%s", detail)
		}
		if strings.Count(detail, "#") != maxTargetMarkupIDsInDetail+1 { // +1 为「首个错误」重复提及
			t.Errorf("detail 列出的段号数应被截断到 %d：%s", maxTargetMarkupIDsInDetail, detail)
		}
	})

	t.Run("空缺陷列表不 panic", func(t *testing.T) {
		if detail := targetMarkupProblemDetail(nil); detail == "" {
			t.Error("空缺陷列表也应给出兜底文案")
		}
	})
}
