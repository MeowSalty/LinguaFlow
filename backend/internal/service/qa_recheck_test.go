package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

// ---- 辅助函数 ----

// createRecheckProfile 用 ent client 直接创建启用 QA 的执行策略（scope=user）。
// protectEnabled/rules 控制保护区重建；qaChecks 为 nil 时启用全部 per-batch checker。
func createRecheckProfile(t *testing.T, client *ent.Client, ownerID int, mutate func(cfg *schema.ExecutionProfileConfigData)) *ent.ExecutionProfile {
	t.Helper()
	cfg := schema.DefaultProfileConfig()
	cfg.QA.Enabled = true
	if mutate != nil {
		mutate(&cfg)
	}
	row, err := client.ExecutionProfile.Create().
		SetName("recheck-profile").
		SetScope("user").
		SetOwnerUserID(ownerID).
		SetConfig(cfg).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create execution profile: %v", err)
	}
	return row
}

// newRecheckService 构造被测服务。
func newRecheckService(client *ent.Client) *QARecheckService {
	return NewQARecheckService(client, NewProjectService(client, nil), NewExecutionProfileService(client, nil), discardLogger())
}

// recheckIssue 快捷构造一条待处理 issue（可指定指纹要素）。
func recheckIssue(code, matchedText string) qa.QualityIssue {
	iss := qa.QualityIssue{
		SegmentIndex: 0,
		Severity:     qa.SeverityWarning,
		Code:         code,
		Message:      "预置 issue " + code,
		Disposition:  qa.DispositionPending,
	}
	if matchedText != "" {
		iss.Span = &qa.Span{MatchedText: matchedText}
	}
	return iss
}

// ---- 1. profile QA 关闭 → ErrQAProfileDisabled ----

func TestQARecheck_ProfileQADisabled(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "recheck-disabled-user")
	project := createTestProject(t, client, "recheck-disabled-proj", user.ID)
	createTestResource(t, client, project.ID, "a.txt")

	// 默认配置 QA.Enabled=false。
	cfg := schema.DefaultProfileConfig()
	profile, err := client.ExecutionProfile.Create().
		SetName("qa-off").
		SetScope("user").
		SetOwnerUserID(user.ID).
		SetConfig(cfg).
		Save(ctx)
	if err != nil {
		t.Fatalf("create profile: %v", err)
	}

	svc := newRecheckService(client)
	_, err = svc.Recheck(ctx, user.ID, project.ID, QARecheckInput{ProfileID: profile.ID})
	if !errors.Is(err, ErrQAProfileDisabled) {
		t.Fatalf("err=%v want ErrQAProfileDisabled", err)
	}
}

// ---- 1b. 术语表加载失败：fail-closed 中止，不降级清除术语类 issue ----

func TestQARecheck_GlossaryLoadFailureAborts(t *testing.T) {
	client := testClient(t)
	// 关闭底层连接模拟 NewDatabaseGlossary 查询遭遇瞬时 DB 错误（如 database
	// is locked）。此时必须返回错误中止重检：若降级为 nil glossary 继续，术语
	// 类 checker 静默跳过，写回会把既有术语类 issue（含 dismissed 裁决）清除。
	if err := client.Close(); err != nil {
		t.Fatalf("close client: %v", err)
	}
	gl, err := buildRecheckGlossary(context.Background(), client, &ent.Project{ID: 1, GlossaryEnabled: true})
	if err == nil {
		t.Fatalf("want error when glossary load fails (fail-closed), got glossary=%v", gl)
	}
	// 未启用术语表的项目不查询数据库，不受连接状态影响。
	gl, err = buildRecheckGlossary(context.Background(), client, &ent.Project{ID: 1})
	if err != nil || gl != nil {
		t.Fatalf("glossary=%v err=%v want nil, nil when glossary disabled", gl, err)
	}
}

// ---- 2. 基本流：重检后 quality_issues 更新、status/target_text 不变 ----

