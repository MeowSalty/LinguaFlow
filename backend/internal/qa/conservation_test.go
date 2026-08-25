package qa

import (
	"fmt"
	"testing"
)

// TestRubyTagLossIssues 表格覆盖注音全丢失检测的判定边界：
// 基线对比、字面 "<ruby>" 计数（变形形态视为丢失）与 SegmentIndex 透传。
func TestRubyTagLossIssues(t *testing.T) {
	cases := []struct {
		name         string
		segmentIndex int
		prev         string
		next         string
		wantNil      bool
		wantCount    int // prev 中 <ruby> 出现次数，进入 message
	}{
		{
			name:         "prev_multiple_next_none",
			segmentIndex: 7,
			prev:         `<ruby>漢<rt>かん</rt></ruby>字<ruby>語<rt>ご</rt></ruby>`,
			next:         "汉字",
			wantNil:      false,
			wantCount:    2,
		},
		{
			name:         "prev_none",
			segmentIndex: 0,
			prev:         "纯文本译文",
			next:         "编辑后的纯文本",
			wantNil:      true,
		},
		{
			name:         "next_still_has_ruby",
			segmentIndex: 3,
			prev:         `<ruby>漢<rt>かん</rt></ruby>`,
			next:         `<ruby>漢<rt>かん</rt></ruby>改`,
			wantNil:      true,
		},
		{
			name:         "prev_has_next_deformed_case_counts_as_loss",
			segmentIndex: 1,
			prev:         `<ruby>漢<rt>かん</rt></ruby>`,
			next:         `<RUBY>漢<RT>かん</RT></RUBY>`,
			wantNil:      false,
			wantCount:    1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RubyTagLossIssues(tc.segmentIndex, tc.prev, tc.next)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("RubyTagLossIssues(%d, %q, %q) = %#v, want nil", tc.segmentIndex, tc.prev, tc.next, got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("RubyTagLossIssues(%d, %q, %q) len=%d, want 1 issue", tc.segmentIndex, tc.prev, tc.next, len(got))
			}
			iss := got[0]
			if iss.Code != CodeRubyTagLoss {
				t.Errorf("code=%q want %q", iss.Code, CodeRubyTagLoss)
			}
			if iss.Severity != SeverityWarning {
				t.Errorf("severity=%q want %q", iss.Severity, SeverityWarning)
			}
			if iss.SegmentIndex != tc.segmentIndex {
				t.Errorf("segment_index=%d want %d", iss.SegmentIndex, tc.segmentIndex)
			}
			if iss.Span != nil {
				t.Errorf("span must be nil (fingerprint stays code-only), got %#v", iss.Span)
			}
			wantMsg := fmt.Sprintf("译文注音全部丢失：编辑前 %d 条", tc.wantCount)
			if iss.Message != wantMsg {
				t.Errorf("message=%q want %q", iss.Message, wantMsg)
			}
			if !iss.IsPending() {
				t.Errorf("fresh issue should be pending, got disposition=%q", iss.Disposition)
			}
		})
	}
}
