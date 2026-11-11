package service

import (
	"context"
	"encoding/json"
	framework "entgo.io/ent"
	"errors"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
	"slices"
	"strings"
	"testing"
)

func TestExecutionControlScalarDiscoveryAndOldFormat(t *testing.T) {
	env := newJobRoundTestEnv(t, nil)
	ctx := context.Background()
	good, _, _ := seedJobWithRounds(t, env, JobStatusRunning, 0, 0, []jobResourceSpec{{status: JobResourceStatusRunning, rounds: []jobRoundSpec{{mode: "translate", status: JobRoundStatusRunning}}}})
	bad, _ := seedJobCancelRetry(t, env.client, env.project.ID, JobStatusPending, []string{JobResourceStatusPending})
	header := "{\"schema_version\":\"" + strings.Repeat("x", 128<<10) + "\",\"defaults_version\":4,\"response_format_version\":1,\"rounds\":[]}"
	if _, err := env.client.ExecContext(ctx, "UPDATE jobs SET execution_config=$1 WHERE id=$2", header, bad.ID); err != nil {
		t.Fatal(err)
	}
	env.client.Job.Intercept(ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(ctx context.Context, q ent.Query) (ent.Value, error) {
			fields := framework.QueryFromContext(ctx).Fields
			if len(fields) == 0 || slices.Contains(fields, job.FieldExecutionConfig) || slices.Contains(fields, job.FieldPipelineRuntimeState) {
				t.Errorf("discovery/recovery loaded JSON fields: %v", fields)
			}
			return next.Query(ctx, q)
		})
	}))
	if err := env.svc.CheckExecutionFormat(ctx, bad.ID); !errors.Is(err, execution.ErrUnsupportedExecutionFormat) {
		t.Fatalf("oversized header: %v", err)
	}
	if err := env.svc.PrepareRecovery(ctx); err != nil {
		t.Fatal(err)
	}
	ids, err := env.svc.PendingTaskIDs(ctx, 0, 128)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(ids, good.ID) || slices.Contains(ids, bad.ID) {
		t.Fatalf("ids %v", ids)
	}
}

func TestExecutionControlDefinitionRejectsBeforeAllocation(t *testing.T) {
	for _, kind := range []string{"bytes", "depth", "nodes", "rounds", "old_format"} {
		t.Run(kind, func(t *testing.T) {
			env := newJobRoundTestEnv(t, nil)
			ctx := context.Background()
			row, _, _ := seedJobWithRounds(t, env, JobStatusFailed, 0, 0, []jobResourceSpec{{status: JobResourceStatusFailed}})
			row = env.client.Job.GetX(ctx, row.ID)
			config := row.ExecutionConfig
			switch kind {
			case "bytes":
				config["extension"] = strings.Repeat("x", 8192)
				env.svc.controlBudget.limit = 4096
			case "depth":
				var deep any = "leaf"
				for i := 0; i < 70; i++ {
					deep = map[string]any{"child": deep}
				}
				config["extension"] = deep
			case "nodes":
				config["extension"] = make([]int, 70000)
			case "rounds":
				original := config["rounds"].([]any)
				v := make([]any, 257)
				for i := range v {
					v[i] = original[0]
				}
				config["rounds"] = v
			case "old_format":
				config["schema_version"] = 3
			}
			env.client.Job.UpdateOneID(row.ID).SetExecutionConfig(config).ExecX(ctx)
			resourceReads := 0
			env.client.JobResource.Intercept(ent.InterceptFunc(func(next ent.Querier) ent.Querier {
				return ent.QuerierFunc(func(ctx context.Context, q ent.Query) (ent.Value, error) {
					fields := framework.QueryFromContext(ctx).Fields
					v, err := next.Query(ctx, q)
					if _, ok := v.([]*ent.JobResource); ok && (len(fields) == 0 || slices.Contains(fields, "segment_ids")) {
						resourceReads++
					}
					return v, err
				})
			}))
			_, err := env.svc.RetryJob(ctx, env.user.ID, row.ID)
			if err == nil {
				t.Fatal("invalid definition accepted")
			}
			if kind == "old_format" && !errors.Is(err, execution.ErrUnsupportedExecutionFormat) {
				t.Fatalf("format: %v", err)
			}
			if resourceReads != 0 {
				t.Fatalf("full resource reads before rejection: %d", resourceReads)
			}
			if after := env.client.Job.Query().Where(job.IDEQ(row.ID)).Select(job.FieldStatus).OnlyX(ctx); after.Status != JobStatusFailed {
				t.Fatalf("mutated status: %s", after.Status)
			}
		})
	}
}

