package service

import (
	"context"
	"reflect"
	"testing"
)

func TestPendingDiscoverySkipsOldFormatAcrossPageBoundary(t *testing.T) {
	env := newJobRoundTestEnv(t, nil)
	ctx := context.Background()
	for range 130 {
		row, _, _ := seedJobWithRounds(t, env, JobStatusPending, 0, 0, nil)
		config := row.ExecutionConfig
		config["schema_version"], config["defaults_version"] = 3, 3
		env.client.Job.UpdateOneID(row.ID).SetExecutionConfig(config).ExecX(ctx)
	}
	current, _, _ := seedJobWithRounds(t, env, JobStatusPending, 0, 0, nil)
	ids, err := env.svc.PendingTaskIDs(ctx, 0, 1)
	if err != nil || !reflect.DeepEqual(ids, []int{current.ID}) {
		t.Fatalf("old page hid current job: %v %v", ids, err)
	}
	if err := env.svc.PrepareRecovery(ctx); err != nil {
		t.Fatal(err)
	}
	ids, err = env.svc.PendingTaskIDs(ctx, 0, 1)
	if err != nil || !reflect.DeepEqual(ids, []int{current.ID}) {
		t.Fatalf("recovery changed format filtering: %v %v", ids, err)
	}
}
