package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

type inventoryScanBarrier struct {
	db interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	}
	entered, release chan struct{}
	once             sync.Once
}

func TestBudgetIntegrationCancelUnknownAuxRetryAttach(t *testing.T) {
	for _, body := range []bool{false, true} {
		t.Run(fmt.Sprintf("reliable_body_%t", body), func(t *testing.T) {
			s, _, actor := configurationConsumerServer(t, config.ModeLocal, nil)
			ctx := t.Context()
			profile := execution.DefaultProfile()
			spec, err := execution.Resolve(execution.JobExecutionSnapshot{SourceLang: "en", TargetLang: "zh", RubyTemplates: execution.RubyTemplates{JSON: "json", Text: "text", BatchJSON: "batch json", BatchText: "batch text"}, Strategy: execution.StrategySnapshot{Protect: profile.Protect, Postprocess: profile.Postprocess, Repair: profile.Repair, Context: profile.Context, Ruby: profile.Ruby, QA: profile.QA}, Rounds: []execution.JobRoundSnapshot{{Mode: "correct", Correct: &execution.JobCorrectRoundSnapshot{Concurrency: 1}}}})
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(spec)
			if err != nil {
				t.Fatal(err)
			}
			var frozen map[string]any
			if err = json.Unmarshal(raw, &frozen); err != nil {
				t.Fatal(err)
			}
			c := s.entClient
			p := c.Project.Create().SetName("cancel-unknown").SetOwnerUserID(actor.ID).SaveX(ctx)
			r := c.Resource.Create().SetProjectID(p.ID).SetPath("cancel.txt").SetStoragePath("cancel.txt").SetFormat("txt").SaveX(ctx)
			j := c.Job.Create().SetProjectID(p.ID).SetCreatedByID(actor.ID).SetExecutionPlanID(1).SetExecutionConfig(frozen).SetStatus(service.JobStatusPaused).SaveX(ctx)
			jr := c.JobResource.Create().SetJobID(j.ID).SetResourceID(r.ID).SetStatus(service.JobResourceStatusRunning).SaveX(ctx)
			round := c.JobRound.Create().SetJobID(j.ID).SetJobResourceID(jr.ID).SetRoundIndex(0).SetMode("correct").SetStatus(service.JobRoundStatusRunning).SaveX(ctx)
			builder := c.WorkRequest.Create().SetIdentity("unknown-aux").SetJobID(j.ID).SetResourceID(r.ID).SetJobRoundID(round.ID).SetRetryEpoch(0).SetSegmentIds([]int{1}).SetStage("alignment").SetBackendID(0).SetBudgetModel("budget").SetInputDigest("opaque").SetState("unknown").SetUsageKnown(false)
			if body {
				builder.SetFreezePayload([]byte("reliable freeze")).SetFreezeBytes(15)
			}
			builder.SaveX(ctx)
			if err = s.executionBudget.Rebuild(ctx, s.budgetSource); err != nil {
				t.Fatal(err)
			}
			before := s.executionBudget.Snapshot(actor.ID).Instance
			if before.PayloadBytes <= 0 || before.LogicalStorageBytes <= 0 {
				t.Fatalf("missing unresolved metadata: %+v", before)
			}
			if _, err = s.jobSvc.CancelJob(ctx, actor.ID, j.ID); err != nil {
				t.Fatal(err)
			}
			actual, storage, err := s.budgetSource.Job(ctx, j.ID)
			if err != nil {
				t.Fatal(err)
			}
			actual.LogicalStorageBytes = storage
			if actual.PayloadBytes != 0 || actual.LogicalStorageBytes <= 0 || s.executionBudget.Snapshot(actor.ID).Instance != actual {
				t.Fatalf("cancel root=%+v facts=%+v", s.executionBudget.Snapshot(actor.ID), actual)
			}
			old := c.WorkRequest.Query().OnlyX(ctx)
			if old.State != "unknown" || old.UsageKnown || old.InputTokens != 0 || old.OutputTokens != 0 || old.Identity != "unknown-aux" || old.RetryEpoch != 0 || old.PromotionState != "cancelled" {
				t.Fatalf("audit changed: %+v", old)
			}
			result, err := s.jobSvc.RetryJob(ctx, actor.ID, j.ID)
			if err != nil {
				t.Fatal(err)
			}
			s.jobSvc.ReleaseExecutionControlResult(result)
			if meta, err := s.budgetSource.Metadata(ctx, j.ID); err != nil || meta != 0 {
				t.Fatalf("old cancelled audit revived: meta=%d err=%v", meta, err)
			}
			actorCtx := service.WithExecutionActor(ctx, actor.ID)
			defer service.ReleaseExecutionActor(actorCtx)
			if err = s.prepareExecutionActor(actorCtx, actor.ID); err != nil {
				t.Fatal(err)
			}
			admission, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 1}})
			if err != nil {
				t.Fatal(err)
			}
			defer admission.Close()
			if err = admission.ConfigurePipelineBudget(backend.PipelineBudgetLimits{Segments: 8, Candidates: 1 << 20, Item: 1 << 20, Responses: 1 << 20, Handoff: 8 << 20}); err != nil {
				t.Fatal(err)
			}
			n, candidate, growth, handoff, err := s.budgetSource.Runtime(ctx, j.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err = admission.RestoreInventory(n, candidate, growth, handoff); err != nil {
				t.Fatal(err)
			}
			if err = admission.RestoreRootMetadata(0); err != nil {
				t.Fatal(err)
			}
			baseline := s.executionBudget.Snapshot(actor.ID).Instance
			if err = admission.AttachRootBudget(s.executionBudget, resourcebudget.Scope{ActorID: actor.ID, JobID: j.ID}); err != nil {
				t.Fatal(err)
			}
			if err = admission.ChangeStation(1, 0, 1); err != nil {
				t.Fatal(err)
			}
			admission.Close()
			if got := s.executionBudget.Snapshot(actor.ID).Instance; got != baseline || got != actual {
				t.Fatalf("retry attach duplicated audit: got=%+v baseline=%+v facts=%+v", got, baseline, actual)
			}
			// Retry keeps the old attempt identity and epoch; an old response
			// cannot create new reliable backing under the new Job epoch.
			err = workstate.NewStore(c).HandoffResponse(ctx, workstate.ResponseHandoff{RequestID: old.Identity, Scope: workstate.Scope{JobID: j.ID, ResourceID: r.ID, RoundID: round.ID, RetryEpoch: 0}, FreezePayload: []byte("late"), ResponsePayload: []byte("late"), SnapshotDigest: "late"})
			if err == nil || !errors.Is(err, workstate.ErrStopped) {
				t.Fatalf("late old-epoch response: %v", err)
			}
			if got := s.executionBudget.Snapshot(actor.ID).Instance; got != baseline {
				t.Fatalf("late response changed budget: %+v", got)
			}
			t.Logf("cancel/retry/attach preserve unknown audit; retained storage=%d, RAM=0", storage)
		})
	}
}