func TestQARecheck_BasicFlow(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "recheck-basic-user")
	project := createTestProject(t, client, "recheck-basic-proj", user.ID)
	res := createTestResource(t, client, project.ID, "basic.txt")
	// 源文含数字"3"，译文缺失数字 → number_mismatch。
	seg := createTestSegmentWithTarget(t, client, res.ID, 0, "I have 3 cats", "我有三只猫", nil)
	profile := createRecheckProfile(t, client, user.ID, nil)

	svc := newRecheckService(client)
	result, err := svc.Recheck(ctx, user.ID, project.ID, QARecheckInput{ProfileID: profile.ID})
	if err != nil {
		t.Fatalf("Recheck: %v", err)
	}

	if result.ResourcesChecked != 1 || len(result.Resources) != 1 {
		t.Fatalf("resources_checked=%d resources=%d want 1", result.ResourcesChecked, len(result.Resources))
	}
	if result.SegmentsChecked != 1 {
		t.Fatalf("segments_checked=%d want 1", result.SegmentsChecked)
	}
	if result.IssuesNew != 1 {
		t.Fatalf("issues_new=%d want 1 (number_mismatch)", result.IssuesNew)
	}

	updated := client.Segment.GetX(ctx, seg.ID)
	if !hasIssueCode(updated.QualityIssues, "number_mismatch") {
		t.Fatalf("quality_issues=%+v want number_mismatch", updated.QualityIssues)
	}
	// 不修改译文与段落状态。
	if updated.TargetText == nil || *updated.TargetText != "我有三只猫" {
		t.Fatalf("target_text=%v want unchanged", updated.TargetText)
	}
	if updated.Status != segment.StatusTranslated {
		t.Fatalf("status=%q want translated (unchanged)", updated.Status)
	}
}

// ---- 3. dismissed 裁决按指纹继承 ----

func TestQARecheck_DispositionInherited(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "recheck-dismiss-user")
	project := createTestProject(t, client, "recheck-dismiss-proj", user.ID)
	res := createTestResource(t, client, project.ID, "dismiss.txt")
	// 预置同指纹（code=number_mismatch；该 checker 不产 span，指纹 matched_text 为空）
	// 的 dismissed issue。
	dismissed := recheckIssue("number_mismatch", "")
	decidedAt := time.Now()
	dismissed.Disposition = qa.DispositionDismissed
	dismissed.DecidedBy = &user.ID
	dismissed.DecidedAt = &decidedAt
	createTestSegmentWithTarget(t, client, res.ID, 0, "I have 3 cats", "我有三只猫", []qa.QualityIssue{dismissed})
	profile := createRecheckProfile(t, client, user.ID, nil)

	svc := newRecheckService(client)
	result, err := svc.Recheck(ctx, user.ID, project.ID, QARecheckInput{ProfileID: profile.ID})
	if err != nil {
		t.Fatalf("Recheck: %v", err)
	}
	if result.DispositionsInherited != 1 {
		t.Fatalf("dispositions_inherited=%d want 1", result.DispositionsInherited)
	}

	updated := client.Segment.GetX(ctx, res.QuerySegments().OnlyIDX(ctx))
	for _, iss := range updated.QualityIssues {
		if iss.Code == "number_mismatch" {
			if iss.Disposition != qa.DispositionDismissed {
				t.Fatalf("disposition=%q want dismissed (inherited)", iss.Disposition)
			}
			return
		}
	}
	t.Fatalf("quality_issues=%+v want number_mismatch retained", updated.QualityIssues)
}

// ---- 4. 语义 issue 保留 ----

func TestQARecheck_SemanticIssueRetained(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "recheck-semantic-user")
	project := createTestProject(t, client, "recheck-semantic-proj", user.ID)
	res := createTestResource(t, client, project.ID, "semantic.txt")
	// 预置语义 issue（calque，确定性 QA 不维护），同时源文带数字触发确定性 issue。
	semantic := recheckIssue("calque", "")
	createTestSegmentWithTarget(t, client, res.ID, 0, "I have 3 cats", "我有三只猫", []qa.QualityIssue{semantic})
	profile := createRecheckProfile(t, client, user.ID, nil)

	svc := newRecheckService(client)
	if _, err := svc.Recheck(ctx, user.ID, project.ID, QARecheckInput{ProfileID: profile.ID}); err != nil {
		t.Fatalf("Recheck: %v", err)
	}

	updated := client.Segment.GetX(ctx, res.QuerySegments().OnlyIDX(ctx))
	if !hasIssueCode(updated.QualityIssues, "calque") {
		t.Fatalf("quality_issues=%+v want calque retained", updated.QualityIssues)
	}
	if !hasIssueCode(updated.QualityIssues, "number_mismatch") {
		t.Fatalf("quality_issues=%+v want number_mismatch added", updated.QualityIssues)
	}
}

// ---- 5. 消失指纹清除（确定性 issue 不再触发 → 清除）----

