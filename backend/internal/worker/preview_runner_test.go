package worker

import (
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

// TestTranslateStatusAllowedMatrix 覆盖 SegmentFilter × status 全矩阵，
// 锁定 applyTranslateSegmentFilter 与 PreviewRunner 后续 translate 轮共用的判断语义。
func TestTranslateStatusAllowedMatrix(t *testing.T) {
	statuses := []string{
		string(service.SegmentStatusPending),
		string(service.SegmentStatusTranslated),
		string(service.SegmentStatusEdited),
		string(service.SegmentStatusApproved),
		string(service.SegmentStatusRejected),
	}

	type tc struct {
		filter string // StatusFilter；"" 表示 filter==nil
		status string
		want   bool
	}
	cases := []tc{
		// pending_only（默认）：仅 pending/rejected
		{"pending_only", string(service.SegmentStatusPending), true},
		{"pending_only", string(service.SegmentStatusRejected), true},
		{"pending_only", string(service.SegmentStatusTranslated), false},
		{"pending_only", string(service.SegmentStatusEdited), false},
		{"pending_only", string(service.SegmentStatusApproved), false},

		// skip_approved：排除 approved
		{"skip_approved", string(service.SegmentStatusPending), true},
		{"skip_approved", string(service.SegmentStatusRejected), true},
		{"skip_approved", string(service.SegmentStatusTranslated), true},
		{"skip_approved", string(service.SegmentStatusEdited), true},
		{"skip_approved", string(service.SegmentStatusApproved), false},

		// all：始终放行
		{"all", string(service.SegmentStatusPending), true},
		{"all", string(service.SegmentStatusTranslated), true},
		{"all", string(service.SegmentStatusEdited), true},
		{"all", string(service.SegmentStatusApproved), true},
		{"all", string(service.SegmentStatusRejected), true},

		// filter==nil 等价 pending_only
		{"", string(service.SegmentStatusPending), true},
		{"", string(service.SegmentStatusRejected), true},
		{"", string(service.SegmentStatusTranslated), false},
		{"", string(service.SegmentStatusApproved), false},
		{"", string(service.SegmentStatusEdited), false},
	}

	for _, c := range cases {
		var filter *service.SegmentFilterSnapshot
		if c.filter != "" {
			filter = &service.SegmentFilterSnapshot{StatusFilter: c.filter}
		}
		got := translateStatusAllowed(filter, c.status)
		if got != c.want {
			t.Errorf("filter=%q status=%q: got %v want %v", c.filter, c.status, got, c.want)
		}
	}

	// 兜底：确保没有任何遗漏的 status 被错误放行 through default/pending_only 分支。
	seen := map[string]bool{}
	for _, c := range cases {
		seen[c.status] = true
	}
	for _, s := range statuses {
		if !seen[s] {
			t.Errorf("matrix missing status %q", s)
		}
	}
}

// TestApplyTranslateSegmentFilterEquivalence 验证重构后 applyTranslateSegmentFilter
// 的集合行为与原内联 switch 等价。
func TestApplyTranslateSegmentFilterEquivalence(t *testing.T) {
	rows := []*ent.Segment{
		testSegmentRowWithStatus(segment.StatusPending),
		testSegmentRowWithStatus(segment.StatusRejected),
		testSegmentRowWithStatus(segment.StatusTranslated),
		testSegmentRowWithStatus(segment.StatusEdited),
		testSegmentRowWithStatus(segment.StatusApproved),
	}

	tests := []struct {
		name    string
		filter  *service.SegmentFilterSnapshot
		wantSet map[segment.Status]bool
	}{
		{
			name:   "nil filter defaults to pending_only",
			filter: nil,
			wantSet: map[segment.Status]bool{
				segment.StatusPending:  true,
				segment.StatusRejected: true,
			},
		},
		{
			name:   "pending_only",
			filter: &service.SegmentFilterSnapshot{StatusFilter: "pending_only"},
			wantSet: map[segment.Status]bool{
				segment.StatusPending:  true,
				segment.StatusRejected: true,
			},
		},
		{
			name:   "skip_approved",
			filter: &service.SegmentFilterSnapshot{StatusFilter: "skip_approved"},
			wantSet: map[segment.Status]bool{
				segment.StatusPending:    true,
				segment.StatusRejected:   true,
				segment.StatusTranslated: true,
				segment.StatusEdited:     true,
			},
		},
		{
			name:   "all",
			filter: &service.SegmentFilterSnapshot{StatusFilter: "all"},
			wantSet: map[segment.Status]bool{
				segment.StatusPending:    true,
				segment.StatusRejected:   true,
				segment.StatusTranslated: true,
				segment.StatusEdited:     true,
				segment.StatusApproved:   true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := applyTranslateSegmentFilter(rows, tt.filter)
			if len(got) != len(tt.wantSet) {
				t.Fatalf("%s: got %d rows %v, want %d", tt.name, len(got), statusesOf(got), len(tt.wantSet))
			}
			for _, seg := range got {
				if !tt.wantSet[seg.Status] {
					t.Errorf("%s: unexpected status %s in result", tt.name, seg.Status)
				}
			}
		})
	}
}

func statusesOf(segs []*ent.Segment) []segment.Status {
	out := make([]segment.Status, len(segs))
	for i, s := range segs {
		out[i] = s.Status
	}
	return out
}

// testSegmentRowWithStatus 构造一个仅 Status 不同的最小 ent.Segment。
// 仅过滤逻辑依赖 Status 字段，其余字段保持零值。
func testSegmentRowWithStatus(status segment.Status) *ent.Segment {
	seg := &ent.Segment{}
	seg.Status = status
	return seg
}
