package worker

import (
	"context"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobresource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

// TestRoundConcurrency 从快照提取各模式轮的站位容量（per-mode concurrency）；
// 模式配置为 nil 或索引越界时回退 1。
func TestRoundConcurrency(t *testing.T) {
	snapshot := &service.JobExecutionSnapshot{
		Rounds: []service.JobRoundSnapshot{
			{Mode: "translate", Translate: &service.JobTranslateRoundSnapshot{Concurrency: 4}},
			{Mode: "extract", Extract: &service.JobExtractRoundSnapshot{Concurrency: 3}},
			{Mode: "adjudicate", Adjudicate: &service.JobAdjudicateRoundSnapshot{Concurrency: 5}},
			{Mode: "semantic_qa", SemanticQA: &service.JobSemanticQARoundSnapshot{Concurrency: 2}},
			{Mode: "revise", Revise: &service.JobReviseRoundSnapshot{Concurrency: 6}},
			{Mode: "correct", Correct: &service.JobCorrectRoundSnapshot{Concurrency: 7}},
			{Mode: "translate"}, // 模式配置为 nil → 回退 1
		},
	}

	cases := []struct {
		name string
		idx  int
		want int
	}{
		{"translate", 0, 4},
		{"extract", 1, 3},
		{"adjudicate", 2, 5},
		{"semantic_qa", 3, 2},
		{"revise", 4, 6},
		{"correct", 5, 7},
		{"nil config fallback", 6, 1},
		{"negative index", -1, 1},
		{"out of range", len(snapshot.Rounds), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := roundConcurrency(snapshot, tc.idx); got != tc.want {
				t.Fatalf("roundConcurrency(%d) = %d, want %d", tc.idx, got, tc.want)
			}
		})
	}
}

// TestComputeWorkWeight_ByteBased computeWorkWeight 经 service.SumResourceWorkWeight
// 按字节（非字符）统计源文本："abc" = 3 字节，"中文" = 每汉字 UTF-8 3 字节共 6
// 字节 → 总 9。
func TestComputeWorkWeight_ByteBased(t *testing.T) {
	client := newRegistryTestClient(t)
	ctx := context.Background()
	jobID, _, _ := registryFixture(t, client)

	job, err := client.Job.Get(ctx, jobID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}

	res, err := client.Resource.Create().
		SetProjectID(job.ProjectID).
		SetPath("weight.txt").
		SetFormat("txt").
		SetStoragePath("storage/weight.txt").
		Save(ctx)
	if err != nil {
		t.Fatalf("create resource: %v", err)
	}

	// ASCII 3 字节 + CJK 6 字节（2 汉字 × 3 字节）。
	for i, text := range []string{"abc", "中文"} {
		if _, err := client.Segment.Create().
			SetResourceID(res.ID).
			SetSegmentIndex(i).
			SetSourceText(text).
			Save(ctx); err != nil {
			t.Fatalf("create segment %d: %v", i, err)
		}
	}

	created, err := client.JobResource.Create().
		SetStatus("pending").
		SetJob(job).
		SetResource(res).
		Save(ctx)
	if err != nil {
		t.Fatalf("create job_resource: %v", err)
	}
	// computeWorkWeight 读 Edges.ResourceOrErr()：与生产一致，从 DB 连边加载
	//（ent 创建返回值不自动携带已加载边）。
	items, err := client.JobResource.Query().
		Where(jobresource.IDEQ(created.ID)).
		WithResource().
		All(ctx)
	if err != nil {
		t.Fatalf("query job_resource with resource edge: %v", err)
	}
	item := items[0]

	runner := &JobRunner{client: client}
	weight, err := runner.computeWorkWeight(ctx, item)
	if err != nil {
		t.Fatalf("computeWorkWeight: %v", err)
	}
	if weight != 9 {
		t.Fatalf("work weight = %d, want 9（3 + 2×3 字节，按字节而非字符计）", weight)
	}
}