func TestExecutionControlRootResultOwnership(t *testing.T) {
	env := newJobRoundTestEnv(t, nil)
	ctx := context.Background()
	row, _, _ := seedJobWithRounds(t, env, JobStatusPaused, 0, 0, []jobResourceSpec{{status: JobResourceStatusRunning, rounds: []jobRoundSpec{{mode: "translate", status: JobRoundStatusRunning}}}})
	root := inputTestRoot(t)
	env.svc.SetExecutionControlBudget(root, func(ctx context.Context, actor int) error {
		return PrepareExecutionActor(ctx, func() (func(), error) {
			// New test roots start empty; this zero inventory scan mirrors actor preparation.
			if err := root.RebuildActor(ctx, actor, emptyControlSource{}); err != nil {
				return nil, err
			}
			return root.PinActor(actor)
		})
	}, DefaultExecutionInputBytes)
	result, err := env.svc.ResumeJob(ctx, env.user.ID, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	held := root.Snapshot(env.user.ID).Instance
	if held.PayloadBytes <= 0 || held.QueuedDescriptors <= 0 || held.BuildWorkers != 0 || held.PrepareOwners != 0 {
		t.Fatalf("result not retained after service: %+v", held)
	}
	if err := env.svc.RunExecutionControlBuild(result, func() error {
		if root.Snapshot(env.user.ID).Instance.BuildWorkers != 1 {
			return errors.New("missing result build owner")
		}
		_, err := json.Marshal(result.ExecutionConfig)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	env.svc.ReleaseExecutionControlResult(result)
	env.svc.ReleaseExecutionControlResult(result)
	if left := root.Snapshot(env.user.ID).Instance; left != (resourcebudget.Vector{}) {
		t.Fatalf("result leaked: %+v", left)
	}
}

type emptyControlSource struct{}

func (emptyControlSource) Scan(context.Context, func(resourcebudget.Record) error) error { return nil }

func TestExecutionControlStartupMissingMatrixDeepDefinition(t *testing.T) {
	env := newJobRoundTestEnv(t, nil)
	ctx := context.Background()
	row, _, _ := seedJobWithRounds(t, env, JobStatusRunning, 0, 0, []jobResourceSpec{{status: JobResourceStatusRunning}})
	row = env.client.Job.GetX(ctx, row.ID)
	var nested any = "leaf"
	for i := 0; i < 70; i++ {
		nested = map[string]any{"child": nested}
	}
	row.ExecutionConfig["extension"] = nested
	env.client.Job.UpdateOneID(row.ID).SetExecutionConfig(row.ExecutionConfig).ExecX(ctx)
	if err := env.svc.PrepareRecovery(ctx); err == nil {
		t.Fatal("deep startup definition accepted")
	}
	if n := env.client.JobRound.Query().CountX(ctx); n != 0 {
		t.Fatalf("allocated matrix %d", n)
	}
	if status := env.client.Job.Query().Where(job.IDEQ(row.ID)).Select(job.FieldStatus).OnlyX(ctx).Status; status != JobStatusRunning {
		t.Fatalf("mutated startup: %s", status)
	}
}

func TestExecutionControlDescriptorLoweringBeforeDefinition(t *testing.T) {
	env := newJobRoundTestEnv(t, nil)
	ctx := context.Background()
	row, _, _ := seedJobWithRounds(t, env, JobStatusFailed, 0, 0, []jobResourceSpec{{status: JobResourceStatusFailed}})
	limits := resourcebudget.Limits{MaxUnconfirmedMembers: 4, MaxPayloadBytes: 2 << 30, MaxPrepareOwners: 1, MaxMainResponseOwners: 1, MaxAuxResponseOwners: 1, MaxBuildWorkers: 1, MaxQueuedDescriptors: 1, MaxLogicalStorageBytes: 2 << 30}
	root, err := resourcebudget.New(resourcebudget.Policy{Actor: limits, Instance: limits})
	if err != nil {
		t.Fatal(err)
	}
	env.svc.SetExecutionControlBudget(root, nil, DefaultExecutionInputBytes)
	_, err = env.svc.RetryJob(ctx, env.user.ID, row.ID)
	if !errors.Is(err, resourcebudget.ErrCapacity) && !errors.Is(err, backend.ErrBuildCapacity) {
		t.Fatalf("lowered descriptors: %v", err)
	}
	if root.Snapshot(env.user.ID).Instance != (resourcebudget.Vector{}) {
		t.Fatal("partial claim leaked")
	}
}

func TestExecutionControlRetryEpochUsesFixedScalarPages(t *testing.T) {
	env := newJobRoundTestEnv(t, nil)
	ctx := context.Background()
	row, jrs, rounds := seedJobWithRounds(t, env, JobStatusFailed, 0, 0, []jobResourceSpec{{status: JobResourceStatusFailed, rounds: []jobRoundSpec{{mode: "translate", status: JobRoundStatusFailed}}}})
	resourceID := env.client.JobResource.GetX(ctx, jrs[0].ID).QueryResource().OnlyX(ctx).ID
	for i := 0; i < 257; i++ {
		seg := env.client.Segment.Create().SetResourceID(resourceID).SetSegmentIndex(i).SetSourceText("source").SaveX(ctx)
		env.client.WorkItem.Create().SetJobID(row.ID).SetResourceID(resourceID).SetJobRoundID(rounds[0][0].ID).SetSegmentID(seg.ID).SetState("pending").SetCursor(json.RawMessage(`{"discarded":"` + strings.Repeat("x", 8192) + `"}`)).SaveX(ctx)
	}
	pages := 0
	env.client.WorkItem.Intercept(ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(ctx context.Context, q ent.Query) (ent.Value, error) {
			fields := framework.QueryFromContext(ctx).Fields
			if slices.Contains(fields, workitem.FieldCursor) {
				t.Error("retry read Cursor")
			}
			v, err := next.Query(ctx, q)
			if rows, ok := v.([]*ent.WorkItem); ok {
				if len(rows) > 128 {
					t.Errorf("page %d", len(rows))
				}
				pages++
			}
			return v, err
		})
	}))
	result, err := env.svc.RetryJob(ctx, env.user.ID, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	env.svc.ReleaseExecutionControlResult(result)
	if pages < 3 {
		t.Fatalf("pages %d", pages)
	}
	if n := env.client.WorkItem.Query().Where(workitem.RetryEpochEQ(1), workitem.StateEQ("pending")).CountX(ctx); n != 257 {
		t.Fatalf("retry facts %d", n)
	}
}
