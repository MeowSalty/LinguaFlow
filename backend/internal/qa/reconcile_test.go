package qa

import (
	"reflect"
	"testing"
	"time"
)

// intPtr 返回指向 v 的指针，用于构造 DecidedBy。
func intPtr(v int) *int { return &v }

// TestReconcileIssues_EmptyExisting 验证 existing 为空时直接返回 fresh，
// 且不修改 fresh 的内容。
func TestReconcileIssues_EmptyExisting(t *testing.T) {
	start, end := 10, 15
	now := time.Now().UTC()
	fresh := []QualityIssue{
		{
			SegmentIndex: 0,
			Severity:     SeverityError,
			Code:         "untranslated",
			Message:      "found untranslated text",
			Span:         &Span{MatchedText: "Hello", TargetStart: &start, TargetEnd: &end},
			Disposition:  DispositionDismissed,
			DecidedBy:    intPtr(7),
			DecidedAt:    &now,
			Note:         "predefined note",
		},
	}
	before := make([]QualityIssue, len(fresh))
	copy(before, fresh)

	for _, existing := range [][]QualityIssue{nil, {}} {
		got := ReconcileIssues(fresh, existing)
		if !reflect.DeepEqual(got, fresh) {
			t.Errorf("empty existing (existing=%#v) should return fresh unchanged\n got: %#v\nwant: %#v", existing, got, fresh)
		}
	}
	if !reflect.DeepEqual(fresh, before) {
		t.Errorf("fresh must not be modified\n got: %#v\nwant: %#v", fresh, before)
	}
}

// TestReconcileIssues_SameFingerprintInheritsDisposition 验证同指纹时继承旧裁决
// （disposition/decided_by/decided_at/note），而 severity/message/span 取 fresh 新值。
func TestReconcileIssues_SameFingerprintInheritsDisposition(t *testing.T) {
	userID := 42
	now := time.Now().UTC()
	start, end := 3, 6

	fresh := []QualityIssue{
		{
			SegmentIndex: 2,
			Severity:     SeverityError,
			Code:         "calque",
			Message:      "新的 message",
			Span:         &Span{MatchedText: "foo", TargetStart: &start, TargetEnd: &end},
		},
	}
	existing := []QualityIssue{
		{
			Code:        "calque",
			Span:        &Span{MatchedText: "foo"},
			Disposition: DispositionDismissed,
			DecidedBy:   &userID,
			DecidedAt:   &now,
			Note:        "人工确认非问题",
		},
	}

	got := ReconcileIssues(fresh, existing)
	if len(got) != 1 {
		t.Fatalf("len=%d want 1: %#v", len(got), got)
	}
	out := got[0]

	// 继承字段
	if out.Disposition != DispositionDismissed {
		t.Errorf("disposition = %q, want %q", out.Disposition, DispositionDismissed)
	}
	if out.DecidedBy == nil || *out.DecidedBy != userID {
		t.Errorf("decided_by = %v, want %d", out.DecidedBy, userID)
	}
	if out.DecidedAt == nil || !out.DecidedAt.Equal(now) {
		t.Errorf("decided_at = %v, want %v", out.DecidedAt, now)
	}
	if out.Note != "人工确认非问题" {
		t.Errorf("note = %q, want %q", out.Note, "人工确认非问题")
	}

	// fresh 新值保留
	if out.SegmentIndex != 2 {
		t.Errorf("segment_index = %d, want 2", out.SegmentIndex)
	}
	if out.Severity != SeverityError {
		t.Errorf("severity = %q, want %q", out.Severity, SeverityError)
	}
	if out.Message != "新的 message" {
		t.Errorf("message = %q, want %q", out.Message, "新的 message")
	}
	if out.Span == nil || out.Span.MatchedText != "foo" ||
		out.Span.TargetStart == nil || *out.Span.TargetStart != start ||
		out.Span.TargetEnd == nil || *out.Span.TargetEnd != end {
		t.Errorf("span 应为 fresh 的新值（含新偏移）: %#v", out.Span)
	}
}

