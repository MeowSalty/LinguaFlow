package qa

import (
	"reflect"
	"testing"
	"time"
)

// TestFilterOutPendingByCodes_DropsPendingHit 验证 code 命中且仍为 pending
// （含 Go 零值 disposition）的 issue 被移除。
func TestFilterOutPendingByCodes_DropsPendingHit(t *testing.T) {
	issues := []QualityIssue{
		{Code: CheckUntranslated, Span: &Span{MatchedText: "Hi"}},
		{Code: CheckUntranslated}, // 零值 disposition 语义等同 pending，也应移除
	}
	got := FilterOutPendingByCodes(issues, map[string]struct{}{CheckUntranslated: {}})
	if len(got) != 0 {
		t.Fatalf("命中且未 dismissed 的 issue 应被移除，got %#v", got)
	}
}

// TestFilterOutPendingByCodes_KeepsDismissedHit 验证 code 命中但已 dismissed 的
// 记录保留：用户有意为之，删除会让其以 pending 复活。
func TestFilterOutPendingByCodes_KeepsDismissedHit(t *testing.T) {
	now := time.Now().UTC()
	dismissed := QualityIssue{
		Code:        CheckUntranslated,
		Span:        &Span{MatchedText: "Hi"},
		Disposition: DispositionDismissed,
		DecidedBy:   intPtr(7),
		DecidedAt:   &now,
		Note:        "人工确认非问题",
	}
	got := FilterOutPendingByCodes([]QualityIssue{dismissed}, map[string]struct{}{CheckUntranslated: {}})
	if len(got) != 1 {
		t.Fatalf("dismissed 记录应保留，len=%d: %#v", len(got), got)
	}
	if !got[0].Dismissed() || got[0].Note != "人工确认非问题" {
		t.Errorf("dismissed 记录应原样保留，got %#v", got[0])
	}
}

// TestFilterOutPendingByCodes_KeepsUntargetedPending 验证 code 未命中的 pending
// issue 原样保留。
func TestFilterOutPendingByCodes_KeepsUntargetedPending(t *testing.T) {
	issues := []QualityIssue{
		{Code: CheckWidthMix, Span: &Span{MatchedText: "Ｘ"}},
		{Code: IssueCodeGrammar, Span: &Span{MatchedText: "语法"}},
	}
	got := FilterOutPendingByCodes(issues, map[string]struct{}{CheckUntranslated: {}})
	if !reflect.DeepEqual(got, issues) {
		t.Fatalf("范围外 issue 应原样保留\n got: %#v\nwant: %#v", got, issues)
	}
}

// TestFilterOutPendingByCodes_EmptyDropKeepsAll 验证 drop 为空（含 nil）时
// 全部 issue 原样返回。
func TestFilterOutPendingByCodes_EmptyDropKeepsAll(t *testing.T) {
	issues := []QualityIssue{
		{Code: CheckUntranslated},
		{Code: IssueCodeMistranslation, Disposition: DispositionDismissed},
	}
	for _, drop := range []map[string]struct{}{{}, nil} {
		got := FilterOutPendingByCodes(issues, drop)
		if !reflect.DeepEqual(got, issues) {
			t.Fatalf("drop=%v 时应原样返回全部 issue\n got: %#v\nwant: %#v", drop, got, issues)
		}
	}
}

// TestReviseFinalIssues_NotRanKeepsNonTargets 验证 qaRan=false 时不重算：
// targeted pending（确定性与语义各一）被移除；范围外 pending 与 dismissed 记录
// 原样保留，fresh 不参与。
func TestReviseFinalIssues_NotRanKeepsNonTargets(t *testing.T) {
	targeted := []string{CheckUntranslated, IssueCodeMistranslation}
	existing := []QualityIssue{
		{Code: CheckUntranslated, Span: &Span{MatchedText: "Hi"}},                                    // 目标内 pending（确定性）→ 移除
		{Code: IssueCodeMistranslation, Span: &Span{MatchedText: "错译"}},                              // 目标内 pending（语义）→ 移除
		{Code: CheckWidthMix, Span: &Span{MatchedText: "Ｘ"}},                                         // 范围外 pending（确定性）→ 保留
		{Code: IssueCodeGrammar, Span: &Span{MatchedText: "语法"}},                                     // 范围外 pending（语义）→ 保留
		{Code: CheckUntranslated, Span: &Span{MatchedText: "Yo"}, Disposition: DispositionDismissed}, // dismissed → 保留
	}
	fresh := []QualityIssue{{Code: CheckDuplicate}} // qaRan=false 不应参与

	got := ReviseFinalIssues(existing, fresh, targeted, false)
	want := []QualityIssue{
		{Code: CheckWidthMix, Span: &Span{MatchedText: "Ｘ"}},
		{Code: IssueCodeGrammar, Span: &Span{MatchedText: "语法"}},
		{Code: CheckUntranslated, Span: &Span{MatchedText: "Yo"}, Disposition: DispositionDismissed},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("qaRan=false 应仅移除 targeted pending\n got: %#v\nwant: %#v", got, want)
	}
}

