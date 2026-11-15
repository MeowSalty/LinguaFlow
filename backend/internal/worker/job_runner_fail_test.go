package worker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobresource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

// pendingDefinitionFixture 构造定义预检必定失败的 pending 任务：响应交接
// 上限压到 1KiB，使 loadJobDefinition 在字节上限处返回
// backend.ErrBuildCapacity（确定性永久错误），且不进入 MarkJobRunning。
func pendingDefinitionFixture(t *testing.T) *rubyPipelineFixture {
	t.Helper()
	f := newRubyPipelineFixture(t, 1, 1)
	f.client.Job.UpdateOneID(f.jobID).SetStatus(service.JobStatusPending).ExecX(t.Context())
	f.runner.pipeCfg.ResponseHandoffBytes = 1024
	return f
}

func TestProcessJobPermanentDefinitionErrorIsTerminal(t *testing.T) {
	f := pendingDefinitionFixture(t)
	ctx := t.Context()

	err := f.runner.processJob(ctx, f.jobID)
	if !errors.Is(err, backend.ErrBuildCapacity) {
		t.Fatalf("definition failure = %v, want %v", err, backend.ErrBuildCapacity)
	}
	jobRow := f.client.Job.GetX(ctx, f.jobID)
	if jobRow.Status != service.JobStatusFailed {
		t.Fatalf("job status = %q, want %q", jobRow.Status, service.JobStatusFailed)
	}
	if jobRow.ErrorMessage == nil || !strings.Contains(*jobRow.ErrorMessage, backend.ErrBuildCapacity.Error()) {
		t.Fatalf("job error message = %v, want capacity detail", jobRow.ErrorMessage)
	}
	if jobRow.FinishedAt == nil || jobRow.RetentionAnchorAt == nil {
		t.Fatalf("terminal failure left finished_at=%v retention_anchor_at=%v", jobRow.FinishedAt, jobRow.RetentionAnchorAt)
	}
	resources := f.client.JobResource.Query().Where(jobresource.HasJobWith(job.IDEQ(f.jobID))).AllX(ctx)
	if len(resources) != 2 {
		t.Fatalf("job resources = %d, want 2", len(resources))
	}
	for _, item := range resources {
		if item.Status != service.JobResourceStatusFailed {
			t.Fatalf("job resource %d status = %q, want %q", item.ID, item.Status, service.JobResourceStatusFailed)
		}
	}

	// 永久失败不消耗重试能力：资源级前置条件（failed 资源）成立即可翻回 pending。
	retried, err := f.jobs.RetryJob(ctx, f.ownerID, f.jobID)
	if err != nil {
		t.Fatalf("retry after permanent definition failure: %v", err)
	}
	if retried.Status != service.JobStatusPending {
		t.Fatalf("retried status = %q, want %q", retried.Status, service.JobStatusPending)
	}
	if retried.ErrorMessage != nil {
		t.Fatalf("retried job kept error message %q", *retried.ErrorMessage)
	}
}

func TestProcessJobCancelledContextKeepsPendingState(t *testing.T) {
	f := pendingDefinitionFixture(t)
	ctx := t.Context()

	runCtx, cancel := context.WithCancel(ctx)
	cancel()
	err := f.runner.processJob(runCtx, f.jobID)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled run error = %v, want %v", err, context.Canceled)
	}
	if status := f.client.Job.GetX(ctx, f.jobID).Status; status != service.JobStatusPending {
		t.Fatalf("job status = %q, want %q", status, service.JobStatusPending)
	}
	for _, item := range f.client.JobResource.Query().Where(jobresource.HasJobWith(job.IDEQ(f.jobID))).AllX(ctx) {
		if item.Status != service.JobResourceStatusPending {
			t.Fatalf("job resource %d status = %q, want %q", item.ID, item.Status, service.JobResourceStatusPending)
		}
	}
}

func derefString(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

// TestProcessJobResourceLoadFailureIsTerminal 覆盖资源置 running 之后、
// loadCanonicalDocument 之前的失败（历史缺陷：直接 return err 导致资源与
// job 永远卡 running，轮次全 pending、进度 0/0，且 RetryJob 拒绝）。
// 触发方式：段 meta 嵌套深度 80 超过 MaxBuildDepth(64)，loadExecutionSegments
// 返回 backend.ErrBuildCapacity——失败点完全落在 loadCanonicalDocument 路径。
// 第二资源预置 completed 保证 ReconcileJob 能推导出 failed 终态。
func TestProcessJobResourceLoadFailureIsTerminal(t *testing.T) {
	f := newRubyPipelineFixture(t, 1, 0)
	ctx := t.Context()
	deep := strings.Repeat(`{"a":`, 80) + `0` + strings.Repeat(`}`, 80)
	f.client.Segment.Update().Where(segment.ResourceIDEQ(f.resourceIDs[0])).SetMeta(deep).ExecX(ctx)

	job, err := f.client.Job.Get(ctx, f.jobID)
	if err != nil {
		t.Fatal(err)
	}
	project, err := f.client.Project.Get(ctx, job.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	item, err := f.client.JobResource.Query().Where(jobresource.IDEQ(f.jobResources[0])).WithResource().Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := loadJobRounds(ctx, f.client, f.jobID)
	if err != nil {
		t.Fatal(err)
	}
	err = f.runner.processJobResource(ctx, &service.JobExecution{Job: job, Project: project}, f.snapshot, item, registry, nil, nil)
	if err != nil {
		t.Fatalf("load failure must not escape with the resource left running: %v", err)
	}
	item = f.client.JobResource.GetX(ctx, f.jobResources[0])
	if item.Status != service.JobResourceStatusFailed {
		t.Fatalf("resource status = %q, want %q", item.Status, service.JobResourceStatusFailed)
	}
	if item.ErrorMessage == nil || !strings.Contains(*item.ErrorMessage, backend.ErrBuildCapacity.Error()) {
		t.Fatalf("resource error message = %s, want capacity detail", derefString(item.ErrorMessage))
	}
	if err = f.jobs.ReconcileJob(ctx, f.jobID); err != nil {
		t.Fatal(err)
	}
	jobRow := f.client.Job.GetX(ctx, f.jobID)
	if jobRow.Status != service.JobStatusFailed {
		t.Fatalf("job status = %q, want %q", jobRow.Status, service.JobStatusFailed)
	}
	if jobRow.FinishedAt == nil {
		t.Fatal("failed job left finished_at nil")
	}
	// 终态 failed 恢复 RetryJob 的前置条件（存在 failed 资源）。
	if _, err = f.jobs.RetryJob(ctx, f.ownerID, f.jobID); err != nil {
		t.Fatalf("retry after resource load failure: %v", err)
	}
}
