package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/activitylog"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/usagerecord"
	"github.com/MeowSalty/LinguaFlow/backend/internal/preview"
	"github.com/MeowSalty/LinguaFlow/backend/internal/previewtoken"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
)

// fakeRevisionRunner 捕获 runner 输入并返回预设结果，供 RevisionPreviewService 单测。
type fakeRevisionRunner struct {
	called              bool
	capturedSnapshot    *JobExecutionSnapshot
	capturedSynthesized bool
	capturedTargetIdx   int
	capturedRepair      repair.Options
	result              *RevisionPreviewResult
	err                 error
}

func (f *fakeRevisionRunner) RunRevisionPreview(
	_ context.Context,
	snapshot *JobExecutionSnapshot,
	_ *ent.Project,
	_ *ent.Resource,
	_ []*ent.Segment,
	targetSegmentIdx int,
	_ qa.Config,
	repairOpts repair.Options,
	synthesized bool,
) (*RevisionPreviewResult, error) {
	f.called = true
	f.capturedSnapshot = snapshot
	f.capturedSynthesized = synthesized
	f.capturedTargetIdx = targetSegmentIdx
	f.capturedRepair = repairOpts
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

// newRevisionFixture 构建围绕内存 DB 的真实服务链，并注入 fakeRevisionRunner。
func newRevisionFixture(t *testing.T) (*RevisionPreviewService, *ent.Client, int, int, *fakeRevisionRunner) {
	t.Helper()
	client := testClient(t)
	logger := discardLogger()
	users := NewUserService(client, NewAuthService(client, AuthConfig{}, NewAdminService(client)))
	projects := NewProjectService(client, users)
	backends := NewBackendService(client, users, nil)
	executionPlans := NewExecutionPlanService(client, users)
	promptTemplates := NewTranslationPromptTemplateService(client)
	bootstrapTemplates := NewBootstrapPromptTemplateService(client)
	profiles := NewExecutionProfileService(client)
	jobs := NewJobService(client, projects, executionPlans, backends, promptTemplates, bootstrapTemplates, profiles, nil, nil)
	runner := &fakeRevisionRunner{}
	svc := NewRevisionPreviewService(logger, client, projects, jobs, runner, "test-secret", 15*time.Minute, 2, 5*time.Minute, nil)

	u, err := client.User.Create().
		SetUsername("alice").
		SetPasswordHash("$2a$10$dummyhashvaluehere").
		SetEmail("alice@test.com").
		Save(context.Background())
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	projectID := seedProject(t, client, u.ID)
	return svc, client, u.ID, projectID, runner
}

// seedRevisePlan 创建含单 revise 轮的用户级执行计划模板。
func seedRevisePlan(t *testing.T, client *ent.Client, userID, backendID int) int {
	t.Helper()
	plan, err := client.ExecutionPlanTemplate.Create().
		SetName("revise-plan").
		SetScope("user").
		SetOwnerUserID(userID).
		SetRubyRetry(schema.ExecutionPlanRubyRetryConfig{}).
		SetRounds([]schema.ExecutionRoundConfig{{
			Mode:      "revise",
			BackendID: backendID,
			Revise: &schema.ReviseRoundConfig{
				BatchSize:    5,
				Concurrency:  1,
				SegmentScope: "with_issues",
				Retry:        schema.RetryConfig{MaxAttempts: 2},
			},
		}}).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create revise plan: %v", err)
	}
	return plan.ID
}

// seedRevisionSegment 创建带译文、状态与 issues 的段落。
func seedRevisionSegment(t *testing.T, client *ent.Client, resourceID, index int, source, target string, status segment.Status, issues []qa.QualityIssue) *ent.Segment {
	t.Helper()
	row, err := client.Segment.Create().
		SetResourceID(resourceID).
		SetSegmentIndex(index).
		SetSourceText(source).
		SetTargetText(target).
		SetStatus(status).
		SetQualityIssues(issues).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create segment: %v", err)
	}
	return row
}

func revisionTestIssues() []qa.QualityIssue {
	return []qa.QualityIssue{
		{Code: qa.IssueCodeCalque, Message: "借译", Severity: qa.SeverityWarning, Disposition: qa.DispositionPending},
		{Code: qa.IssueCodeMistranslation, Message: "误译", Severity: qa.SeverityWarning, Disposition: qa.DispositionDismissed},
		{Code: qa.CheckLengthRatio, Message: "长度比", Severity: qa.SeverityError, Disposition: qa.DispositionPending},
	}
}