func TestQARecheck_VanishedFingerprintCleared(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "recheck-clear-user")
	project := createTestProject(t, client, "recheck-clear-proj", user.ID)
	res := createTestResource(t, client, project.ID, "clear.txt")
	// 预置一个重检不会再触发的确定性 issue（number_mismatch，但源文无数字）。
	stale := recheckIssue("number_mismatch", "9")
	createTestSegmentWithTarget(t, client, res.ID, 0, "Hello world", "你好世界", []qa.QualityIssue{stale})
	profile := createRecheckProfile(t, client, user.ID, nil)

	svc := newRecheckService(client)
	result, err := svc.Recheck(ctx, user.ID, project.ID, QARecheckInput{ProfileID: profile.ID})
	if err != nil {
		t.Fatalf("Recheck: %v", err)
	}
	if result.IssuesCleared != 1 {
		t.Fatalf("issues_cleared=%d want 1", result.IssuesCleared)
	}

	updated := client.Segment.GetX(ctx, res.QuerySegments().OnlyIDX(ctx))
	if len(updated.QualityIssues) != 0 {
		t.Fatalf("quality_issues=%+v want empty (stale cleared)", updated.QualityIssues)
	}
}

// ---- 6. checks 过滤：只配 ["untranslated"] 时其他问题不产出 ----

func TestQARecheck_ChecksFilter(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "recheck-filter-user")
	project := createTestProject(t, client, "recheck-filter-proj", user.ID)
	res := createTestResource(t, client, project.ID, "filter.txt")
	// number_mismatch 本会触发（源文数字 vs 译文无数字），但 checks 只开 untranslated。
	createTestSegmentWithTarget(t, client, res.ID, 0, "I have 3 cats", "我有三只猫", nil)
	profile := createRecheckProfile(t, client, user.ID, func(cfg *schema.ExecutionProfileConfigData) {
		cfg.QA.Checks = []string{"untranslated"}
	})

	svc := newRecheckService(client)
	result, err := svc.Recheck(ctx, user.ID, project.ID, QARecheckInput{ProfileID: profile.ID})
	if err != nil {
		t.Fatalf("Recheck: %v", err)
	}
	if result.IssuesNew != 0 {
		t.Fatalf("issues_new=%d want 0 (number_mismatch filtered out)", result.IssuesNew)
	}
	updated := client.Segment.GetX(ctx, res.QuerySegments().OnlyIDX(ctx))
	if len(updated.QualityIssues) != 0 {
		t.Fatalf("quality_issues=%+v want empty", updated.QualityIssues)
	}
}

// ---- 7. 保护区重建：protect 开启时行内代码不触发标点误报，关闭时报 ----

func TestQARecheck_ProtectRegion(t *testing.T) {
	// 源文 `print("hello")` 为行内代码，译文含全角引号（或代码被保护后不再对比引号）。
	// protect 关闭时：源文引号存在而译文该类引号计数为 0 → punctuation_missing 误报；
	// protect 开启时：代码区被重建为保护区，checker 剥离后不报。
	run := func(t *testing.T, protectEnabled bool) []qa.QualityIssue {
		client := testClient(t)
		ctx := context.Background()
		user := createTestUser(t, client, "recheck-protect-user")
		project := createTestProject(t, client, "recheck-protect-proj", user.ID)
		res := createTestResource(t, client, project.ID, "protect.txt")
		createTestSegmentWithTarget(t, client, res.ID, 0, `调用 print("hello") 打印`, `调用 print（“hello”）打印`, nil)
		profile := createRecheckProfile(t, client, user.ID, func(cfg *schema.ExecutionProfileConfigData) {
			cfg.Protect.Enabled = protectEnabled
		})

		svc := newRecheckService(client)
		if _, err := svc.Recheck(ctx, user.ID, project.ID, QARecheckInput{ProfileID: profile.ID}); err != nil {
			t.Fatalf("Recheck: %v", err)
		}
		return client.Segment.GetX(ctx, res.QuerySegments().OnlyIDX(ctx)).QualityIssues
	}

	t.Run("protect_on_no_false_positive", func(t *testing.T) {
		issues := run(t, true)
		if hasIssueCode(issues, "punctuation_missing") {
			t.Fatalf("quality_issues=%+v want no punctuation_missing (protected region rebuilt)", issues)
		}
	})
	t.Run("protect_off_reports", func(t *testing.T) {
		issues := run(t, false)
		// 反向验证：保护区未重建时，引号类误报（或标点相关误报）会被检出。
		if len(issues) == 0 {
			t.Fatalf("quality_issues empty, want punctuation false positive without protect")
		}
	})
}

