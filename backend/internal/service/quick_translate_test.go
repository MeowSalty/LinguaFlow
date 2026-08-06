package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/activitylog"
	entbackend "github.com/MeowSalty/LinguaFlow/backend/internal/ent/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/usagerecord"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/preview"
)

// fakeQuickRunner 捕获 runner 输入并返回预设结果，供 QuickTranslateService 单测。
type fakeQuickRunner struct {
	called   bool
	captured QuickTranslateRunnerInput
	result   *QuickTranslateResult
	err      error
}

func (f *fakeQuickRunner) Run(_ context.Context, in QuickTranslateRunnerInput) (*QuickTranslateResult, error) {
	f.called = true
	f.captured = in
	if f.err != nil {
		return nil, f.err
	}
	if f.result != nil {
		return f.result, nil
	}
	return &QuickTranslateResult{
		Status:     "success",
		SourceText: in.SourceText,
		TargetText: "译文",
		Collector:  preview.NewMemoryCollector(),
		Metrics:    []backend.MeterMetrics{{APICalls: 1, InputTokens: 10, OutputTokens: 5}},
	}, nil
}

// newQuickFixture 构建围绕内存 DB 的全套真实服务，并注入 fakeQuickRunner。
// JobService 的 store/broker 传 nil：prepareExecutionSnapshotForActor 不使用它们。
func newQuickFixture(t *testing.T) (*QuickTranslateService, *ent.Client, int, *fakeQuickRunner) {
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
	audit := NewAuditService(client, users, projects)
	runner := &fakeQuickRunner{}
	svc := NewQuickTranslateService(logger, client, projects, jobs, backends, executionPlans, audit, runner, 2, 5*time.Minute)

	u, err := client.User.Create().
		SetUsername("alice").
		SetPasswordHash("$2a$10$dummyhashvaluehere").
		SetEmail("alice@test.com").
		Save(context.Background())
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return svc, client, u.ID, runner
}

func seedUserBackend(t *testing.T, client *ent.Client, userID int) int {
	t.Helper()
	b, err := client.Backend.Create().
		SetName("be").
		SetBackendType(entbackend.BackendType("openai")).
		SetScope("user").
		SetOwnerUserID(userID).
		SetOptions(map[string]any{}).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create backend: %v", err)
	}
	return b.ID
}

// seedTranslatePlan 直接经 ent 创建一个仅含单 translate 轮的用户级执行计划模板，
// 引用内置提示词模板 (ID=-1) 与内置策略 (ID=-1)。绕过 validateExecutionRounds。
func seedTranslatePlan(t *testing.T, client *ent.Client, userID, backendID int) int {
	t.Helper()
	plan, err := client.ExecutionPlanTemplate.Create().
		SetName("p").
		SetScope("user").
		SetOwnerUserID(userID).
		SetRubyRetry(schema.ExecutionPlanRubyRetryConfig{}).
		SetRounds([]schema.ExecutionRoundConfig{{
			Mode:      "translate",
			BackendID: backendID,
			Translate: &schema.TranslateRoundConfig{
				PromptTemplateID: -1,
				ProfileID:        -1,
				BatchSize:        10,
				MaxWordsPerBatch: 500,
				Concurrency:      1,
				Retry:            schema.RetryConfig{MaxAttempts: 0},
			},
		}}).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create translate plan: %v", err)
	}
	return plan.ID
}

// seedExtractOnlyPlan 创建仅含单 extract 轮的计划（无 translate 轮）。
func seedExtractOnlyPlan(t *testing.T, client *ent.Client, userID, backendID int) int {
	t.Helper()
	plan, err := client.ExecutionPlanTemplate.Create().
		SetName("extract-only").
		SetScope("user").
		SetOwnerUserID(userID).
		SetRubyRetry(schema.ExecutionPlanRubyRetryConfig{}).
		SetRounds([]schema.ExecutionRoundConfig{{
			Mode:      "extract",
			BackendID: backendID,
			Extract: &schema.ExtractRoundConfig{
				BootstrapTemplateID: -1,
				BatchSize:           10,
				MaxWordsPerBatch:    500,
				Concurrency:         1,
				Retry:               schema.RetryConfig{MaxAttempts: 0},
			},
		}}).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create extract plan: %v", err)
	}
	return plan.ID
}