func TestRevisionPreviewService_Busy(t *testing.T) {
	_, client, userID, _, runner := newRevisionFixture(t)
	sem := make(chan struct{}, 1)
	sem <- struct{}{}
	users := NewUserService(client, NewAuthService(client, AuthConfig{}, NewAdminService(client)))
	busySvc := NewRevisionPreviewService(discardLogger(), client, NewProjectService(client, users), nil, runner, "test-secret", time.Minute, 1, time.Minute, sem)

	out, err := busySvc.RunRevisionPreview(context.Background(), RevisionPreviewInput{ActorUserID: userID})
	if err == nil || out != nil {
		t.Fatalf("want busy error, got out=%v err=%v", out, err)
	}
	if runner.called {
		t.Fatalf("runner must not be called when busy")
	}
}

func TestRevisionPreviewService_NoTarget(t *testing.T) {
	svc, client, userID, projectID, _ := newRevisionFixture(t)
	res := createTestResource(t, client, projectID, "a.txt")

	pending := seedRevisionSegment(t, client, res.ID, 0, "src", "译文", segment.StatusPending, revisionTestIssues())
	empty := seedRevisionSegment(t, client, res.ID, 1, "src", "", segment.StatusTranslated, revisionTestIssues())

	planID := seedTranslatePlan(t, client, userID, seedUserBackend(t, client, userID))
	for _, segID := range []int{pending.ID, empty.ID} {
		_, err := svc.RunRevisionPreview(context.Background(), RevisionPreviewInput{
			ActorUserID: userID, ProjectID: projectID, ResourceID: res.ID, SegmentID: segID,
			ExecutionPlanID: planID,
		})
		if err == nil || !containsError(err, ErrRevisionNoTarget) {
			t.Fatalf("segment %d: want ErrRevisionNoTarget, got %v", segID, err)
		}
	}
}

func TestRevisionPreviewService_NoIssues(t *testing.T) {
	svc, client, userID, projectID, runner := newRevisionFixture(t)
	res := createTestResource(t, client, projectID, "a.txt")

	// 只有 dismissed 语义 issue 与 pending 确定性 issue：均不是修复目标。
	seg := seedRevisionSegment(t, client, res.ID, 0, "src", "译文", segment.StatusTranslated, []qa.QualityIssue{
		{Code: qa.IssueCodeCalque, Message: "借译", Severity: qa.SeverityWarning, Disposition: qa.DispositionDismissed},
		{Code: qa.CheckLengthRatio, Message: "长度比", Severity: qa.SeverityError, Disposition: qa.DispositionPending},
	})

	planID := seedTranslatePlan(t, client, userID, seedUserBackend(t, client, userID))
	_, err := svc.RunRevisionPreview(context.Background(), RevisionPreviewInput{
		ActorUserID: userID, ProjectID: projectID, ResourceID: res.ID, SegmentID: seg.ID, ExecutionPlanID: planID,
	})
	if err == nil || !containsError(err, ErrRevisionNoIssues) {
		t.Fatalf("want ErrRevisionNoIssues, got %v", err)
	}
	if runner.called {
		t.Fatalf("runner must not be called when no fix issues")
	}
}

func TestRevisionPreviewService_InvalidIssueCodes(t *testing.T) {
	svc, client, userID, projectID, runner := newRevisionFixture(t)
	res := createTestResource(t, client, projectID, "a.txt")
	seg := seedRevisionSegment(t, client, res.ID, 0, "src", "译文", segment.StatusTranslated, []qa.QualityIssue{
		{Code: qa.IssueCodeCalque, Message: "借译", Severity: qa.SeverityWarning, Disposition: qa.DispositionPending},
	})

	_, err := svc.RunRevisionPreview(context.Background(), RevisionPreviewInput{
		ActorUserID: userID, ProjectID: projectID, ResourceID: res.ID, SegmentID: seg.ID,
		ExecutionPlanID: seedTranslatePlan(t, client, userID, seedUserBackend(t, client, userID)),
		IssueCodes:      []string{qa.CheckLengthRatio},
	})
	if err == nil || !containsError(err, ErrRevisionInvalidIssueCodes) {
		t.Fatalf("want ErrRevisionInvalidIssueCodes, got %v", err)
	}
	if runner.called {
		t.Fatalf("runner must not be called on invalid codes")
	}
}

