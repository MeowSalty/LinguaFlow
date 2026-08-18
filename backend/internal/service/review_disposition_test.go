package service

import (
	"context"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

// dispositionTestEnv 封装 SetIssueDisposition 测试所需的完整链路：
// 用户（项目 owner，通过 requireProjectAccess 的 owner 分支）、项目、资源。
// 复用 service 包共享的 testClient / createTestUser / createTestProject /
// createTestResource / createTestSegmentWithTarget 测试辅助函数。
type dispositionTestEnv struct {
	client  *ent.Client
	svc     *ReviewService
	ctx     context.Context
	user    *ent.User
	project *ent.Project
	res     *ent.Resource
}

func setupDispositionEnv(t *testing.T, suffix string) *dispositionTestEnv {
	t.Helper()
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "disp-"+suffix+"-user")
	project := createTestProject(t, client, "disp-"+suffix+"-proj", user.ID)
	res := createTestResource(t, client, project.ID, "chapters/disp-"+suffix+".txt")
	return &dispositionTestEnv{
		client:  client,
		svc:     NewReviewService(client, NewProjectService(client, nil)),
		ctx:     ctx,
		user:    user,
		project: project,
		res:     res,
	}
}

// findIssueByCode 按 code 返回 issues 中第一条匹配项的指针。
func findIssueByCode(issues []qa.QualityIssue, code string) *qa.QualityIssue {
	for i := range issues {
		if issues[i].Code == code {
			return &issues[i]
		}
	}
	return nil
}

// TestSetIssueDisposition_Dismissed 验证对单条 issue 下达 dismissed 裁决：
//   - 目标 issue（按 (code, matched_text) 指纹定位）获得 Disposition=dismissed、
//     DecidedBy=actor、DecidedAt 非空、Note 落地；
//   - 同段落其他 issue 的裁决字段保持零值（不受影响）。
func TestSetIssueDisposition_Dismissed(t *testing.T) {
	env := setupDispositionEnv(t, "dismiss")
	issues := []qa.QualityIssue{
		{SegmentIndex: 0, Severity: qa.SeverityWarning, Code: qa.CheckSourceResidual, Message: "residual", Span: &qa.Span{MatchedText: "residual"}},
		{SegmentIndex: 0, Severity: qa.SeverityError, Code: qa.IssueCodeCalque, Message: "calque", Span: &qa.Span{MatchedText: "calque"}},
	}
	seg := createTestSegmentWithTarget(t, env.client, env.res.ID, 0, "Hello", "你好", issues)

	updated, err := env.svc.SetIssueDisposition(env.ctx, env.user.ID, env.project.ID, env.res.ID, seg.ID,
		qa.CheckSourceResidual, "residual", string(qa.DispositionDismissed), "专有名词")
	if err != nil {
		t.Fatalf("SetIssueDisposition(dismissed): %v", err)
	}

	target := findIssueByCode(updated.QualityIssues, qa.CheckSourceResidual)
	if target == nil {
		t.Fatalf("source_residual issue missing after dismissal: %#v", updated.QualityIssues)
	}
	if target.Disposition != qa.DispositionDismissed {
		t.Fatalf("disposition=%q want %q", target.Disposition, qa.DispositionDismissed)
	}
	if target.DecidedBy == nil || *target.DecidedBy != env.user.ID {
		t.Fatalf("decided_by=%v want %d", target.DecidedBy, env.user.ID)
	}
	if target.DecidedAt == nil {
		t.Fatalf("decided_at=nil want non-nil")
	}
	if target.Note != "专有名词" {
		t.Fatalf("note=%q want %q", target.Note, "专有名词")
	}

	// 未被裁决的 issue 不受影响：Disposition/DecidedBy/DecidedAt/Note 保持零值。
	other := findIssueByCode(updated.QualityIssues, qa.IssueCodeCalque)
	if other == nil {
		t.Fatalf("calque issue missing: %#v", updated.QualityIssues)
	}
	if other.Disposition != qa.DispositionPending || other.DecidedBy != nil || other.DecidedAt != nil || other.Note != "" {
		t.Fatalf("untouched issue was mutated: %#v", *other)
	}
}

