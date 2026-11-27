package worker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

type promptUpgradeArchiveStop struct{ *roundStore }

func (*promptUpgradeArchiveStop) PublishOutputs(context.Context, pipeline.OutputPublication) (pipeline.MainResponseHeader, error) {
	return pipeline.MainResponseHeader{}, permanentStoreError{errors.New("stop after provider capture")}
}

func TestStagedPromptUpgradeRecoversSQLAndPublishesGlossary(t *testing.T) {
	f, provider := newHandoffHTTPFixture(t, "translate", 1, 1, 1)
	provider.open()
	var calls atomic.Int32
	provider.transform = func(content any) any {
		if calls.Add(1) == 1 {
			return map[string]any{"malformed": true}
		}
		result := content.(map[string]any)
		result["glossary"] = []map[string]string{{"source": "sample term", "target": "术语"}}
		return result
	}
	inline := execution.DefaultInlineTermExtraction()
	inline.Enabled = true
	f.snapshot.Rounds[0].Translate.InlineTermExtraction = &inline
	f.snapshot.Rounds[0].Translate.Retry.MaxAttempts = 0
	f.snapshot.GlossaryEnabled = true
	f.snapshot.Strategy.Repair.Enabled, f.snapshot.Strategy.Repair.PromptUpgrade = true, true
	f.snapshot.RetryReminderTemplate = repair.DefaultRetryReminderTemplate
	f.snapshot = completeWorkerSnapshot(t, f.snapshot)
	f.persistSnapshot(t)
	ctx := service.WithExecutionJobActor(t.Context(), f.ownerID, f.jobID)
	project := f.client.Job.QueryProject(f.client.Job.GetX(ctx, f.jobID)).OnlyX(ctx)
	dictionary, err := service.NewDatabaseGlossary(ctx, f.client, project)
	if err != nil {
		t.Fatal(err)
	}
	execute := func(stop bool) (pipeline.RunRoundResult, error) {
		runtime, err := NewExecutionRuntime(f.snapshot, f.pool, nil, pipeline.DefaultCandidateLimits())
		if err != nil {
			return pipeline.RunRoundResult{}, err
		}
		defer runtime.Close()
		executionCtx := pipeline.WithExecutionRuntime(ctx, runtime)
		eng, err := f.factory.BuildEngine(executionCtx, f.snapshot, engine.RuntimeResources{Glossary: dictionary}, progress.Nop{})
		if err != nil {
			return pipeline.RunRoundResult{}, err
		}
		defer eng.Close()
		doc, base := f.round(t, 0)
		defer runtime.CloseDocument(doc)
		base.publicationProjectID = project.ID
		eng.PrepareDocument(doc, nil)
		var store pipeline.RoundStore = base
		if stop {
			store = &promptUpgradeArchiveStop{base}
		}
		return pipeline.RunStagedRound(executionCtx, eng.Rounds()[0], doc, store, runtime, f.logger, progress.Nop{})
	}
	if _, err := execute(true); err == nil || calls.Load() != 1 {
		t.Fatalf("first execution calls=%d err=%v", calls.Load(), err)
	}
	first := f.client.WorkRequest.Query().Where(workrequest.JobIDEQ(f.jobID)).OnlyX(ctx)
	if first.PromotionState != "capture_pending" || first.ResponseDigest == "" || first.Stage != "main" {
		t.Fatalf("first capture did not persist: %+v", first)
	}
	result, err := execute(false)
	if err != nil || len(result.Resolved) != 1 || calls.Load() != 2 {
		t.Fatalf("recovery calls=%d result=%+v err=%v", calls.Load(), result, err)
	}
	rows := f.client.WorkRequest.Query().Where(workrequest.JobIDEQ(f.jobID)).Order(ent.Asc(workrequest.FieldID)).AllX(ctx)
	if len(rows) != 2 || rows[0].ResponseDigest != first.ResponseDigest || rows[1].Stage != workstate.MainPromptUpgradeStage || rows[0].LogicalAttempt != 1 || rows[1].LogicalAttempt != 1 || rows[1].PromotionState != "promoted" || len(rows[1].GlossaryReceipt) == 0 {
		t.Fatalf("retry did not preserve the two independent request facts: %+v", rows)
	}
	if rows[0].UsageRecordID == nil || rows[1].UsageRecordID == nil || *rows[0].UsageRecordID == *rows[1].UsageRecordID || !rows[0].UsageKnown || !rows[1].UsageKnown {
		t.Fatalf("retry did not record independent usage: %+v", rows)
	}
	if got := f.client.GlossaryEntry.Query().CountX(ctx); got != 1 {
		t.Fatalf("glossary entries=%d, want 1", got)
	}
}

type promptUpgradeReservationStop struct{ *roundStore }

func (s *promptUpgradeReservationStop) Reserve(ctx context.Context, intent pipeline.RequestIntent) error {
	if err := s.roundStore.Reserve(ctx, intent); err != nil {
		return err
	}
	if intent.PromptUpgrade {
		return permanentStoreError{errors.New("process stopped after durable upgrade reservation")}
	}
	return nil
}

func TestStagedPromptUpgradeRecoveryDoesNotRepeatUnknownInvocation(t *testing.T) {
	f := newRubyPipelineFixture(t, 1, 1)
	f.respond = func(_ context.Context, _ rubyPipelineRequest) (any, error) {
		return map[string]any{"malformed": true}, nil
	}
	f.snapshot.Rounds[0].Translate.Retry.MaxAttempts = 0
	f.snapshot.Strategy.Repair.Enabled, f.snapshot.Strategy.Repair.PromptUpgrade = true, true
	f.snapshot.RetryReminderTemplate = repair.DefaultRetryReminderTemplate
	f.snapshot = completeWorkerSnapshot(t, f.snapshot)
	f.persistSnapshot(t)
	if _, _, err := f.execute(t, 0, nil, func(s *roundStore) pipeline.RoundStore { return &promptUpgradeReservationStop{s} }); err == nil {
		t.Fatal("expected stop after upgrade debit")
	}
	if err := workstate.MarkUnknown(t.Context(), f.client, f.jobID); err != nil {
		t.Fatal(err)
	}
	_, result, err := f.execute(t, 0, nil, nil)
	if err != nil || len(result.Unresolved) != 1 {
		t.Fatalf("recovered unknown upgrade: result=%+v err=%v", result, err)
	}
	rows := f.client.WorkRequest.Query().Where(workrequest.JobIDEQ(f.jobID)).Order(ent.Asc(workrequest.FieldID)).AllX(t.Context())
	if len(rows) != 2 || rows[0].State != "completed" || rows[1].Stage != workstate.MainPromptUpgradeStage || rows[1].State != "unknown" || rows[1].UsageRecordID != nil {
		t.Fatalf("unexpected recovered request ledger: %+v", rows)
	}
	f.mu.Lock()
	calls := len(f.requests)
	f.mu.Unlock()
	if calls != 1 {
		t.Fatalf("physical calls=%d, want one known dispatched call", calls)
	}
}