func TestRevisionPreviewService_IssueCodesFilter_EmptyIntersection(t *testing.T) {
	svc, client, userID, projectID, runner := newRevisionFixture(t)
	res := createTestResource(t, client, projectID, "a.txt")
	seg := seedRevisionSegment(t, client, res.ID, 0, "src", "译文", segment.StatusTranslated, []qa.QualityIssue{
		{Code: qa.IssueCodeCalque, Message: "借译", Severity: qa.SeverityWarning, Disposition: qa.DispositionPending},
	})

	_, err := svc.RunRevisionPreview(context.Background(), RevisionPreviewInput{
		ActorUserID: userID, ProjectID: projectID, ResourceID: res.ID, SegmentID: seg.ID,
		ExecutionPlanID: seedTranslatePlan(t, client, userID, seedUserBackend(t, client, userID)),
		IssueCodes:      []string{qa.IssueCodeMistranslation},
	})
	if err == nil || !containsError(err, ErrRevisionNoIssues) {
		t.Fatalf("want ErrRevisionNoIssues for empty intersection, got %v", err)
	}
	if runner.called {
		t.Fatalf("runner must not be called for empty intersection")
	}
}

func TestRevisionPreviewService_NoBackend(t *testing.T) {
	svc, client, userID, projectID, _ := newRevisionFixture(t)
	res := createTestResource(t, client, projectID, "a.txt")
	seg := seedRevisionSegment(t, client, res.ID, 0, "src", "译文", segment.StatusTranslated, []qa.QualityIssue{
		{Code: qa.IssueCodeCalque, Message: "借译", Severity: qa.SeverityWarning, Disposition: qa.DispositionPending},
	})

	// 仅 extract 轮：无 revise 轮也无 translate 轮，无法合成修订轮。
	planID := seedExtractOnlyPlan(t, client, userID, seedUserBackend(t, client, userID))
	_, err := svc.RunRevisionPreview(context.Background(), RevisionPreviewInput{
		ActorUserID: userID, ProjectID: projectID, ResourceID: res.ID, SegmentID: seg.ID, ExecutionPlanID: planID,
	})
	if err == nil || !containsError(err, ErrRevisionNoBackend) {
		t.Fatalf("want ErrRevisionNoBackend, got %v", err)
	}
}

func TestRevisionPreviewService_Success_SynthesizedFromTranslate(t *testing.T) {
	svc, client, userID, projectID, runner := newRevisionFixture(t)
	res := createTestResource(t, client, projectID, "a.txt")
	seg := seedRevisionSegment(t, client, res.ID, 0, "src", "旧译文", segment.StatusTranslated, revisionTestIssues())
	seedRevisionSegment(t, client, res.ID, 1, "other", "其他译文", segment.StatusTranslated, nil)

	finalIssues := []qa.QualityIssue{{Code: qa.CheckNumberMismatch, Message: "数字不一致", Severity: qa.SeverityError, Disposition: qa.DispositionPending}}
	runner.result = &RevisionPreviewResult{
		Status:        "success",
		SegmentID:     seg.ID,
		SourceText:    "src",
		TargetText:    "新译文",
		QualityIssues: finalIssues,
		Metrics:       []backend.MeterMetrics{{APICalls: 1, InputTokens: 10, OutputTokens: 5}},
		Collector:     preview.NewMemoryCollector(),
	}

	out, err := svc.RunRevisionPreview(context.Background(), RevisionPreviewInput{
		ActorUserID: userID, ProjectID: projectID, ResourceID: res.ID, SegmentID: seg.ID,
		ExecutionPlanID: seedTranslatePlan(t, client, userID, seedUserBackend(t, client, userID)),
	})
	if err != nil {
		t.Fatalf("RunRevisionPreview: %v", err)
	}

	if !runner.called || !runner.capturedSynthesized {
		t.Fatalf("runner must be called with synthesized=true")
	}
	if len(runner.capturedSnapshot.Rounds) != 1 || runner.capturedSnapshot.Rounds[0].Mode != "revise" {
		t.Fatalf("captured snapshot must contain single revise round, got %+v", runner.capturedSnapshot.Rounds)
	}
	if runner.capturedTargetIdx != 0 {
		t.Fatalf("target idx = %d want 0", runner.capturedTargetIdx)
	}

	// 合成轮的 IssueCodes 收窄为实际修复目标（calque），不使用全量语义白名单。
	rev := runner.capturedSnapshot.Rounds[0].Revise
	if rev == nil || len(rev.IssueCodes) != 1 || rev.IssueCodes[0] != qa.IssueCodeCalque {
		t.Fatalf("synthesized issue codes = %+v want [calque]", rev)
	}
	// Repair 从完整 snapshot 的 translate 轮策略抢救：默认 profile 全部算子开启。
	if want := (repair.Options{JSONStructural: true, SchemaAliases: true, PlaceholderNormalize: true, PromptUpgrade: true}); runner.capturedRepair != want {
		t.Fatalf("repair opts = %+v want %+v", runner.capturedRepair, want)
	}

	// FixIssues 仅含 pending 语义 issue（calque），排除 dismissed 与确定性。
	if len(out.FixIssues) != 1 || out.FixIssues[0].Code != qa.IssueCodeCalque {
		t.Fatalf("FixIssues = %+v want single pending calque", out.FixIssues)
	}
	if out.OriginalTargetText != "旧译文" || out.TargetText != "新译文" || out.Status != "success" {
		t.Fatalf("out = %+v", out)
	}

	// 令牌断言：kind=fix、基线=原译文、TargetHash=修订文本哈希。
	claims, err := previewtoken.NewCodec("test-secret", time.Minute).Decode(out.ApplyToken)
	if err != nil {
		t.Fatalf("decode token: %v", err)
	}
	if claims.Kind != previewtoken.KindRevision {
		t.Fatalf("kind = %q want %q", claims.Kind, previewtoken.KindRevision)
	}
	if claims.BaselineTarget == nil || *claims.BaselineTarget != "旧译文" {
		t.Fatalf("baseline target = %v want 旧译文", claims.BaselineTarget)
	}
	if claims.TargetHash != sha256Hex("新译文") {
		t.Fatalf("target hash mismatch")
	}
	if len(claims.FinalIssues) != 1 || claims.FinalIssues[0].Code != qa.CheckNumberMismatch {
		t.Fatalf("final issues = %+v", claims.FinalIssues)
	}

	// 用量记录落库。
	n, err := client.UsageRecord.Query().Where(usagerecord.SourceEQ("preview")).Count(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("usage records = %d err=%v want 1", n, err)
	}
}

