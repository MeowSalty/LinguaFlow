package service

import (
	"context"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/activitylog"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/previewtoken"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

// newApplyFixture 构建围绕内存 DB 的真实 PreviewService（ApplyPreview 不使用
// runner，传 nil），供 apply 链路单测。
func newApplyFixture(t *testing.T) (*PreviewService, *ent.Client, int, int) {
	t.Helper()
	client := testClient(t)
	users := NewUserService(client, NewAuthService(client, AuthConfig{}, NewAdminService(client)))
	projects := NewProjectService(client, users)
	backends := NewBackendService(client, users, nil)
	profiles := NewExecutionProfileService(client, users)
	executionPlans := NewExecutionPlanService(client, users, profiles)
	promptTemplates := NewTranslationPromptTemplateService(client)
	bootstrapTemplates := NewBootstrapPromptTemplateService(client)
	jobs := NewJobService(client, projects, executionPlans, backends, promptTemplates, bootstrapTemplates, profiles, nil, nil)
	audit := NewAuditService(client, users, projects)
	svc := NewPreviewService(discardLogger(), client, projects, jobs, audit, nil, "test-secret", 15*time.Minute, 2, 5*time.Minute)

	u, err := client.User.Create().
		SetUsername("alice").
		SetPasswordHash("$2a$10$dummyhashvaluehere").
		SetEmail("alice@test.com").
		Save(context.Background())
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	projectID := seedProject(t, client, u.ID)
	return svc, client, u.ID, projectID
}

// encodeApplyClaims 用与 PreviewService 相同的密钥签发测试令牌，
// 绕过预览执行直接构造 apply 输入。
func encodeApplyClaims(t *testing.T, claims previewtoken.ApplyClaims) string {
	t.Helper()
	token, _, err := previewtoken.NewCodec("test-secret", time.Minute).Encode(claims)
	if err != nil {
		t.Fatalf("encode token: %v", err)
	}
	return token
}

// revisionApplyClaims 构造基线匹配 seg 的修订令牌 claims。
func revisionApplyClaims(seg *ent.Segment, resourceID, projectID, userID int, resolvedCodes []string, qc previewtoken.QAConfigClaims) previewtoken.ApplyClaims {
	return previewtoken.ApplyClaims{
		ActorUserID:    userID,
		ProjectID:      projectID,
		ResourceID:     resourceID,
		SegmentID:      seg.ID,
		Kind:           previewtoken.KindRevision,
		SourceHash:     sha256Hex(seg.SourceText),
		PreviewSource:  seg.SourceText,
		TargetHash:     sha256Hex("新译文"),
		BaselineSource: seg.SourceText,
		BaselineTarget: seg.TargetText,
		BaselineStatus: string(segment.StatusTranslated),
		QAConfig:       qc,
		ResolvedCodes:  resolvedCodes,
	}
}

// TestApplyPreview_RevisionRewrite_QADisabled_RemovesOnlyResolvedPending 是契约
// 翻转点：修订令牌 + 用户改写文本 + QA 未启用时，落库 issue 为既有集合剔除
// ResolvedCodes 命中的 pending（范围外与 dismissed 保留）——旧行为是整段清空。
func TestApplyPreview_RevisionRewrite_QADisabled_RemovesOnlyResolvedPending(t *testing.T) {
	applySvc, client, userID, projectID := newApplyFixture(t)
	res := createTestResource(t, client, projectID, "a.txt")
	seg := seedRevisionSegment(t, client, res.ID, 0, "src", "旧译文", segment.StatusTranslated, []qa.QualityIssue{
		{Code: qa.IssueCodeCalque, Message: "借译", Severity: qa.SeverityWarning, Disposition: qa.DispositionPending},           // 目标内 pending → 移除
		{Code: qa.IssueCodeGrammar, Message: "语法", Severity: qa.SeverityWarning, Disposition: qa.DispositionPending},          // 范围外语义 → 保留
		{Code: qa.IssueCodeMistranslation, Message: "误译", Severity: qa.SeverityWarning, Disposition: qa.DispositionDismissed}, // dismissed → 保留
		{Code: qa.CheckLengthRatio, Message: "长度比", Severity: qa.SeverityError, Disposition: qa.DispositionPending},           // 范围外确定性 → 保留
	})

	token := encodeApplyClaims(t, revisionApplyClaims(seg, res.ID, projectID, userID, []string{qa.IssueCodeCalque}, previewtoken.QAConfigClaims{Enabled: false}))

	applied, err := applySvc.ApplyPreview(context.Background(), userID, projectID, res.ID, seg.ID, token, "用户改写的译文")
	if err != nil {
		t.Fatalf("ApplyPreview: %v", err)
	}
	if len(applied.QualityIssues) != 3 {
		t.Fatalf("applied issues = %+v want 3（范围外与 dismissed 保留）", applied.QualityIssues)
	}
	got := map[string]qa.QualityIssue{}
	for _, iss := range applied.QualityIssues {
		got[iss.Code] = iss
	}
	if _, ok := got[qa.IssueCodeCalque]; ok {
		t.Fatalf("声明已修复的 pending calque 应被移除: %+v", applied.QualityIssues)
	}
	if got[qa.IssueCodeGrammar].Disposition != qa.DispositionPending ||
		got[qa.IssueCodeMistranslation].Disposition != qa.DispositionDismissed ||
		got[qa.CheckLengthRatio].Disposition != qa.DispositionPending {
		t.Fatalf("范围外与 dismissed issue 应原样保留: %+v", applied.QualityIssues)
	}

	// 修订令牌审计事件携带 resolved_codes。
	row, err := client.ActivityLog.Query().
		Where(activitylog.ActionEQ("resource.segment.revision_preview.apply")).
		Only(context.Background())
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	codes, ok := row.Metadata["resolved_codes"].([]any)
	if !ok || len(codes) != 1 || codes[0] != qa.IssueCodeCalque {
		t.Fatalf("audit resolved_codes = %#v want [calque]", row.Metadata["resolved_codes"])
	}
}

// TestApplyPreview_RevisionRewrite_QAEnabled_ReviseFinalCombination 验证改写 +
// QA 启用时按 ReviseFinalIssues 组合：fresh 确定性重算进入结果，范围外语义
// pending 与 dismissed 保留，targeted pending 移除——而非旧的整段替换为 fresh。
func TestApplyPreview_RevisionRewrite_QAEnabled_ReviseFinalCombination(t *testing.T) {
	applySvc, client, userID, projectID := newApplyFixture(t)
	res := createTestResource(t, client, projectID, "a.txt")
	seg := seedRevisionSegment(t, client, res.ID, 0, "hello", "旧译文", segment.StatusTranslated, []qa.QualityIssue{
		{Code: qa.IssueCodeCalque, Message: "借译", Severity: qa.SeverityWarning, Disposition: qa.DispositionPending},
		{Code: qa.IssueCodeGrammar, Message: "语法", Severity: qa.SeverityWarning, Disposition: qa.DispositionPending},
	})

	token := encodeApplyClaims(t, revisionApplyClaims(seg, res.ID, projectID, userID, []string{qa.IssueCodeCalque}, previewtoken.QAConfigClaims{
		Enabled:    true,
		Checks:     []string{qa.CheckUntranslated},
		SourceLang: "en",
		TargetLang: "zh",
	}))

	// 用户改写为与原文相同：untranslated checker 必然命中，fresh 结果可预测。
	applied, err := applySvc.ApplyPreview(context.Background(), userID, projectID, res.ID, seg.ID, token, "hello")
	if err != nil {
		t.Fatalf("ApplyPreview: %v", err)
	}
	if len(applied.QualityIssues) != 2 {
		t.Fatalf("applied issues = %+v want 2（fresh untranslated + 范围外 grammar）", applied.QualityIssues)
	}
	freshSeen, scopeSeen := false, false
	for _, iss := range applied.QualityIssues {
		switch iss.Code {
		case qa.CheckUntranslated:
			freshSeen = true
			if !iss.IsPending() {
				t.Errorf("fresh 检出的 untranslated 应保持 pending: %+v", iss)
			}
		case qa.IssueCodeGrammar:
			scopeSeen = true
			if !iss.IsPending() {
				t.Errorf("范围外语义 grammar 应保留 pending: %+v", iss)
			}
		case qa.IssueCodeCalque:
			t.Errorf("targeted pending calque 应被移除: %+v", iss)
		}
	}
	if !freshSeen || !scopeSeen {
		t.Fatalf("want fresh untranslated + kept grammar, got %+v", applied.QualityIssues)
	}
}

// TestApplyPreview_TranslateToken_BehaviorUnchanged 回归翻译令牌语义：
// 改写 + QA 未启用 → 清空；不改写 → FinalIssues 原样。
func TestApplyPreview_TranslateToken_BehaviorUnchanged(t *testing.T) {
	applySvc, client, userID, projectID := newApplyFixture(t)
	res := createTestResource(t, client, projectID, "a.txt")

	newTranslateClaims := func(seg *ent.Segment, finalIssues []qa.QualityIssue, targetHash string) previewtoken.ApplyClaims {
		return previewtoken.ApplyClaims{
			ActorUserID:    userID,
			ProjectID:      projectID,
			ResourceID:     res.ID,
			SegmentID:      seg.ID,
			Kind:           previewtoken.KindTranslate,
			SourceHash:     sha256Hex(seg.SourceText),
			PreviewSource:  seg.SourceText,
			TargetHash:     targetHash,
			BaselineSource: seg.SourceText,
			BaselineTarget: seg.TargetText,
			BaselineStatus: string(segment.StatusTranslated),
			FinalIssues:    finalIssues,
			QAConfig:       previewtoken.QAConfigClaims{Enabled: false},
		}
	}

	// 改写 + QA 未启用 → 清空（既有行为）。
	rewritten := seedRevisionSegment(t, client, res.ID, 0, "src", "旧译文", segment.StatusTranslated, []qa.QualityIssue{
		{Code: qa.IssueCodeCalque, Message: "借译", Severity: qa.SeverityWarning, Disposition: qa.DispositionPending},
	})
	token := encodeApplyClaims(t, newTranslateClaims(rewritten, nil, sha256Hex("新译文")))
	applied, err := applySvc.ApplyPreview(context.Background(), userID, projectID, res.ID, rewritten.ID, token, "用户改写的译文")
	if err != nil {
		t.Fatalf("rewrite: ApplyPreview: %v", err)
	}
	if len(applied.QualityIssues) != 0 {
		t.Fatalf("translate rewrite without QA must clear issues, got %+v", applied.QualityIssues)
	}

	// 不改写 → FinalIssues 原样。
	unchanged := seedRevisionSegment(t, client, res.ID, 1, "src2", "旧译文2", segment.StatusTranslated, []qa.QualityIssue{
		{Code: qa.IssueCodeCalque, Message: "借译", Severity: qa.SeverityWarning, Disposition: qa.DispositionPending},
	})
	finalIssues := []qa.QualityIssue{{Code: qa.CheckNumberMismatch, Message: "数字不一致", Severity: qa.SeverityError, Disposition: qa.DispositionPending}}
	token = encodeApplyClaims(t, newTranslateClaims(unchanged, finalIssues, sha256Hex("新译文2")))
	applied, err = applySvc.ApplyPreview(context.Background(), userID, projectID, res.ID, unchanged.ID, token, "新译文2")
	if err != nil {
		t.Fatalf("no rewrite: ApplyPreview: %v", err)
	}
	if len(applied.QualityIssues) != 1 || applied.QualityIssues[0].Code != qa.CheckNumberMismatch {
		t.Fatalf("unchanged target must keep FinalIssues, got %+v", applied.QualityIssues)
	}
}

// TestApplyPreview_LegacyRevisionToken_EmptyResolvedCodes_FallsBackToOldBehavior
// 验证旧式修订令牌（无 rc 字段，ResolvedCodes 为空）退化为旧行为：
// 改写 + QA 未启用 → 整段清空。
func TestApplyPreview_LegacyRevisionToken_EmptyResolvedCodes_FallsBackToOldBehavior(t *testing.T) {
	applySvc, client, userID, projectID := newApplyFixture(t)
	res := createTestResource(t, client, projectID, "a.txt")
	seg := seedRevisionSegment(t, client, res.ID, 0, "src", "旧译文", segment.StatusTranslated, []qa.QualityIssue{
		{Code: qa.IssueCodeCalque, Message: "借译", Severity: qa.SeverityWarning, Disposition: qa.DispositionPending},
	})

	token := encodeApplyClaims(t, revisionApplyClaims(seg, res.ID, projectID, userID, nil, previewtoken.QAConfigClaims{Enabled: false}))

	applied, err := applySvc.ApplyPreview(context.Background(), userID, projectID, res.ID, seg.ID, token, "用户改写的译文")
	if err != nil {
		t.Fatalf("ApplyPreview: %v", err)
	}
	if len(applied.QualityIssues) != 0 {
		t.Fatalf("legacy revision token must fall back to clearing issues, got %+v", applied.QualityIssues)
	}
}