// ---- 8. 无译文段跳过（skipped_no_target）----

func TestQARecheck_SkipsNoTarget(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "recheck-notarget-user")
	project := createTestProject(t, client, "recheck-notarget-proj", user.ID)
	res := createTestResource(t, client, project.ID, "notarget.txt")
	createTestSegmentWithTarget(t, client, res.ID, 0, "I have 3 cats", "我有三只猫", nil)
	createTestSegment(t, client, res.ID, 1, "No translation yet", nil)
	profile := createRecheckProfile(t, client, user.ID, nil)

	svc := newRecheckService(client)
	result, err := svc.Recheck(ctx, user.ID, project.ID, QARecheckInput{ProfileID: profile.ID})
	if err != nil {
		t.Fatalf("Recheck: %v", err)
	}
	if result.SegmentsSkippedNoTarget != 1 {
		t.Fatalf("segments_skipped_no_target=%d want 1", result.SegmentsSkippedNoTarget)
	}
	if result.SegmentsChecked != 1 {
		t.Fatalf("segments_checked=%d want 1", result.SegmentsChecked)
	}
}

// ---- 9. 活跃 job 跳过（resources_skipped_busy）----

func TestQARecheck_BusyResourceSkipped(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "recheck-busy-user")
	project := createTestProject(t, client, "recheck-busy-proj", user.ID)
	busyRes := createTestResource(t, client, project.ID, "busy.txt")
	freeRes := createTestResource(t, client, project.ID, "free.txt")
	createTestSegmentWithTarget(t, client, busyRes.ID, 0, "I have 3 cats", "我有三只猫", nil)
	createTestSegmentWithTarget(t, client, freeRes.ID, 0, "I have 3 cats", "我有三只猫", nil)
	profile := createRecheckProfile(t, client, user.ID, nil)

	// 建 pending job + job_resource 指向 busyRes。
	activeJob, err := client.Job.Create().
		SetProjectID(project.ID).
		SetExecutionPlanID(1).
		SetStatus(JobStatusPending).
		SetResourceCount(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := client.JobResource.Create().
		SetStatus(JobResourceStatusPending).
		SetSegmentCount(1).
		SetJob(activeJob).
		SetResource(busyRes).
		Save(ctx); err != nil {
		t.Fatalf("create job_resource: %v", err)
	}

	svc := newRecheckService(client)
	result, err := svc.Recheck(ctx, user.ID, project.ID, QARecheckInput{ProfileID: profile.ID})
	if err != nil {
		t.Fatalf("Recheck: %v", err)
	}
	if len(result.ResourcesSkippedBusy) != 1 {
		t.Fatalf("resources_skipped_busy=%+v want 1 entry", result.ResourcesSkippedBusy)
	}
	if result.ResourcesSkippedBusy[0].ResourceID != busyRes.ID || result.ResourcesSkippedBusy[0].ActiveJobID != activeJob.ID {
		t.Fatalf("busy=%+v want resource %d job %d", result.ResourcesSkippedBusy[0], busyRes.ID, activeJob.ID)
	}
	if result.ResourcesChecked != 1 {
		t.Fatalf("resources_checked=%d want 1 (free resource only)", result.ResourcesChecked)
	}
	// 忙碌资源的段落未被重检：无 issues 写入。
	busySeg := client.Segment.GetX(ctx, busyRes.QuerySegments().OnlyIDX(ctx))
	if len(busySeg.QualityIssues) != 0 {
		t.Fatalf("busy resource quality_issues=%+v want untouched", busySeg.QualityIssues)
	}
}

// ---- 10. 超限（maxRecheckSegments 包级 var，测试调小）----

func TestQARecheck_TooLarge(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "recheck-toolarge-user")
	project := createTestProject(t, client, "recheck-toolarge-proj", user.ID)
	res := createTestResource(t, client, project.ID, "toolarge.txt")
	for i := 0; i < 5; i++ {
		createTestSegmentWithTarget(t, client, res.ID, i, "src", "tgt", nil)
	}
	profile := createRecheckProfile(t, client, user.ID, nil)

	orig := maxRecheckSegments
	maxRecheckSegments = 4
	t.Cleanup(func() { maxRecheckSegments = orig })

	svc := newRecheckService(client)
	_, err := svc.Recheck(ctx, user.ID, project.ID, QARecheckInput{ProfileID: profile.ID})
	if !errors.Is(err, ErrRecheckTooLarge) {
		t.Fatalf("err=%v want ErrRecheckTooLarge", err)
	}
}