func TestRevisionPreviewService_Success_UsesConfiguredReviseRound(t *testing.T) {
	svc, client, userID, projectID, runner := newRevisionFixture(t)
	res := createTestResource(t, client, projectID, "a.txt")
	seg := seedRevisionSegment(t, client, res.ID, 0, "src", "旧译文", segment.StatusEdited, []qa.QualityIssue{
		{Code: qa.IssueCodeCalque, Message: "借译", Severity: qa.SeverityWarning, Disposition: qa.DispositionPending},
		{Code: qa.IssueCodeOmission, Message: "漏译", Severity: qa.SeverityWarning, Disposition: qa.DispositionPending},
	})
	runner.result = &RevisionPreviewResult{
		Status: "success", SegmentID: seg.ID, SourceText: "src", TargetText: "新译文",
		Collector: preview.NewMemoryCollector(),
	}

	out, err := svc.RunRevisionPreview(context.Background(), RevisionPreviewInput{
		ActorUserID: userID, ProjectID: projectID, ResourceID: res.ID, SegmentID: seg.ID,
		ExecutionPlanID: seedRevisePlan(t, client, userID, seedUserBackend(t, client, userID)),
	})
	if err != nil {
		t.Fatalf("RunRevisionPreview: %v", err)
	}
	if runner.capturedSynthesized {
		t.Fatalf("configured revise round must not be synthesized")
	}
	rev := runner.capturedSnapshot.Rounds[0].Revise
	if rev == nil {
		t.Fatalf("revise snapshot is nil")
	}
	if rev.BatchSize != 5 || rev.SegmentScope != "with_issues" {
		t.Fatalf("revise config = %+v", rev)
	}
	// 配置轮的 IssueCodes 收窄为实际修复目标的 code 集合。
	if len(rev.IssueCodes) != 2 {
		t.Fatalf("issue codes = %v want calque+omission", rev.IssueCodes)
	}
	if len(out.FixIssues) != 2 {
		t.Fatalf("FixIssues = %+v want 2", out.FixIssues)
	}
}