// TestReconcileIssues_DisappearedFingerprintCleared 验证 existing 中的已裁决指纹
// 若在 fresh 中消失，则自然清除（结果中不出现）。
func TestReconcileIssues_DisappearedFingerprintCleared(t *testing.T) {
	userID := 7
	now := time.Now().UTC()
	existing := []QualityIssue{
		{
			Code:        "length_ratio",
			Span:        &Span{MatchedText: "很长很长"},
			Disposition: DispositionDismissed,
			DecidedBy:   &userID,
			DecidedAt:   &now,
			Note:        "已裁决",
		},
	}
	fresh := []QualityIssue{
		{Code: "untranslated", Span: &Span{MatchedText: "Hello"}},
	}

	got := ReconcileIssues(fresh, existing)
	if len(got) != 1 {
		t.Fatalf("len=%d want 1（消失的指纹应被清除）: %#v", len(got), got)
	}
	if got[0].Code != "untranslated" {
		t.Errorf("code = %q, want %q", got[0].Code, "untranslated")
	}
	if got[0].Disposition != DispositionPending {
		t.Errorf("剩余 issue 应为 pending，got %q", got[0].Disposition)
	}
}

// TestReconcileIssues_NewFingerprintStaysPending 验证 fresh 中 existing 没有的
// 新指纹保持 pending（零值 disposition），不继承任何裁决字段。
func TestReconcileIssues_NewFingerprintStaysPending(t *testing.T) {
	userID := 9
	now := time.Now().UTC()
	existing := []QualityIssue{
		{
			Code:        "duplicate",
			Span:        &Span{MatchedText: "x"},
			Disposition: DispositionDismissed,
			DecidedBy:   &userID,
			DecidedAt:   &now,
			Note:        "n",
		},
	}
	fresh := []QualityIssue{
		{Code: "omission", Span: &Span{MatchedText: "y"}, Severity: SeverityWarning},
	}

	got := ReconcileIssues(fresh, existing)
	if len(got) != 1 {
		t.Fatalf("len=%d want 1: %#v", len(got), got)
	}
	out := got[0]
	if out.Code != "omission" {
		t.Errorf("code = %q, want %q", out.Code, "omission")
	}
	if out.Disposition != DispositionPending {
		t.Errorf("新指纹应保持 pending，got %q", out.Disposition)
	}
	if out.DecidedBy != nil || out.DecidedAt != nil || out.Note != "" {
		t.Errorf("新指纹不应继承任何裁决字段: %#v", out)
	}
}

// TestReconcileIssues_MultipleInstancesSameCode 验证同 code 不同 matched_text
// 的多条 issue 各自按完整指纹继承裁决，而非仅按 code 匹配。
func TestReconcileIssues_MultipleInstancesSameCode(t *testing.T) {
	user1, user2 := 1, 2
	now1 := time.Now().UTC()
	now2 := now1.Add(time.Hour)

	existing := []QualityIssue{
		{
			Code:        "calque",
			Span:        &Span{MatchedText: "foo"},
			Disposition: DispositionDismissed,
			DecidedBy:   &user1,
			DecidedAt:   &now1,
			Note:        "foo 备注",
		},
		{
			Code:        "calque",
			Span:        &Span{MatchedText: "bar"},
			Disposition: DispositionDismissed,
			DecidedBy:   &user2,
			DecidedAt:   &now2,
			Note:        "bar 备注",
		},
	}
	fresh := []QualityIssue{
		{Code: "calque", Span: &Span{MatchedText: "foo"}, Severity: SeverityWarning, Message: "foo 新消息"},
		{Code: "calque", Span: &Span{MatchedText: "bar"}, Severity: SeverityError, Message: "bar 新消息"},
	}

	got := ReconcileIssues(fresh, existing)
	if len(got) != 2 {
		t.Fatalf("len=%d want 2: %#v", len(got), got)
	}

	// foo 只继承 foo 的裁决（若仅按 code 匹配会错误继承到 bar 的裁决字段）
	outFoo := got[0]
	if outFoo.Disposition != DispositionDismissed || outFoo.DecidedBy == nil || *outFoo.DecidedBy != user1 ||
		!outFoo.DecidedAt.Equal(now1) || outFoo.Note != "foo 备注" {
		t.Errorf("foo 应按完整指纹继承 own 裁决: %#v", outFoo)
	}
	if outFoo.Severity != SeverityWarning || outFoo.Message != "foo 新消息" {
		t.Errorf("foo 的新字段应保留: %#v", outFoo)
	}

	// bar 只继承 bar 的裁决
	outBar := got[1]
	if outBar.Disposition != DispositionDismissed || outBar.DecidedBy == nil || *outBar.DecidedBy != user2 ||
		!outBar.DecidedAt.Equal(now2) || outBar.Note != "bar 备注" {
		t.Errorf("bar 应按完整指纹继承 own 裁决: %#v", outBar)
	}
	if outBar.Severity != SeverityError || outBar.Message != "bar 新消息" {
		t.Errorf("bar 的新字段应保留: %#v", outBar)
	}
}