// TestSetIssueDisposition_PendingRevokes 验证 disposition=pending 撤销既有裁决：
// 已 dismissed 的 issue 的 Disposition/DecidedBy/DecidedAt/Note 全部重置为零值。
func TestSetIssueDisposition_PendingRevokes(t *testing.T) {
	env := setupDispositionEnv(t, "revoke")
	decidedAt := time.Now().UTC()
	issues := []qa.QualityIssue{
		{
			SegmentIndex: 0,
			Severity:     qa.SeverityWarning,
			Code:         qa.CheckSourceResidual,
			Message:      "residual",
			Span:         &qa.Span{MatchedText: "residual"},
			Disposition:  qa.DispositionDismissed,
			DecidedBy:    &env.user.ID,
			DecidedAt:    &decidedAt,
			Note:         "专有名词",
		},
	}
	seg := createTestSegmentWithTarget(t, env.client, env.res.ID, 0, "Hello", "你好", issues)

	updated, err := env.svc.SetIssueDisposition(env.ctx, env.user.ID, env.project.ID, env.res.ID, seg.ID,
		qa.CheckSourceResidual, "residual", string(qa.DispositionPending), "")
	if err != nil {
		t.Fatalf("SetIssueDisposition(pending): %v", err)
	}

	target := findIssueByCode(updated.QualityIssues, qa.CheckSourceResidual)
	if target == nil {
		t.Fatalf("source_residual issue missing: %#v", updated.QualityIssues)
	}
	if target.Disposition != qa.DispositionPending {
		t.Fatalf("disposition=%q want %q (zero value)", target.Disposition, qa.DispositionPending)
	}
	if target.DecidedBy != nil || target.DecidedAt != nil || target.Note != "" {
		t.Fatalf("disposition fields not reset after revoke: %#v", *target)
	}
}

// TestSetIssueDisposition_NotFound 验证 (code, matched_text) 未命中任何 issue 时
// 返回 ErrIssueNotFound，且不写入任何变更。
func TestSetIssueDisposition_NotFound(t *testing.T) {
	env := setupDispositionEnv(t, "nf")
	seg := createTestSegmentWithTarget(t, env.client, env.res.ID, 0, "Hello", "你好", []qa.QualityIssue{
		{SegmentIndex: 0, Severity: qa.SeverityWarning, Code: qa.CheckSourceResidual, Message: "residual", Span: &qa.Span{MatchedText: "residual"}},
	})

	_, err := env.svc.SetIssueDisposition(env.ctx, env.user.ID, env.project.ID, env.res.ID, seg.ID,
		qa.CheckUntranslated, "不存在的文本", string(qa.DispositionDismissed), "")
	if err != ErrIssueNotFound {
		t.Fatalf("err=%v want ErrIssueNotFound", err)
	}

	after, err := env.client.Segment.Get(env.ctx, seg.ID)
	if err != nil {
		t.Fatalf("reload segment: %v", err)
	}
	if len(after.QualityIssues) != 1 || after.QualityIssues[0].Disposition != qa.DispositionPending {
		t.Fatalf("quality_issues changed on NotFound: %#v", after.QualityIssues)
	}
}