// TestReviseFinalIssues_RanRecomputesDeterministic 验证 qaRan=true 的重算契约：
//   - fresh 的确定性 issue 进入结果；
//   - existing 中同指纹 dismissed 的确定性 issue 裁决被继承到 fresh 副本上；
//   - existing 中未被 fresh 检出的旧确定性 issue 消失（重算语义）;
//   - 范围外语义 pending 保留，targeted 语义 pending 被移除。
func TestReviseFinalIssues_RanRecomputesDeterministic(t *testing.T) {
	userID := 42
	now := time.Now().UTC()
	start, end := 0, 5
	existing := []QualityIssue{
		{ // 同指纹 dismissed：裁决应继承到 fresh 副本上
			Code:        CheckUntranslated,
			Span:        &Span{MatchedText: "Hello"},
			Disposition: DispositionDismissed,
			DecidedBy:   intPtr(userID),
			DecidedAt:   &now,
			Note:        "人工确认非问题",
		},
		{ // 未被 fresh 检出：随重算消失
			Code: CheckWidthMix,
			Span: &Span{MatchedText: "Ｘ"},
		},
		{ // 范围外语义 pending：保留
			Code: IssueCodeGrammar,
			Span: &Span{MatchedText: "语法"},
		},
		{ // targeted 语义 pending：移除
			Code: IssueCodeMistranslation,
			Span: &Span{MatchedText: "错译"},
		},
	}
	fresh := []QualityIssue{
		{
			SegmentIndex: 3,
			Severity:     SeverityError,
			Code:         CheckUntranslated,
			Message:      "found untranslated text",
			Span:         &Span{MatchedText: "Hello", TargetStart: &start, TargetEnd: &end},
		},
		{ // 全新确定性 issue：进入结果且保持 pending
			Code: CheckSourceResidual,
			Span: &Span{MatchedText: "residual"},
		},
	}

	got := ReviseFinalIssues(existing, fresh, []string{IssueCodeMistranslation}, true)
	if len(got) != 3 {
		t.Fatalf("len=%d want 3（继承副本 + 新检出 + 范围外语义）: %#v", len(got), got)
	}

	// got[0]：fresh 副本，继承 dismissed 裁决字段，其余字段用 fresh 新值。
	inherited := got[0]
	if inherited.Code != CheckUntranslated {
		t.Fatalf("got[0].code = %q, want %q", inherited.Code, CheckUntranslated)
	}
	if !inherited.Dismissed() || inherited.DecidedBy == nil || *inherited.DecidedBy != userID ||
		inherited.DecidedAt == nil || !inherited.DecidedAt.Equal(now) || inherited.Note != "人工确认非问题" {
		t.Errorf("同指纹 dismissed 的裁决应被继承: %#v", inherited)
	}
	if inherited.SegmentIndex != 3 || inherited.Severity != SeverityError ||
		inherited.Message != "found untranslated text" ||
		inherited.Span == nil || inherited.Span.TargetStart == nil || *inherited.Span.TargetStart != start {
		t.Errorf("fresh 新字段应保留: %#v", inherited)
	}

	// got[1]：全新检出的确定性 issue，保持 pending。
	renewed := got[1]
	if renewed.Code != CheckSourceResidual {
		t.Fatalf("got[1].code = %q, want %q", renewed.Code, CheckSourceResidual)
	}
	if renewed.Disposition != DispositionPending || renewed.DecidedBy != nil || renewed.DecidedAt != nil || renewed.Note != "" {
		t.Errorf("新检出 issue 应保持显式 pending 且无裁决字段: %#v", renewed)
	}

	// got[2]：范围外的语义 pending 保留。
	sem := got[2]
	if sem.Code != IssueCodeGrammar || !sem.IsPending() {
		t.Errorf("范围外语义 pending 应保留: %#v", sem)
	}

	// 旧确定性 issue 与 targeted 语义 pending 均不应出现。
	for _, iss := range got {
		if iss.Code == CheckWidthMix {
			t.Errorf("未被 fresh 检出的旧确定性 issue 应消失: %#v", iss)
		}
		if iss.Code == IssueCodeMistranslation {
			t.Errorf("targeted 语义 pending 应被移除: %#v", iss)
		}
	}
}

// TestReviseFinalIssues_RanEmptyFreshKeepsSemanticOnly 验证 qaRan=true 且 fresh
// 为空、kept 全为语义时：结果只含 kept 的语义部分（裁决原样保留）。
func TestReviseFinalIssues_RanEmptyFreshKeepsSemanticOnly(t *testing.T) {
	existing := []QualityIssue{
		{Code: IssueCodeNaturalness, Span: &Span{MatchedText: "n1"}},                               // 范围外语义 pending
		{Code: IssueCodeCalque, Span: &Span{MatchedText: "c1"}, Disposition: DispositionDismissed}, // dismissed 语义
	}

	got := ReviseFinalIssues(existing, nil, []string{IssueCodeMistranslation}, true)
	if !reflect.DeepEqual(got, existing) {
		t.Fatalf("fresh 为空时应只保留 kept 的语义部分\n got: %#v\nwant: %#v", got, existing)
	}
}
