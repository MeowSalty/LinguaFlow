package qa

import (
	"reflect"
	"testing"
	"time"
)

// TestRecheckFinalIssues_InheritsDismissal 验证同指纹的旧裁决被继承：
// 重检只是重算问题本身，已下达的结论（dismissed 等）不应被静默冲掉；
// 其余字段（severity/message/span）仍以 fresh 重算值为准。
func TestRecheckFinalIssues_InheritsDismissal(t *testing.T) {
	now := time.Now().UTC()
	existing := []QualityIssue{
		{
			Code:        CheckWidthMix,
			Span:        &Span{MatchedText: "Ｘ"},
			Disposition: DispositionDismissed,
			DecidedBy:   intPtr(7),
			DecidedAt:   &now,
			Note:        "人工确认非问题",
		},
	}
	fresh := []QualityIssue{
		{ // 同指纹重检出：问题没变，只有描述细节可能更新
			SegmentIndex: 2,
			Severity:     SeverityWarning,
			Code:         CheckWidthMix,
			Message:      "全半角混用",
			Span:         &Span{MatchedText: "Ｘ"},
		},
	}

	got := RecheckFinalIssues(fresh, existing)
	if len(got) != 1 {
		t.Fatalf("len=%d want 1（fresh 对应项）: %#v", len(got), got)
	}
	iss := got[0]
	if !iss.Dismissed() || iss.DecidedBy == nil || *iss.DecidedBy != 7 ||
		iss.DecidedAt == nil || !iss.DecidedAt.Equal(now) || iss.Note != "人工确认非问题" {
		t.Errorf("同指纹 dismissed 裁决应被继承: %#v", iss)
	}
	if iss.SegmentIndex != 2 || iss.Severity != SeverityWarning || iss.Message != "全半角混用" {
		t.Errorf("其余字段应使用 fresh 新值: %#v", iss)
	}
}

// TestRecheckFinalIssues_KeepsSemanticAndConservation 验证 fresh 为空时语义类与
// 守恒类 issue 原样保留：它们的维护者是 semantic_qa 轮与各写路径，重检既不可
// 重算也不应清除；确定性 issue 则随重检消失（重检认为问题没了就是没了）。
func TestRecheckFinalIssues_KeepsSemanticAndConservation(t *testing.T) {
	existing := []QualityIssue{
		{Code: IssueCodeCalque, Span: &Span{MatchedText: "c1"}},
		{Code: IssueCodeGrammar, Span: &Span{MatchedText: "g1"}, Disposition: DispositionDismissed},
		{Code: CodeRubyTagLoss, Span: &Span{MatchedText: "r1"}},
		{Code: CodeRubyRestoreIncomplete, Span: &Span{MatchedText: "r2"}},
		// 确定性 issue：fresh 为空即重检未检出，应被清除
		{Code: CheckSourceResidual, Span: &Span{MatchedText: "s1"}},
	}

	got := RecheckFinalIssues(nil, existing)
	want := []QualityIssue{
		{Code: IssueCodeCalque, Span: &Span{MatchedText: "c1"}},
		{Code: IssueCodeGrammar, Span: &Span{MatchedText: "g1"}, Disposition: DispositionDismissed},
		{Code: CodeRubyTagLoss, Span: &Span{MatchedText: "r1"}},
		{Code: CodeRubyRestoreIncomplete, Span: &Span{MatchedText: "r2"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("语义与守恒类应保留、确定性应清除\n got: %#v\nwant: %#v", got, want)
	}
}

// TestRecheckFinalIssues_DropsVanishedDeterministic 验证指纹不在 fresh 中的
// 旧确定性 issue 被清除：配置声明的检查集合即为真相，不因"旧账"而保留。
func TestRecheckFinalIssues_DropsVanishedDeterministic(t *testing.T) {
	existing := []QualityIssue{
		{Code: CheckUntranslated, Span: &Span{MatchedText: "Hi"}},
	}
	fresh := []QualityIssue{
		{Code: CheckSourceResidual, Span: &Span{MatchedText: "residual"}},
	}

	got := RecheckFinalIssues(fresh, existing)
	want := []QualityIssue{
		{Code: CheckSourceResidual, Span: &Span{MatchedText: "residual"}, Disposition: DispositionPending},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("消失的旧确定性 issue 应被清除\n got: %#v\nwant: %#v", got, want)
	}
}

// TestRecheckFinalIssues_BothEmpty 验证 fresh 与 existing 均为空时返回空，
// 不产生额外记录。
func TestRecheckFinalIssues_BothEmpty(t *testing.T) {
	if got := RecheckFinalIssues(nil, nil); len(got) != 0 {
		t.Fatalf("want empty, got %#v", got)
	}
}

// TestRecheckFinalIssues_NewFingerprintStaysPending 验证新指纹 issue 保持
// 未决：checker 构造时省略 Disposition 得到的 Go 零值 "" 应归一化为显式
// "pending"，保证落库与内存语义一致。
func TestRecheckFinalIssues_NewFingerprintStaysPending(t *testing.T) {
	existing := []QualityIssue{
		// 指纹不同，不构成"旧裁决"，不阻断 pending 归一化
		{Code: CheckWidthMix, Span: &Span{MatchedText: "Ｘ"}},
	}
	fresh := []QualityIssue{
		{Code: CheckUntranslated, Span: &Span{MatchedText: "Hi"}}, // Disposition 为零值 ""
	}

	got := RecheckFinalIssues(fresh, existing)
	if len(got) != 1 {
		t.Fatalf("len=%d want 1: %#v", len(got), got)
	}
	if got[0].Disposition != DispositionPending || string(got[0].Disposition) != "pending" {
		t.Errorf("新指纹 issue 的 disposition 应归一化为显式 %q: %#v", DispositionPending, got[0])
	}
	if got[0].DecidedBy != nil || got[0].DecidedAt != nil || got[0].Note != "" {
		t.Errorf("新指纹 issue 不应带任何裁决字段: %#v", got[0])
	}
}