// TestSetIssueDisposition_CASConflict 覆盖 SetIssueDisposition 轻量 CAS
// （TargetText 非空时附加 TargetTextEQ 条件）的可测分支。
//
// 说明：CAS 的比对值取自 authorizeSegment 在方法内刚读取的 row.TargetText，
// 读取与 UPDATE 在同一调用内完成，公众 API 无法注入"读取后译文被改"的陈旧快照；
// 真正的冲突只可能出现在内部 read 与 Exec 之间的并发窗口，内存 SQLite 单线程
// 测试无法确定性制造该窗口。故这里验证 CAS 语义的三个稳定分支：
//   - target 为空：CAS 条件被跳过，正常落库；
//   - target 非空且未变化：CAS 命中，正常落库；
//   - 两次调用之间外部改译文：服务重读新译文，CAS 以新值为准仍命中，
//     证明裁决不依赖陈旧快照、不会因并发改译文而静默丢弃。
func TestSetIssueDisposition_CASConflict(t *testing.T) {
	t.Run("empty_target_skips_cas", func(t *testing.T) {
		env := setupDispositionEnv(t, "cas-empty")
		// createTestSegment 不设 target_text：row.TargetText 为 nil，CAS 条件跳过。
		seg := createTestSegment(t, env.client, env.res.ID, 0, "Hello", []qa.QualityIssue{
			{SegmentIndex: 0, Severity: qa.SeverityWarning, Code: qa.CheckSourceResidual, Message: "residual", Span: &qa.Span{MatchedText: "residual"}},
		})
		updated, err := env.svc.SetIssueDisposition(env.ctx, env.user.ID, env.project.ID, env.res.ID, seg.ID,
			qa.CheckSourceResidual, "residual", string(qa.DispositionDismissed), "")
		if err != nil {
			t.Fatalf("SetIssueDisposition on empty target: %v", err)
		}
		if target := findIssueByCode(updated.QualityIssues, qa.CheckSourceResidual); target == nil || target.Disposition != qa.DispositionDismissed {
			t.Fatalf("disposition not applied with empty target: %#v", updated.QualityIssues)
		}
	})

	t.Run("fresh_target_matches_cas", func(t *testing.T) {
		env := setupDispositionEnv(t, "cas-match")
		seg := createTestSegmentWithTarget(t, env.client, env.res.ID, 0, "Hello", "你好", []qa.QualityIssue{
			{SegmentIndex: 0, Severity: qa.SeverityWarning, Code: qa.CheckSourceResidual, Message: "residual", Span: &qa.Span{MatchedText: "residual"}},
		})
		updated, err := env.svc.SetIssueDisposition(env.ctx, env.user.ID, env.project.ID, env.res.ID, seg.ID,
			qa.CheckSourceResidual, "residual", string(qa.DispositionDismissed), "")
		if err != nil {
			t.Fatalf("SetIssueDisposition: %v", err)
		}
		if target := findIssueByCode(updated.QualityIssues, qa.CheckSourceResidual); target == nil || target.DecidedAt == nil {
			t.Fatalf("CAS mismatch on unmodified target: %#v", updated.QualityIssues)
		}
	})

	t.Run("target_edited_between_calls", func(t *testing.T) {
		env := setupDispositionEnv(t, "cas-edit")
		seg := createTestSegmentWithTarget(t, env.client, env.res.ID, 0, "Hello", "你好", []qa.QualityIssue{
			{SegmentIndex: 0, Severity: qa.SeverityWarning, Code: qa.CheckSourceResidual, Message: "residual", Span: &qa.Span{MatchedText: "residual"}},
		})
		if _, err := env.svc.SetIssueDisposition(env.ctx, env.user.ID, env.project.ID, env.res.ID, seg.ID,
			qa.CheckSourceResidual, "residual", string(qa.DispositionDismissed), ""); err != nil {
			t.Fatalf("first disposition: %v", err)
		}
		// 模拟两次调用之间外部改译文：内部 authorizeSegment 会重读新译文，
		// CAS 以新值为准，仍应命中并成功撤销。
		if _, err := env.client.Segment.UpdateOneID(seg.ID).SetTargetText("你好呀").Save(env.ctx); err != nil {
			t.Fatalf("external target edit: %v", err)
		}
		updated, err := env.svc.SetIssueDisposition(env.ctx, env.user.ID, env.project.ID, env.res.ID, seg.ID,
			qa.CheckSourceResidual, "residual", string(qa.DispositionPending), "")
		if err != nil {
			t.Fatalf("second disposition after target edit: %v", err)
		}
		target := findIssueByCode(updated.QualityIssues, qa.CheckSourceResidual)
		if target == nil || target.Disposition != qa.DispositionPending || target.DecidedBy != nil {
			t.Fatalf("revoke not applied after external target edit: %#v", updated.QualityIssues)
		}
	})
}