func TestRevisionPreviewService_FailedRound_NoToken(t *testing.T) {
	svc, client, userID, projectID, runner := newRevisionFixture(t)
	res := createTestResource(t, client, projectID, "a.txt")
	seg := seedRevisionSegment(t, client, res.ID, 0, "src", "旧译文", segment.StatusTranslated, []qa.QualityIssue{
		{Code: qa.IssueCodeCalque, Message: "借译", Severity: qa.SeverityWarning, Disposition: qa.DispositionPending},
	})
	// failed 轮的 TargetText 是回退的原译文（非空），令牌必须被抑制。
	runner.result = &RevisionPreviewResult{
		Status: "failed", SegmentID: seg.ID, SourceText: "src", TargetText: "旧译文",
		Collector: preview.NewMemoryCollector(),
	}

	out, err := svc.RunRevisionPreview(context.Background(), RevisionPreviewInput{
		ActorUserID: userID, ProjectID: projectID, ResourceID: res.ID, SegmentID: seg.ID,
		ExecutionPlanID: seedTranslatePlan(t, client, userID, seedUserBackend(t, client, userID)),
	})
	if err != nil {
		t.Fatalf("RunRevisionPreview: %v", err)
	}
	if out.Status != "failed" || out.ApplyToken != "" {
		t.Fatalf("failed round must not issue token: status=%q token=%q", out.Status, out.ApplyToken)
	}
}

// TestRevisionPreviewService_UnchangedTarget_NoToken 验证 partial 轮（重试耗尽/
// preserveResult 回写原文）与 no-op 成功轮（LLM 判定无需改动）都不会签发令牌：
// TargetText 等于基线原译文时应用令牌只会给原译文盖 edited/reviewed 戳。
func TestRevisionPreviewService_UnchangedTarget_NoToken(t *testing.T) {
	svc, client, userID, projectID, runner := newRevisionFixture(t)
	res := createTestResource(t, client, projectID, "a.txt")
	seg := seedRevisionSegment(t, client, res.ID, 0, "src", "旧译文", segment.StatusTranslated, []qa.QualityIssue{
		{Code: qa.IssueCodeCalque, Message: "借译", Severity: qa.SeverityWarning, Disposition: qa.DispositionPending},
	})
	planID := seedTranslatePlan(t, client, userID, seedUserBackend(t, client, userID))

	for _, tc := range []struct {
		name   string
		status string
	}{
		{"partial", "partial"},
		{"noop_success", "success"},
	} {
		runner.result = &RevisionPreviewResult{
			Status: tc.status, SegmentID: seg.ID, SourceText: "src", TargetText: "旧译文",
			Collector: preview.NewMemoryCollector(),
		}
		out, err := svc.RunRevisionPreview(context.Background(), RevisionPreviewInput{
			ActorUserID: userID, ProjectID: projectID, ResourceID: res.ID, SegmentID: seg.ID,
			ExecutionPlanID: planID,
		})
		if err != nil {
			t.Fatalf("%s: RunRevisionPreview: %v", tc.name, err)
		}
		if out.Status != tc.status || out.ApplyToken != "" {
			t.Fatalf("%s: unchanged target must not issue token: status=%q token=%q", tc.name, out.Status, out.ApplyToken)
		}
	}
}

// TestRevisionPreviewService_Synthesized_IssueCodesNarrowed 验证合成 revise 轮
// 同样遵守请求级 issue_codes 收窄：喂给 LLM 的修复目标不超出请求的 code 子集。
func TestRevisionPreviewService_Synthesized_IssueCodesNarrowed(t *testing.T) {
	svc, client, userID, projectID, runner := newRevisionFixture(t)
	res := createTestResource(t, client, projectID, "a.txt")
	seg := seedRevisionSegment(t, client, res.ID, 0, "src", "旧译文", segment.StatusTranslated, []qa.QualityIssue{
		{Code: qa.IssueCodeCalque, Message: "借译", Severity: qa.SeverityWarning, Disposition: qa.DispositionPending},
		{Code: qa.IssueCodeOmission, Message: "漏译", Severity: qa.SeverityWarning, Disposition: qa.DispositionPending},
	})
	runner.result = &RevisionPreviewResult{
		Status: "success", SegmentID: seg.ID, SourceText: "src", TargetText: "新译文",
		Collector: preview.NewMemoryCollector(),
	}

	_, err := svc.RunRevisionPreview(context.Background(), RevisionPreviewInput{
		ActorUserID: userID, ProjectID: projectID, ResourceID: res.ID, SegmentID: seg.ID,
		ExecutionPlanID: seedTranslatePlan(t, client, userID, seedUserBackend(t, client, userID)),
		IssueCodes:      []string{qa.IssueCodeCalque},
	})
	if err != nil {
		t.Fatalf("RunRevisionPreview: %v", err)
	}
	rev := runner.capturedSnapshot.Rounds[0].Revise
	if rev == nil || len(rev.IssueCodes) != 1 || rev.IssueCodes[0] != qa.IssueCodeCalque {
		t.Fatalf("synthesized issue codes = %+v want only [calque]", rev)
	}
}

