package service

import (
	"context"
	"errors"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

// seedJobCancelRetry 直接经 ent 构造一个指定状态的 Job 及若干指定状态的 JobResource，
// 绕过 CreateManualJob 的快照逻辑，专门用于 CancelJob/RetryJob 的状态前置校验测试。
func seedJobCancelRetry(t *testing.T, client *ent.Client, projectID int, jobStatus string, resourceStatuses []string) (*ent.Job, []*ent.JobResource) {
	t.Helper()
	res := createTestResource(t, client, projectID, "job.txt")
	job, err := client.Job.Create().
		SetProjectID(projectID).
		SetExecutionPlanID(1).
		SetStatus(jobStatus).
		SetResourceCount(len(resourceStatuses)).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	jrs := make([]*ent.JobResource, 0, len(resourceStatuses))
	for i, st := range resourceStatuses {
		jr, err := client.JobResource.Create().
			SetStatus(st).
			SetJob(job).
			SetResource(res).
			Save(context.Background())
		if err != nil {
			t.Fatalf("create jr[%d]: %v", i, err)
		}
		jrs = append(jrs, jr)
	}
	return job, jrs
}

func TestCancelJob_StatePrecondition(t *testing.T) {
	tests := []struct {
		name         string
		jobStatus    string
		resStatuses  []string
		wantErr      error
		wantJobAfter string
		wantResAfter string // 期望首个资源的事后状态；仅成功用例校验
	}{
		{name: "pending ok", jobStatus: JobStatusPending, resStatuses: []string{JobResourceStatusPending}, wantJobAfter: JobStatusCancelled, wantResAfter: JobResourceStatusCancelled},
		{name: "running ok", jobStatus: JobStatusRunning, resStatuses: []string{JobResourceStatusRunning}, wantJobAfter: JobStatusCancelled, wantResAfter: JobResourceStatusCancelled},
		{name: "paused ok", jobStatus: JobStatusPaused, resStatuses: []string{JobResourceStatusRunning}, wantJobAfter: JobStatusCancelled, wantResAfter: JobResourceStatusCancelled},
		{name: "completed rejected", jobStatus: JobStatusCompleted, resStatuses: []string{JobResourceStatusCompleted}, wantErr: ErrJobNotCancellable},
		{name: "failed rejected", jobStatus: JobStatusFailed, resStatuses: []string{JobResourceStatusFailed}, wantErr: ErrJobNotCancellable},
		{name: "cancelled rejected", jobStatus: JobStatusCancelled, resStatuses: []string{JobResourceStatusCancelled}, wantErr: ErrJobNotCancellable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := testClient(t)
			ctx := context.Background()
			user := createTestUser(t, client, "cancel-"+tt.name)
			project := createTestProject(t, client, "cancel-proj-"+tt.name, user.ID)
			projects := NewProjectService(client, NewUserService(client, NewAuthService(client, AuthConfig{}, NewAdminService(client))))
			svc := &JobService{client: client, projects: projects}
			job, jrs := seedJobCancelRetry(t, client, project.ID, tt.jobStatus, tt.resStatuses)

			got, err := svc.CancelJob(ctx, user.ID, job.ID)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("CancelJob: %v", err)
			}
			if got.Status != tt.wantJobAfter {
				t.Errorf("job status = %q, want %q", got.Status, tt.wantJobAfter)
			}
			after, err := client.JobResource.Get(ctx, jrs[0].ID)
			if err != nil {
				t.Fatalf("get jr: %v", err)
			}
			if after.Status != tt.wantResAfter {
				t.Errorf("resource status = %q, want %q", after.Status, tt.wantResAfter)
			}
		})
	}
}

func TestRetryJob_StatePrecondition(t *testing.T) {
	tests := []struct {
		name         string
		jobStatus    string
		resStatuses  []string
		wantErr      error
		wantJobAfter string
		wantResAfter string // 期望首个资源的事后状态；仅成功用例校验
	}{
		{name: "failed with failed resource ok", jobStatus: JobStatusFailed, resStatuses: []string{JobResourceStatusFailed}, wantJobAfter: JobStatusPending, wantResAfter: JobResourceStatusPending},
		{name: "failed with cancelled resource ok", jobStatus: JobStatusFailed, resStatuses: []string{JobResourceStatusCancelled}, wantJobAfter: JobStatusPending, wantResAfter: JobResourceStatusPending},
		{name: "cancelled with cancelled resource ok", jobStatus: JobStatusCancelled, resStatuses: []string{JobResourceStatusCancelled}, wantJobAfter: JobStatusPending, wantResAfter: JobResourceStatusPending},
		{name: "completed rejected", jobStatus: JobStatusCompleted, resStatuses: []string{JobResourceStatusCompleted}, wantErr: ErrJobNotRetryable},
		{name: "pending rejected", jobStatus: JobStatusPending, resStatuses: []string{JobResourceStatusPending}, wantErr: ErrJobNotRetryable},
		{name: "paused rejected", jobStatus: JobStatusPaused, resStatuses: []string{JobResourceStatusRunning}, wantErr: ErrJobNotRetryable},
		{name: "failed without retryable resource rejected", jobStatus: JobStatusFailed, resStatuses: []string{JobResourceStatusCompleted}, wantErr: ErrJobNoFailedResource},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := testClient(t)
			ctx := context.Background()
			user := createTestUser(t, client, "retry-"+tt.name)
			project := createTestProject(t, client, "retry-proj-"+tt.name, user.ID)
			projects := NewProjectService(client, NewUserService(client, NewAuthService(client, AuthConfig{}, NewAdminService(client))))
			svc := &JobService{client: client, projects: projects}
			job, jrs := seedJobCancelRetry(t, client, project.ID, tt.jobStatus, tt.resStatuses)

			got, err := svc.RetryJob(ctx, user.ID, job.ID)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("RetryJob: %v", err)
			}
			if got.Status != tt.wantJobAfter {
				t.Errorf("job status = %q, want %q", got.Status, tt.wantJobAfter)
			}
			after, err := client.JobResource.Get(ctx, jrs[0].ID)
			if err != nil {
				t.Fatalf("get jr: %v", err)
			}
			if after.Status != tt.wantResAfter {
				t.Errorf("resource status = %q, want %q", after.Status, tt.wantResAfter)
			}
		})
	}
}