// TestRoundAbandoned 欠账为空或有后续同模式轮承接时不算放弃；有欠账且无
// 承接者（该模式最后一轮）才算真实放弃。欠账形态分别覆盖仅未解决、仅终态
// 失败、两者兼有。
func TestRoundAbandoned(t *testing.T) {
	cases := []struct {
		name         string
		residual     roundResidual
		hasSuccessor bool
		want         bool
	}{
		{"欠账为空", roundResidual{}, false, false},
		{"欠账为空且有承接者", roundResidual{}, true, false},
		{"仅未解决段且有承接者", roundResidual{unresolved: 2}, true, false},
		{"仅终态失败段且有承接者", roundResidual{failed: 3}, true, false},
		{"两类欠账兼有且有承接者", roundResidual{unresolved: 1, failed: 2}, true, false},
		{"仅未解决段且无承接者", roundResidual{unresolved: 2}, false, true},
		{"仅终态失败段且无承接者", roundResidual{failed: 3}, false, true},
		{"两类欠账兼有且无承接者", roundResidual{unresolved: 1, failed: 2}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := roundAbandoned(tc.residual, tc.hasSuccessor); got != tc.want {
				t.Fatalf("roundAbandoned(%+v, hasSuccessor=%v) = %v, want %v",
					tc.residual, tc.hasSuccessor, got, tc.want)
			}
		})
	}
}

// TestResidualWarning 四个软警告模式产出非空文案，计数取未解决与终态失败的
// 和（unresolved=2、failed=3 → 5；取 max 会得到 3，取单项会得到 2 或 3）；
// 欠账为空或无话术的模式（translate/correct/未知）返回空串。文案不含
// 「可重试任务」——软警告只挂在 completed 资源上，而 completed 资源不可重试。
func TestResidualWarning(t *testing.T) {
	cases := []struct {
		name     string
		mode     string
		residual roundResidual
		empty    bool
		contains []string
	}{
		{"semantic_qa 取和", pipeline.RoundModeSemanticQA, roundResidual{unresolved: 2, failed: 3}, false, []string{"语义质检", "5"}},
		{"revise 取和", pipeline.RoundModeRevise, roundResidual{unresolved: 2, failed: 3}, false, []string{"修订", "5"}},
		{"adjudicate 取和", pipeline.RoundModeAdjudicate, roundResidual{unresolved: 2, failed: 3}, false, []string{"质量裁决", "5"}},
		{"extract 取和", pipeline.RoundModeExtract, roundResidual{unresolved: 2, failed: 3}, false, []string{"术语抽取", "5"}},
		{"semantic_qa 仅终态失败", pipeline.RoundModeSemanticQA, roundResidual{failed: 4}, false, []string{"4"}},
		{"semantic_qa 欠账为空", pipeline.RoundModeSemanticQA, roundResidual{}, true, nil},
		{"translate 返回空串", pipeline.RoundModeTranslate, roundResidual{unresolved: 2, failed: 3}, true, nil},
		{"correct 返回空串", pipeline.RoundModeCorrect, roundResidual{unresolved: 2, failed: 3}, true, nil},
		{"未知模式返回空串", "bogus_mode", roundResidual{unresolved: 2, failed: 3}, true, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := residualWarning(tc.mode, tc.residual)
			if tc.empty {
				if got != "" {
					t.Fatalf("residualWarning(%q, %+v) = %q, want 空串", tc.mode, tc.residual, got)
				}
				return
			}
			if got == "" {
				t.Fatalf("residualWarning(%q, %+v) = 空串, want 非空", tc.mode, tc.residual)
			}
			for _, s := range tc.contains {
				if !strings.Contains(got, s) {
					t.Fatalf("residualWarning(%q, %+v) = %q, want 包含 %q", tc.mode, tc.residual, got, s)
				}
			}
			// 计数必须是和而非 max/单项：unresolved=2、failed=3 时只允许出现 5，
			// 文案其余部分不含数字，出现 2 或 3 即口径错误。
			for _, wrong := range []string{"2", "3", "可重试任务"} {
				if strings.Contains(got, wrong) {
					t.Fatalf("residualWarning(%q, %+v) = %q, 不应包含 %q（和而非 max/单项、不承诺可重试）", tc.mode, tc.residual, got, wrong)
				}
			}
		})
	}
}