func (b *inventoryScanBarrier) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	if strings.Contains(q, "AND (CASE WHEN creator.id") {
		b.once.Do(func() {
			close(b.entered)
			select {
			case <-b.release:
			case <-ctx.Done():
			}
		})
	}
	return b.db.QueryContext(ctx, q, args...)
}

func TestBudgetIntegrationActorScanAndInactiveCleanup(t *testing.T) {
	for _, operation := range []string{"cancel", "delete"} {
		t.Run(operation, func(t *testing.T) {
			s, c, u := historyAPIServer(t)
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			p := jobQueryProject(t, c, u.ID)
			status := "completed"
			if operation == "cancel" {
				status = "paused"
			}
			j := jobQuerySeed(t, c, p.ID, status, "manual", time.Now().UTC())
			c.Job.UpdateOne(j).SetCreatedByID(u.ID).SetExecutionConfig(map[string]any{"schema_version": 4, "defaults_version": 4, "response_format_version": 1}).ExecX(ctx)
			r := c.Resource.Create().SetProjectID(p.ID).SetPath("budget.txt").SetFormat("txt").SetStoragePath("budget.txt").SaveX(ctx)
			jr := c.JobResource.Create().SetJobID(j.ID).SetResourceID(r.ID).SaveX(ctx)
			round := c.JobRound.Create().SetJobID(j.ID).SetJobResourceID(jr.ID).SetRoundIndex(0).SetMode("translate").SaveX(ctx)
			payload := []byte("可靠冻结文本")
			c.WorkRequest.Create().SetIdentity("race-request").SetJobID(j.ID).SetResourceID(r.ID).SetJobRoundID(round.ID).SetRetryEpoch(0).SetSegmentIds([]int{1}).SetStage("main").SetBackendID(0).SetBudgetModel("budget").SetInputDigest("digest").SetFreezePayload(payload).SetFreezeBytes(int64(len(payload))).SetState("sent").SaveX(ctx)
			s.budgetSource = workstate.InventorySource{DB: c, Driver: "sqlite", ItemBytes: 1 << 20}
			var err error
			s.executionBudget, err = resourcebudget.New(testServerConfig().ExecutionBudget)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.executionBudget.Rebuild(ctx, s.budgetSource); err != nil {
				t.Fatal(err)
			}
			before := s.executionBudget.Snapshot(0).Instance
			barrier := &inventoryScanBarrier{db: c, entered: make(chan struct{}), release: make(chan struct{})}
			s.budgetSource.DB = barrier
			s.jobSvc.SetBudgetCancellation(s.prepareCancelledBudget)
			s.taskHistory.SetBudgetDeletion(s.prepareBudgetDeletion)
			scanCtx := service.WithExecutionActor(ctx, u.ID)
			defer service.ReleaseExecutionActor(scanCtx)
			scanDone := make(chan error, 1)
			go func() { scanDone <- s.prepareExecutionActor(scanCtx, u.ID) }()
			select {
			case <-barrier.entered:
			case <-ctx.Done():
				t.Fatal("actor scan did not enter")
			}
			cleanupDone := make(chan error, 1)
			go func() {
				if operation == "cancel" {
					_, err := s.jobSvc.CancelJob(ctx, u.ID, j.ID)
					cleanupDone <- err
				} else {
					cleanupDone <- s.taskHistory.Delete(ctx, u.ID, service.HistoryTarget{Kind: service.OperationTranslation, ID: fmt.Sprint(j.ID), ProjectID: p.ID})
				}
			}()
			select {
			case err := <-cleanupDone:
				close(barrier.release)
				t.Fatalf("cleanup crossed incomplete actor scan: %v", err)
			case <-time.After(40 * time.Millisecond):
			}
			close(barrier.release)
			if err := <-scanDone; err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-cleanupDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("cleanup did not resume")
			}
			actual, bytes, err := s.budgetSource.Job(ctx, j.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := actual
			want.LogicalStorageBytes = bytes
			if operation == "delete" {
				if _, err := c.Job.Get(ctx, j.ID); !ent.IsNotFound(err) {
					t.Fatalf("deleted job still present: %v", err)
				}
				if want != (resourcebudget.Vector{}) {
					t.Fatalf("deleted job retained inventory: %+v", want)
				}
			}
			got := s.executionBudget.Snapshot(u.ID)
			if got.Instance != want || got.Actor != want {
				t.Fatalf("lost or duplicated cleanup credit: root=%+v actual=%+v", got, want)
			}
			if before.PayloadBytes <= want.PayloadBytes || before.LogicalStorageBytes <= want.LogicalStorageBytes {
				t.Fatalf("cleanup did not remove reliable backing before=%+v after=%+v", before, want)
			}
			rebuilt, err := resourcebudget.New(testServerConfig().ExecutionBudget)
			if err != nil {
				t.Fatal(err)
			}
			if err := rebuilt.Rebuild(ctx, s.budgetSource); err != nil {
				t.Fatal(err)
			}
			if rebuilt.Snapshot(0).Instance != want {
				t.Fatalf("cleanup credit differs from durable rebuild: live=%+v rebuilt=%+v", want, rebuilt.Snapshot(0).Instance)
			}
			t.Logf("%s serialized behind actor scan; retained S/P/storage=%+v", operation, want)
		})
	}
}
