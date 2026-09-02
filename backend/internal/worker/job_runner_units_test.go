package worker

import (
	"context"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobresource"
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