// ---- 11. 选择模式：segment_ids 显式选择只检选中段 ----

func TestQARecheck_SegmentIDSelection(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "recheck-select-user")
	project := createTestProject(t, client, "recheck-select-proj", user.ID)
	res := createTestResource(t, client, project.ID, "select.txt")
	seg0 := createTestSegmentWithTarget(t, client, res.ID, 0, "I have 3 cats", "我有三只猫", nil)
	seg1 := createTestSegmentWithTarget(t, client, res.ID, 1, "I have 3 cats", "我有三只猫", nil)
	profile := createRecheckProfile(t, client, user.ID, nil)

	svc := newRecheckService(client)
	result, err := svc.Recheck(ctx, user.ID, project.ID, QARecheckInput{
		ProfileID:  profile.ID,
		SegmentIDs: []int{seg0.ID},
	})
	if err != nil {
		t.Fatalf("Recheck: %v", err)
	}
	if result.SegmentsChecked != 1 {
		t.Fatalf("segments_checked=%d want 1 (explicit selection)", result.SegmentsChecked)
	}
	if result.IssuesNew != 1 {
		t.Fatalf("issues_new=%d want 1", result.IssuesNew)
	}

	// 选中段已更新，未选中段保持无 issues。
	if len(client.Segment.GetX(ctx, seg0.ID).QualityIssues) == 0 {
		t.Fatalf("selected segment want issues")
	}
	if len(client.Segment.GetX(ctx, seg1.ID).QualityIssues) != 0 {
		t.Fatalf("unselected segment quality_issues want empty")
	}
}

// ---- 12. 文档级 duplicate_source_divergence：默认启用、显式 checks 过滤 ----

func TestQARecheck_DuplicateSourceDivergence(t *testing.T) {
	// 两段同源不同译 → 后出现的段落收到 duplicate_source_divergence warning。
	type env struct {
		client  *ent.Client
		userID  int
		project *ent.Project
		profile *ent.ExecutionProfile
	}
	setup := func(t *testing.T, mutate func(cfg *schema.ExecutionProfileConfigData)) *env {
		client := testClient(t)
		user := createTestUser(t, client, "recheck-div-user")
		project := createTestProject(t, client, "recheck-div-proj", user.ID)
		res := createTestResource(t, client, project.ID, "div.txt")
		createTestSegmentWithTarget(t, client, res.ID, 0, "Hello world", "你好", nil)
		createTestSegmentWithTarget(t, client, res.ID, 1, "Hello world", "世界", nil)
		profile := createRecheckProfile(t, client, user.ID, mutate)
		return &env{client: client, userID: user.ID, project: project, profile: profile}
	}

	t.Run("enabled_by_default", func(t *testing.T) {
		e := setup(t, nil)
		svc := newRecheckService(e.client)
		result, err := svc.Recheck(context.Background(), e.userID, e.project.ID, QARecheckInput{ProfileID: e.profile.ID})
		if err != nil {
			t.Fatalf("Recheck: %v", err)
		}
		if result.IssuesNew != 1 {
			t.Fatalf("issues_new=%d want 1 (duplicate_source_divergence)", result.IssuesNew)
		}
		segs, err := e.client.Segment.Query().All(context.Background())
		if err != nil {
			t.Fatalf("query segments: %v", err)
		}
		byIndex := map[int][]qa.QualityIssue{}
		for _, seg := range segs {
			byIndex[seg.SegmentIndex] = seg.QualityIssues
		}
		if hasIssueCode(byIndex[0], qa.CodeDuplicateSourceDivergence) {
			t.Fatalf("first occurrence should be canonical, got divergence issue")
		}
		if !hasIssueCode(byIndex[1], qa.CodeDuplicateSourceDivergence) {
			t.Fatalf("later segment issues=%+v want duplicate_source_divergence", byIndex[1])
		}
	})

	t.Run("filtered_by_explicit_checks", func(t *testing.T) {
		e := setup(t, func(cfg *schema.ExecutionProfileConfigData) {
			cfg.QA.Checks = []string{"untranslated"}
		})
		svc := newRecheckService(e.client)
		result, err := svc.Recheck(context.Background(), e.userID, e.project.ID, QARecheckInput{ProfileID: e.profile.ID})
		if err != nil {
			t.Fatalf("Recheck: %v", err)
		}
		if result.IssuesNew != 0 {
			t.Fatalf("issues_new=%d want 0 (divergence filtered by explicit checks)", result.IssuesNew)
		}
	})
}