// TestReconcileIssues_PendingNotInherited 验证 existing 中 pending（零值）的 issue
// 不会被继承——只有非 pending（如 dismissed）才继承裁决。
func TestReconcileIssues_PendingNotInherited(t *testing.T) {
	userID := 5
	now := time.Now().UTC()
	existing := []QualityIssue{
		{Code: "grammar", Span: &Span{MatchedText: "a"}, Disposition: DispositionPending},
		{
			Code:        "grammar",
			Span:        &Span{MatchedText: "b"},
			Disposition: DispositionDismissed,
			DecidedBy:   &userID,
			DecidedAt:   &now,
			Note:        "b 备注",
		},
	}
	fresh := []QualityIssue{
		{Code: "grammar", Span: &Span{MatchedText: "a"}, Message: "a 新消息"},
		{Code: "grammar", Span: &Span{MatchedText: "b"}, Message: "b 新消息"},
	}

	got := ReconcileIssues(fresh, existing)
	if len(got) != 2 {
		t.Fatalf("len=%d want 2: %#v", len(got), got)
	}

	// a：existing 中 pending，不继承，保持 pending
	outA := got[0]
	if outA.Disposition != DispositionPending {
		t.Errorf("pending 不应被继承，got %q", outA.Disposition)
	}
	if outA.DecidedBy != nil || outA.DecidedAt != nil || outA.Note != "" {
		t.Errorf("pending 的裁决字段应保持零值: %#v", outA)
	}
	if outA.Message != "a 新消息" {
		t.Errorf("a 的新字段应保留: %#v", outA)
	}

	// b：existing 中 dismissed，应被继承
	outB := got[1]
	if outB.Disposition != DispositionDismissed {
		t.Errorf("dismissed 应被继承，got %q", outB.Disposition)
	}
	if outB.DecidedBy == nil || *outB.DecidedBy != userID ||
		!outB.DecidedAt.Equal(now) || outB.Note != "b 备注" {
		t.Errorf("dismissed 的裁决字段应被继承: %#v", outB)
	}
}

// TestReconcileIssues_NilSpanEmptyMatchedText 验证无 span（matched_text 为空）
// 时指纹为 "code:"，对账仍正常按指纹继承。
func TestReconcileIssues_NilSpanEmptyMatchedText(t *testing.T) {
	t.Run("nil span", func(t *testing.T) {
		userID := 3
		now := time.Now().UTC()
		existing := []QualityIssue{
			{
				Code:        "addition",
				Disposition: DispositionDismissed,
				DecidedBy:   &userID,
				DecidedAt:   &now,
				Note:        "无跨度裁决",
			},
		}
		fresh := []QualityIssue{
			{Code: "addition", Severity: SeverityError, Message: "新增内容"},
		}

		got := ReconcileIssues(fresh, existing)
		if len(got) != 1 {
			t.Fatalf("len=%d want 1: %#v", len(got), got)
		}
		out := got[0]
		if out.Disposition != DispositionDismissed || out.DecidedBy == nil || *out.DecidedBy != userID ||
			!out.DecidedAt.Equal(now) || out.Note != "无跨度裁决" {
			t.Errorf("nil span 同指纹（code:）应继承裁决: %#v", out)
		}
		if out.Severity != SeverityError || out.Message != "新增内容" {
			t.Errorf("fresh 新字段应保留: %#v", out)
		}
		if out.Span != nil {
			t.Errorf("span 应保持 nil: %#v", out.Span)
		}
	})

	t.Run("span with empty matched text", func(t *testing.T) {
		userID := 4
		now := time.Now().UTC()
		existing := []QualityIssue{
			{
				Code:        "omission",
				Span:        &Span{MatchedText: ""},
				Disposition: DispositionDismissed,
				DecidedBy:   &userID,
				DecidedAt:   &now,
				Note:        "空文本裁决",
			},
		}
		fresh := []QualityIssue{
			{Code: "omission", Span: &Span{MatchedText: ""}, Message: "缺漏"},
		}

		got := ReconcileIssues(fresh, existing)
		if len(got) != 1 {
			t.Fatalf("len=%d want 1: %#v", len(got), got)
		}
		out := got[0]
		if out.Disposition != DispositionDismissed || out.DecidedBy == nil || *out.DecidedBy != userID ||
			!out.DecidedAt.Equal(now) || out.Note != "空文本裁决" {
			t.Errorf("空 matched_text 同指纹（code:）应继承裁决: %#v", out)
		}
		if out.Message != "缺漏" {
			t.Errorf("fresh 新字段应保留: %#v", out)
		}
	})
}