func seedProject(t *testing.T, client *ent.Client, userID int) int {
	t.Helper()
	p, err := client.Project.Create().
		SetName("proj").
		SetOwnerUserID(userID).
		SetSourceLang("en").
		SetTargetLang("zh").
		SetGlossaryEnabled(true).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return p.ID
}

func seedGlossaryEntry(t *testing.T, client *ent.Client, projectID int, source, target string) {
	t.Helper()
	_, err := client.GlossaryEntry.Create().
		SetProjectID(projectID).
		SetSourceKey(glossarySourceKey(source)).
		SetSource(source).
		SetTarget(target).
		SetCaseSensitive(false).
		SetForbidden(false).
		SetMandatory(true).
		SetNotes("").
		Save(context.Background())
	if err != nil {
		t.Fatalf("seed glossary entry: %v", err)
	}
}

func TestQuickTranslateService_EmptySource_ReturnsInvalidInput(t *testing.T) {
	svc, _, _, _ := newQuickFixture(t)
	_, err := svc.Translate(context.Background(), QuickTranslateInput{
		ActorUserID:     1,
		SourceText:      "   ",
		ExecutionPlanID: 1,
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err = %v, want ErrInvalidInput", err)
	}
}

func TestQuickTranslateService_Busy_ReturnsBusy(t *testing.T) {
	svc := NewQuickTranslateService(discardLogger(), nil, nil, nil, nil, nil, nil, &fakeQuickRunner{}, 1, 5*time.Minute)
	// 预占 actor 1 唯一的 per-actor 槽位 (cap=1)。
	as := svc.acquirePerActor(1)
	if as == nil {
		t.Fatal("acquirePerActor returned nil for fresh actor")
	}
	defer svc.releasePerActor(1, as)
	_, err := svc.Translate(context.Background(), QuickTranslateInput{
		ActorUserID:     1,
		SourceText:      "hello",
		ExecutionPlanID: 1,
	})
	if !errors.Is(err, ErrQuickTranslateBusy) {
		t.Fatalf("err = %v, want ErrQuickTranslateBusy", err)
	}
}

func TestQuickTranslateService_PerActorSemaphore_DoesNotBlockOtherUser(t *testing.T) {
	// 单用户耗尽自身槽位不应阻塞其他用户。用真实 fixture，使两个 actor 都能跑通到 runner。
	svc, client, aliceID, runner := newQuickFixture(t)
	// 为 bob 也建一个独立的 backend + plan。
	bob, err := client.User.Create().
		SetUsername("bob").
		SetPasswordHash("$2a$10$dummyhashvaluehere").
		SetEmail("bob-qt@test.com").
		Save(context.Background())
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	bobBackend := seedUserBackend(t, client, bob.ID)
	bobPlan := seedTranslatePlan(t, client, bob.ID, bobBackend)

	// 预占 alice 的全部槽位 (cap=2)，使其下一次请求应 Busy。
	s1 := svc.acquirePerActor(aliceID)
	s2 := svc.acquirePerActor(aliceID)
	defer svc.releasePerActor(aliceID, s1)
	defer svc.releasePerActor(aliceID, s2)
	if s1 == nil || s2 == nil {
		t.Fatalf("expected both alice slots acquirable, got %v %v", s1 != nil, s2 != nil)
	}

	// alice 第三次请求 → Busy。
	_, err = svc.Translate(context.Background(), QuickTranslateInput{
		ActorUserID:     aliceID,
		SourceText:      "hello",
		ExecutionPlanID: 1, // 不存在，但 Busy 应在快照前返回
	})
	if !errors.Is(err, ErrQuickTranslateBusy) {
		t.Fatalf("alice should be busy, got err = %v", err)
	}

	// bob 的请求不应被 alice 占用影响，正常成功。
	bobOut, err := svc.Translate(context.Background(), QuickTranslateInput{
		ActorUserID:     bob.ID,
		SourceText:      "hello",
		ExecutionPlanID: bobPlan,
	})
	if err != nil {
		t.Fatalf("bob translate should succeed, got err = %v", err)
	}
	if bobOut.Status != "success" {
		t.Errorf("bob status = %q, want success", bobOut.Status)
	}
	if !runner.called {
		t.Error("runner should be called for bob")
	}
}

func TestQuickTranslateService_GlobalCap_ReturnsBusyWhenExhausted(t *testing.T) {
	// 全局上限 = per-actor 上限 × 4。这里 per-actor=1 ⇒ 全局 cap=4。
	// 占满 4 个不同 actor 的槽位后，第 5 个 actor 也应 Busy。
	svc := NewQuickTranslateService(discardLogger(), nil, nil, nil, nil, nil, nil, &fakeQuickRunner{}, 1, 5*time.Minute)
	var held []*actorSemaphore
	for i := 1; i <= 4; i++ {
		as := svc.acquirePerActor(i)
		if as == nil {
			t.Fatalf("acquirePerActor(%d) returned nil", i)
		}
		held = append(held, as)
	}
	defer func() {
		for i, as := range held {
			svc.releasePerActor(i+1, as)
		}
	}()
	s5 := svc.acquirePerActor(5)
	if s5 == nil {
		t.Fatal("5th actor per-actor slot should be acquirable (per-actor not the limit)")
	}
	defer svc.releasePerActor(5, s5)

	_, err := svc.Translate(context.Background(), QuickTranslateInput{
		ActorUserID:     5,
		SourceText:      "hello",
		ExecutionPlanID: 1,
	})
	if !errors.Is(err, ErrQuickTranslateBusy) {
		t.Fatalf("5th actor should hit global cap, got err = %v", err)
	}
}

func TestQuickTranslateService_Success_NoProject(t *testing.T) {
	svc, client, userID, runner := newQuickFixture(t)
	backendID := seedUserBackend(t, client, userID)
	planID := seedTranslatePlan(t, client, userID, backendID)

	out, err := svc.Translate(context.Background(), QuickTranslateInput{
		ActorUserID:     userID,
		SourceText:      "hello",
		ExecutionPlanID: planID,
	})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if out.Status != "success" {
		t.Errorf("status = %q, want success", out.Status)
	}
	if out.TargetText != "译文" {
		t.Errorf("target = %q, want 译文", out.TargetText)
	}
	if out.Usage.APICalls != 1 {
		t.Errorf("usage APICalls = %d, want 1", out.Usage.APICalls)
	}
	if !runner.called {
		t.Fatal("runner not called")
	}
	if runner.captured.Snapshot == nil {
		t.Error("captured snapshot is nil")
	}
	if _, isNop := runner.captured.Glossary.(glossary.Nop); !isNop {
		t.Errorf("captured glossary = %T, want glossary.Nop (no glossary)", runner.captured.Glossary)
	}

	// 用量记录：source=quick_translate。
	usageCount, err := client.UsageRecord.Query().Where(usagerecord.SourceEQ("quick_translate")).Count(context.Background())
	if err != nil {
		t.Fatalf("count usage: %v", err)
	}
	if usageCount != 1 {
		t.Errorf("usage records = %d, want 1", usageCount)
	}
	// 审计日志：action=quick_translate。
	auditCount, err := client.ActivityLog.Query().Where(activitylog.ActionEQ("quick_translate")).Count(context.Background())
	if err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if auditCount != 1 {
		t.Errorf("audit logs = %d, want 1", auditCount)
	}
}

func TestQuickTranslateService_NoProject_InlineGlossary_BuildsMemoryGlossary(t *testing.T) {
	svc, client, userID, runner := newQuickFixture(t)
	backendID := seedUserBackend(t, client, userID)
	planID := seedTranslatePlan(t, client, userID, backendID)

	_, err := svc.Translate(context.Background(), QuickTranslateInput{
		ActorUserID:     userID,
		SourceText:      "hello GPU",
		ExecutionPlanID: planID,
		Glossary: []QuickGlossaryEntryInput{{
			Source: "GPU",
			Target: "图形处理器",
		}},
	})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if _, isNop := runner.captured.Glossary.(glossary.Nop); isNop {
		t.Fatal("captured glossary is Nop, want a real in-memory glossary for inline entries")
	}
	entries, lerr := runner.captured.Glossary.Lookup(context.Background(), "hello GPU", "en", "zh")
	if lerr != nil {
		t.Fatalf("lookup: %v", lerr)
	}
	found := false
	for _, e := range entries {
		if e.Source == "GPU" && e.Target == "图形处理器" {
			found = true
		}
	}
	if !found {
		t.Errorf("inline glossary entry not found in lookup; entries=%+v", entries)
	}
}

func TestQuickTranslateService_Project_InlineGlossary_OverlaysDatabase(t *testing.T) {
	svc, client, userID, runner := newQuickFixture(t)
	backendID := seedUserBackend(t, client, userID)
	planID := seedTranslatePlan(t, client, userID, backendID)
	projectID := seedProject(t, client, userID)
	seedGlossaryEntry(t, client, projectID, "API", "接口")

	out, err := svc.Translate(context.Background(), QuickTranslateInput{
		ActorUserID:     userID,
		SourceText:      "API and GPU",
		ExecutionPlanID: planID,
		ProjectID:       &projectID,
		Glossary: []QuickGlossaryEntryInput{{
			Source: "GPU",
			Target: "图形处理器",
		}},
	})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	// 语言缺省取自项目（请求未显式覆盖）。
	if out.SourceLang != "en" {
		t.Errorf("sourceLang = %q, want en (from project)", out.SourceLang)
	}
	if out.TargetLang != "zh" {
		t.Errorf("targetLang = %q, want zh (from project)", out.TargetLang)
	}
	// 叠加术语表：DB 条目 (API) + 内联条目 (GPU) 都应命中。
	entries, lerr := runner.captured.Glossary.Lookup(context.Background(), "API and GPU", "en", "zh")
	if lerr != nil {
		t.Fatalf("lookup: %v", lerr)
	}
	hasAPI, hasGPU := false, false
	for _, e := range entries {
		if e.Source == "API" && e.Target == "接口" {
			hasAPI = true
		}
		if e.Source == "GPU" && e.Target == "图形处理器" {
			hasGPU = true
		}
	}
	if !hasAPI {
		t.Errorf("DB glossary entry API→接口 missing; entries=%+v", entries)
	}
	if !hasGPU {
		t.Errorf("inline glossary entry GPU→图形处理器 missing; entries=%+v", entries)
	}
}

func TestQuickTranslateService_NoTranslatePlan_ReturnsErr(t *testing.T) {
	svc, client, userID, runner := newQuickFixture(t)
	backendID := seedUserBackend(t, client, userID)
	planID := seedExtractOnlyPlan(t, client, userID, backendID)

	_, err := svc.Translate(context.Background(), QuickTranslateInput{
		ActorUserID:     userID,
		SourceText:      "hello",
		ExecutionPlanID: planID,
	})
	if !errors.Is(err, ErrQuickTranslateNoTranslate) {
		t.Fatalf("err = %v, want ErrQuickTranslateNoTranslate", err)
	}
	if runner.called {
		t.Error("runner should not be called when plan has no translate round")
	}
}

func TestQuickTranslateService_ForbiddenProject_ReturnsForbidden(t *testing.T) {
	svc, client, userID, _ := newQuickFixture(t)
	// 项目由另一个用户 (bob) 拥有。
	bob, err := client.User.Create().
		SetUsername("bob").
		SetPasswordHash("$2a$10$dummyhashvaluehere").
		SetEmail("bob@test.com").
		Save(context.Background())
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	bobProject := seedProject(t, client, bob.ID)

	_, err = svc.Translate(context.Background(), QuickTranslateInput{
		ActorUserID:     userID, // alice
		SourceText:      "hello",
		ExecutionPlanID: 1,
		ProjectID:       &bobProject,
	})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
}

func TestQuickTranslateService_PlanNotFound_ReturnsErr(t *testing.T) {
	svc, _, userID, runner := newQuickFixture(t)
	_, err := svc.Translate(context.Background(), QuickTranslateInput{
		ActorUserID:     userID,
		SourceText:      "hello",
		ExecutionPlanID: 999999,
	})
	if !errors.Is(err, ErrExecutionPlanNotFound) {
		t.Fatalf("err = %v, want ErrExecutionPlanNotFound", err)
	}
	if runner.called {
		t.Error("runner should not be called when plan is not found")
	}
}