// ---- 12b. 部分选择：duplicate_source_divergence 保持全资源作用域 ----

func TestQARecheck_DuplicateSourceDivergencePartialSelection(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "recheck-div-partial-user")
	project := createTestProject(t, client, "recheck-div-partial-proj", user.ID)
	res := createTestResource(t, client, project.ID, "div-partial.txt")
	seg0 := createTestSegmentWithTarget(t, client, res.ID, 0, "Hello world", "你好", nil)
	// 同源不同译的"后出现段"预置 dismissed 裁决：只重检该段时，同源对端段
	// （seg0）不在选中集合，若 divergence 检查误用选中段作用域，该 issue 会
	// 因指纹不再产出而被清除；全资源作用域下应重算出同指纹 issue 并继承裁决。
	dismissed := recheckIssue(qa.CodeDuplicateSourceDivergence, "")
	dismissed.SegmentIndex = 1
	decidedAt := time.Now()
	dismissed.Disposition = qa.DispositionDismissed
	dismissed.DecidedBy = &user.ID
	dismissed.DecidedAt = &decidedAt
	seg1 := createTestSegmentWithTarget(t, client, res.ID, 1, "Hello world", "世界", []qa.QualityIssue{dismissed})
	profile := createRecheckProfile(t, client, user.ID, nil)

	svc := newRecheckService(client)
	result, err := svc.Recheck(ctx, user.ID, project.ID, QARecheckInput{
		ProfileID:  profile.ID,
		SegmentIDs: []int{seg1.ID},
	})
	if err != nil {
		t.Fatalf("Recheck: %v", err)
	}
	if result.SegmentsChecked != 1 {
		t.Fatalf("segments_checked=%d want 1", result.SegmentsChecked)
	}
	if result.IssuesCleared != 0 {
		t.Fatalf("issues_cleared=%d want 0 (divergence still valid at full-resource scope)", result.IssuesCleared)
	}
	if result.DispositionsInherited != 1 {
		t.Fatalf("dispositions_inherited=%d want 1 (dismissed divergence inherited)", result.DispositionsInherited)
	}

	updated := client.Segment.GetX(ctx, seg1.ID)
	found := false
	for _, iss := range updated.QualityIssues {
		if iss.Code == qa.CodeDuplicateSourceDivergence {
			found = true
			if iss.Disposition != qa.DispositionDismissed {
				t.Fatalf("divergence disposition=%q want dismissed (inherited)", iss.Disposition)
			}
		}
	}
	if !found {
		t.Fatalf("selected later segment issues=%+v want duplicate_source_divergence", updated.QualityIssues)
	}
	// 未选中的同源对端段不被写回。
	if issues := client.Segment.GetX(ctx, seg0.ID).QualityIssues; len(issues) != 0 {
		t.Fatalf("unselected segment quality_issues=%+v want empty", issues)
	}
}

// ---- 12c. 部分选择：跨段 duplicate 检查保持全资源作用域 ----

