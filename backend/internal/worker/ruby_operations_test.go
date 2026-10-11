package worker

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func checkRubyOperation(ctx context.Context, f *rubyPipelineFixture, status string) error {
	for _, state := range []string{"", "active"} {
		page, err := service.NewOperationQueryService(f.client).List(ctx, f.ownerID, service.OperationListOptions{
			AccessibleJobListOptions: service.AccessibleJobListOptions{State: state},
		})
		if err != nil {
			return err
		}
		if len(page.Items) != 1 || page.Items[0].Job == nil || page.Items[0].Job.ID != f.jobID || page.Items[0].Job.Status != status {
			return fmt.Errorf("state=%q: operation lost %s job: %+v", state, status, page)
		}
	}
	summary, err := service.NewOperationQueryService(f.client).Summary(ctx, f.ownerID, service.OperationSummaryOptions{})
	if err != nil {
		return err
	}
	want := service.OperationCounts{}
	if status == service.JobStatusPausing {
		want.Pausing = 1
	} else {
		want.Paused = 1
	}
	if summary.Total != want || summary.ByType.Translation != want {
		return fmt.Errorf("operation counts=%+v, want %+v", summary, want)
	}
	return nil
}

func TestRubyOperationsVisibleThroughHTTPAndExitDrain(t *testing.T) {
	f := newRubyPipelineFixture(t, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	requestEntered, releaseRequest := make(chan struct{}), make(chan struct{})
	exitEntered, releaseExit := make(chan struct{}), make(chan struct{})
	var requestOnce, requestRelease, exitOnce, exitRelease sync.Once
	f.respond = func(ctx context.Context, request rubyPipelineRequest) (any, error) {
		requestOnce.Do(func() { close(requestEntered) })
		select {
		case <-releaseRequest:
			return rubyPipelineResponse(request), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	// Pause completion is held before the final UPDATE starts, so queries can
	// observe the fully saved result without holding a database transaction.
	f.client.Job.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
			if status, _ := mutation.Field("status"); status == service.JobStatusPaused {
				exitOnce.Do(func() { close(exitEntered) })
				select {
				case <-releaseExit:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return next.Mutate(ctx, mutation)
		})
	})
	done, stopped := make(chan error, 1), make(chan struct{})
	go func() {
		defer close(stopped)
		done <- f.runner.processJob(ctx, f.jobID)
	}()
	t.Cleanup(func() {
		cancel()
		requestRelease.Do(func() { close(releaseRequest) })
		exitRelease.Do(func() { close(releaseExit) })
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			t.Error("worker did not leave the test barriers")
		}
	})
	select {
	case <-requestEntered:
	case <-ctx.Done():
		t.Fatal("request barrier:", ctx.Err())
	}
	paused, err := f.jobs.PauseJob(ctx, f.ownerID, f.jobID)
	if err != nil || !paused.NeedsDrain || !f.runner.Pause(f.jobID) {
		t.Fatalf("pause=%+v err=%v", paused, err)
	}
	if err := checkRubyOperation(ctx, f, service.JobStatusPausing); err != nil {
		t.Fatal(err)
	}
	stages, err := f.jobs.GetJobStageCounts(ctx, f.ownerID, f.jobID)
	if err != nil || stages.MainRequests != 1 || stages.DrainingRequests != 1 {
		t.Fatalf("HTTP drain=%+v err=%v", stages, err)
	}
	requestRelease.Do(func() { close(releaseRequest) })
	select {
	case <-exitEntered:
	case <-ctx.Done():
		t.Fatal("exit barrier:", ctx.Err())
	}
	stages, err = f.jobs.GetJobStageCounts(ctx, f.ownerID, f.jobID)
	if err != nil || stages.DrainingRequests != 0 || stages.PendingAlignment != 1 {
		t.Fatalf("saved but not yet paused=%+v err=%v", stages, err)
	}
	if err := checkRubyOperation(ctx, f, service.JobStatusPausing); err != nil {
		t.Fatal(err)
	}
	exitRelease.Do(func() { close(releaseExit) })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := checkRubyOperation(ctx, f, service.JobStatusPaused); err != nil {
		t.Fatal(err)
	}
	if main, alignment, _, _ := f.calls(); main != 1 || alignment != 0 {
		t.Fatalf("pause admitted new HTTP: main=%d alignment=%d", main, alignment)
	}
	f.assertUsage(t, 1)
}

func TestRubyOperationsVisibleWhileSavingCandidate(t *testing.T) {
	f := newRubyPipelineFixture(t, 1)
	gate := pipeline.NewPauseGate()
	observed := false
	_, _, err := f.execute(t, 0, gate, func(store *roundStore) pipeline.RoundStore {
		return &rubyPipelineFaultStore{roundStore: store, afterSave: func(ctx context.Context, _ *pipeline.Candidate) error {
			if _, err := f.jobs.PauseJob(ctx, f.ownerID, f.jobID); err != nil {
				return err
			}
			gate.Pause()
			// The save acknowledgement has not returned. Network completion and
			// a durable draft alone must not hide this task from discovery.
			stages, err := f.jobs.GetJobStageCounts(ctx, f.ownerID, f.jobID)
			if err != nil {
				return err
			}
			if stages.MainRequests != 0 || stages.SavingRequests != 1 || stages.DrainingRequests != 1 || stages.PendingAlignment != 1 {
				return fmt.Errorf("saving barrier stages=%+v", stages)
			}
			observed = true
			return checkRubyOperation(ctx, f, service.JobStatusPausing)
		}}
	})
	if err != nil || !observed {
		t.Fatalf("saving observation=%v err=%v", observed, err)
	}
	if main, alignment, _, _ := f.calls(); main != 1 || alignment != 0 {
		t.Fatalf("saving pause admitted new HTTP: %d/%d", main, alignment)
	}
}