// TestRevisionPreviewService_ApplyViaPreviewEndpoint 验证修订令牌可经现有
// translation-preview/apply 应用：CAS 基线为原译文、置 edited/reviewed_by、
// 审计事件分流为 revision_preview.apply，且二次应用因基线变化冲突。
func TestRevisionPreviewService_ApplyViaPreviewEndpoint(t *testing.T) {
	svc, client, userID, projectID, runner := newRevisionFixture(t)
	users := NewUserService(client, NewAuthService(client, AuthConfig{}, NewAdminService(client)))
	projects := NewProjectService(client, users)
	backends := NewBackendService(client, users, nil)
	executionPlans := NewExecutionPlanService(client, users)
	promptTemplates := NewTranslationPromptTemplateService(client)
	bootstrapTemplates := NewBootstrapPromptTemplateService(client)
	profiles := NewExecutionProfileService(client)
	jobs := NewJobService(client, projects, executionPlans, backends, promptTemplates, bootstrapTemplates, profiles, nil, nil)
	audit := NewAuditService(client, users, projects)
	applySvc := NewPreviewService(discardLogger(), client, projects, jobs, audit, nil, "test-secret", 15*time.Minute, 2, 5*time.Minute)

	res := createTestResource(t, client, projectID, "a.txt")
	seg := seedRevisionSegment(t, client, res.ID, 0, "src", "旧译文", segment.StatusTranslated, []qa.QualityIssue{
		{Code: qa.IssueCodeCalque, Message: "借译", Severity: qa.SeverityWarning, Disposition: qa.DispositionPending},
	})
	finalIssues := []qa.QualityIssue{{Code: qa.CheckNumberMismatch, Message: "数字不一致", Severity: qa.SeverityError, Disposition: qa.DispositionPending}}
	runner.result = &RevisionPreviewResult{
		Status: "success", SegmentID: seg.ID, SourceText: "src", TargetText: "新译文",
		QualityIssues: finalIssues, Collector: preview.NewMemoryCollector(),
	}

	out, err := svc.RunRevisionPreview(context.Background(), RevisionPreviewInput{
		ActorUserID: userID, ProjectID: projectID, ResourceID: res.ID, SegmentID: seg.ID,
		ExecutionPlanID: seedTranslatePlan(t, client, userID, seedUserBackend(t, client, userID)),
	})
	if err != nil {
		t.Fatalf("RunRevisionPreview: %v", err)
	}

	applied, err := applySvc.ApplyPreview(context.Background(), userID, projectID, res.ID, seg.ID, out.ApplyToken, "新译文")
	if err != nil {
		t.Fatalf("ApplyPreview: %v", err)
	}
	if applied.TargetText == nil || *applied.TargetText != "新译文" {
		t.Fatalf("applied target = %v", applied.TargetText)
	}
	if applied.Status != segment.StatusEdited {
		t.Fatalf("applied status = %v want edited", applied.Status)
	}
	if applied.Edges.ReviewedBy == nil || applied.Edges.ReviewedBy.ID != userID {
		t.Fatalf("reviewed_by = %v want user %d", applied.Edges.ReviewedBy, userID)
	}
	if len(applied.QualityIssues) != 1 || applied.QualityIssues[0].Code != qa.CheckNumberMismatch {
		t.Fatalf("applied issues = %+v", applied.QualityIssues)
	}

	n, err := client.ActivityLog.Query().
		Where(activitylog.ActionEQ("resource.segment.revision_preview.apply")).
		Count(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("revision apply audit events = %d err=%v want 1", n, err)
	}

	// 基线已移动：同一令牌二次应用必须冲突。
	_, err = applySvc.ApplyPreview(context.Background(), userID, projectID, res.ID, seg.ID, out.ApplyToken, "新译文")
	if err == nil || !containsError(err, ErrPreviewConflict) {
		t.Fatalf("re-apply want ErrPreviewConflict, got %v", err)
	}
}

func containsError(err, target error) bool {
	return errors.Is(err, target)
}