func TestQARecheck_DuplicatePartialSelection(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "recheck-dup-partial-user")
	project := createTestProject(t, client, "recheck-dup-partial-proj", user.ID)
	res := createTestResource(t, client, project.ID, "dup-partial.txt")
	seg0 := createTestSegmentWithTarget(t, client, res.ID, 0, "Hello world", "你好", nil)
	// 不同原文映射到相同译文的"后出现段"预置 dismissed 裁决：只重检该段时，
	// 配对段（seg0）不在选中集合，若 duplicate 检查误用选中段作用域，该 issue
	// 会因指纹不再产出而被清除；全资源作用域下应重算出同指纹 issue 并继承裁决。
	dismissed := recheckIssue(qa.CheckDuplicate, "")
	dismissed.SegmentIndex = 1
	decidedAt := time.Now()
	dismissed.Disposition = qa.DispositionDismissed
	dismissed.DecidedBy = &user.ID
	dismissed.DecidedAt = &decidedAt
	seg1 := createTestSegmentWithTarget(t, client, res.ID, 1, "Goodbye world", "你好", []qa.QualityIssue{dismissed})
	profile := createRecheckProfile(t, client, user.ID, nil)

	svc := newRecheckService(client)
	result, err := svc.Recheck(ctx, user.ID, project.ID, QARecheckInput{
		ProfileID:  profile.ID,
		SegmentIDs: []int{seg1.ID},
	})
	if err != nil {
		t.Fatalf("Recheck: %v", err)
	}
	if result.SegmentsChecked != 1 {
		t.Fatalf("segments_checked=%d want 1", result.SegmentsChecked)
	}
	if result.IssuesCleared != 0 {
		t.Fatalf("issues_cleared=%d want 0 (duplicate still valid at full-resource scope)", result.IssuesCleared)
	}
	if result.DispositionsInherited != 1 {
		t.Fatalf("dispositions_inherited=%d want 1 (dismissed duplicate inherited)", result.DispositionsInherited)
	}

	updated := client.Segment.GetX(ctx, seg1.ID)
	found := false
	for _, iss := range updated.QualityIssues {
		if iss.Code == qa.CheckDuplicate {
			found = true
			if iss.Disposition != qa.DispositionDismissed {
				t.Fatalf("duplicate disposition=%q want dismissed (inherited)", iss.Disposition)
			}
		}
	}
	if !found {
		t.Fatalf("selected later segment issues=%+v want duplicate", updated.QualityIssues)
	}
	// 未选中的配对段不被写回。
	if issues := client.Segment.GetX(ctx, seg0.ID).QualityIssues; len(issues) != 0 {
		t.Fatalf("unselected segment quality_issues=%+v want empty", issues)
	}
}

// ---- 12d. 全选：引擎（选中段）与全资源重算的 duplicate 结果去重合并，不重复计数 ----

func TestQARecheck_DuplicateFullSelectionNoDoubleCount(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "recheck-dup-full-user")
	project := createTestProject(t, client, "recheck-dup-full-proj", user.ID)
	res := createTestResource(t, client, project.ID, "dup-full.txt")
	createTestSegmentWithTarget(t, client, res.ID, 0, "Hello world", "你好", nil)
	createTestSegmentWithTarget(t, client, res.ID, 1, "Goodbye world", "你好", nil)
	profile := createRecheckProfile(t, client, user.ID, nil)

	svc := newRecheckService(client)
	result, err := svc.Recheck(ctx, user.ID, project.ID, QARecheckInput{ProfileID: profile.ID})
	if err != nil {
		t.Fatalf("Recheck: %v", err)
	}
	// 全资源范围内 duplicate issue 指纹恒定（code:matched_text 无 span），
	// 引擎与全资源重算的产出按指纹去重后只能计一次。
	if result.IssuesNew != 1 {
		t.Fatalf("issues_new=%d want 1 (single duplicate issue, deduplicated)", result.IssuesNew)
	}
	if result.ResourcesChecked != 1 || result.Resources[0].ResourceID != res.ID {
		t.Fatalf("resources=%+v want resource %d", result.Resources, res.ID)
	}
}

// ---- 13. running 状态的忙碌资源与"全部忙碌"场景 ----

func TestQARecheck_RunningJobBusy(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "recheck-running-user")
	project := createTestProject(t, client, "recheck-running-proj", user.ID)
	res := createTestResource(t, client, project.ID, "running.txt")
	createTestSegmentWithTarget(t, client, res.ID, 0, "I have 3 cats", "我有三只猫", nil)
	profile := createRecheckProfile(t, client, user.ID, nil)

	runningJob, err := client.Job.Create().
		SetProjectID(project.ID).
		SetExecutionPlanID(1).
		SetStatus(JobStatusRunning).
		SetResourceCount(1).
		Save(ctx)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := client.JobResource.Create().
		SetStatus(JobResourceStatusRunning).
		SetSegmentCount(1).
		SetJob(runningJob).
		SetResource(res).
		Save(ctx); err != nil {
		t.Fatalf("create job_resource: %v", err)
	}

	svc := newRecheckService(client)
	result, err := svc.Recheck(ctx, user.ID, project.ID, QARecheckInput{ProfileID: profile.ID})
	if err != nil {
		t.Fatalf("Recheck (all busy): %v", err)
	}
	if result.ResourcesChecked != 0 || len(result.Resources) != 0 {
		t.Fatalf("resources_checked=%d resources=%d want 0 (all busy)", result.ResourcesChecked, len(result.Resources))
	}
	if len(result.ResourcesSkippedBusy) != 1 || result.ResourcesSkippedBusy[0].ActiveJobID != runningJob.ID {
		t.Fatalf("resources_skipped_busy=%+v want running job %d", result.ResourcesSkippedBusy, runningJob.ID)
	}
}

// ---- 14. 守恒类 issue（ruby_tag_loss）保留 ----

func TestQARecheck_ConservationIssueRetained(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "recheck-conservation-user")
	project := createTestProject(t, client, "recheck-conservation-proj", user.ID)
	res := createTestResource(t, client, project.ID, "conservation.txt")
	// 预置守恒类 issue（ruby_tag_loss，由写路径维护），同时源文带数字触发确定性 issue。
	conservation := recheckIssue(qa.CodeRubyTagLoss, "<ruby>")
	createTestSegmentWithTarget(t, client, res.ID, 0, "I have 3 cats", "我有三只猫", []qa.QualityIssue{conservation})
	profile := createRecheckProfile(t, client, user.ID, nil)

	svc := newRecheckService(client)
	result, err := svc.Recheck(ctx, user.ID, project.ID, QARecheckInput{ProfileID: profile.ID})
	if err != nil {
		t.Fatalf("Recheck: %v", err)
	}
	if result.IssuesCleared != 0 {
		t.Fatalf("issues_cleared=%d want 0 (conservation issue must not be cleared)", result.IssuesCleared)
	}
	updated := client.Segment.GetX(ctx, res.QuerySegments().OnlyIDX(ctx))
	if !hasIssueCode(updated.QualityIssues, qa.CodeRubyTagLoss) {
		t.Fatalf("quality_issues=%+v want ruby_tag_loss retained", updated.QualityIssues)
	}
	if !hasIssueCode(updated.QualityIssues, "number_mismatch") {
		t.Fatalf("quality_issues=%+v want number_mismatch added", updated.QualityIssues)
	}
}

// ---- 15. 幂等重跑：指纹不变 → 零新增、零清除 ----

func TestQARecheck_IdempotentRerun(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "recheck-idempotent-user")
	project := createTestProject(t, client, "recheck-idempotent-proj", user.ID)
	res := createTestResource(t, client, project.ID, "idempotent.txt")
	createTestSegmentWithTarget(t, client, res.ID, 0, "I have 3 cats", "我有三只猫", nil)
	profile := createRecheckProfile(t, client, user.ID, nil)

	svc := newRecheckService(client)
	first, err := svc.Recheck(ctx, user.ID, project.ID, QARecheckInput{ProfileID: profile.ID})
	if err != nil {
		t.Fatalf("Recheck #1: %v", err)
	}
	if first.IssuesNew != 1 {
		t.Fatalf("first run issues_new=%d want 1", first.IssuesNew)
	}
	second, err := svc.Recheck(ctx, user.ID, project.ID, QARecheckInput{ProfileID: profile.ID})
	if err != nil {
		t.Fatalf("Recheck #2: %v", err)
	}
	if second.IssuesNew != 0 || second.IssuesCleared != 0 {
		t.Fatalf("second run new=%d cleared=%d want 0/0 (idempotent)", second.IssuesNew, second.IssuesCleared)
	}
	if second.SegmentsChecked != first.SegmentsChecked {
		t.Fatalf("second run segments_checked=%d want %d", second.SegmentsChecked, first.SegmentsChecked)
	}
}

// ---- 16. group_keys 无效输入 → ErrInvalidInput（4xx 而非 500）----

func TestQARecheck_InvalidGroupKeys(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	user := createTestUser(t, client, "recheck-groupkeys-user")
	project := createTestProject(t, client, "recheck-groupkeys-proj", user.ID)
	res := createTestResource(t, client, project.ID, "groupkeys.txt")
	createTestSegmentWithTarget(t, client, res.ID, 0, "I have 3 cats", "我有三只猫", nil)
	profile := createRecheckProfile(t, client, user.ID, nil)

	svc := newRecheckService(client)
	for name, keys := range map[string][]string{
		"blank":    {"  "},
		"no_match": {"OEBPS/chapter999.xhtml"},
	} {
		_, err := svc.Recheck(ctx, user.ID, project.ID, QARecheckInput{
			ProfileID:        profile.ID,
			SegmentGroupKeys: keys,
		})
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("%s: err=%v want ErrInvalidInput", name, err)
		}
	}
}
