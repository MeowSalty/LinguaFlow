package progress

import (
	"context"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

func TestDBReporterStageObservationAndCorrelation(t *testing.T) {
	client := weightedTestClient(t)
	jobID, jrID := progressFixture(t, client)
	client.Job.UpdateOneID(jobID).SetProgressCompleted(4).ExecX(context.Background())
	roundID := createRoundRow(t, client, jobID, jrID, 0, "translate")
	resourceID := client.JobResource.GetX(context.Background(), jrID).QueryResource().OnlyX(context.Background()).ID
	for index := 0; index < 4; index++ {
		seg := client.Segment.Create().SetResourceID(resourceID).SetSegmentIndex(index).SetSourceText("source").SaveX(context.Background())
		client.JobRoundSegment.Create().SetJobRoundID(roundID).SetSegmentID(seg.ID).ExecX(context.Background())
	}
	client.JobRound.UpdateOneID(roundID).SetSegmentTotal(5).SetSegmentCompleted(4).ExecX(context.Background())
	request := client.WorkRequest.Create().SetIdentity("saving-main").SetJobID(jobID).SetResourceID(resourceID).SetJobRoundID(roundID).
		SetRetryEpoch(0).SetSegmentIds([]int{}).SetStage("main").SetBackendID(1).SetBudgetModel("stage_separated").SetInputDigest("frozen").SetState("received").SaveX(context.Background())
	broker := event.NewBroker(nil)
	events := broker.Subscribe(jobID)
	defer broker.Unsubscribe(jobID, events)
	r := NewDBReporter(DBReporterOptions{Client: client, JobID: jobID, JobResourceID: jrID, Broker: broker, Ticker: time.Hour})
	t.Cleanup(func() { _ = r.Close() })
	NotifyWorkState(r)
	select {
	case evt := <-events:
		counts, ok := evt.Metadata["stages"].(*workstate.StageCounts)
		if evt.Type != "stage_counts" || !ok || counts.ConfirmedWork != 4 || counts.SavingRequests != 1 || counts.ReadyToCommit != 0 || counts.AsOf.IsZero() {
			t.Fatalf("incorrect persisted observation: %+v", evt)
		}
	default:
		t.Fatal("durable-state notification produced no event")
	}
	client.WorkRequest.UpdateOne(request).SetState("completed").ExecX(context.Background())
	NotifyWorkState(r)
	select {
	case evt := <-events:
		counts, ok := evt.Metadata["stages"].(*workstate.StageCounts)
		if evt.Type != "stage_counts" || !ok || counts.SavingRequests != 0 || counts.ConfirmedWork != 4 {
			t.Fatalf("completion observation changed confirmed work or retained saving request: %+v", evt)
		}
	default:
		t.Fatal("completed request produced no updated stage observation")
	}
	r.OnBatchEvent(BatchEvent{Stage: "ruby_alignment", Status: "partial", SegmentIDs: []string{"s1"}, SegmentCount: 1,
		ParentRequestID: "main-request", CandidateID: "candidate", CandidateVersion: 3, LogicalAttempt: 2, NetworkAttempt: 1, VerifiedItems: 2, MissingItems: 1})
	select {
	case evt := <-events:
		for field, want := range map[string]any{"parent_request_id": "main-request", "candidate_id": "candidate", "candidate_version": int64(3), "logical_attempt": 2, "network_attempt": 1, "verified_items": 2, "missing_items": 1} {
			if evt.Metadata[field] != want {
				t.Errorf("%s=%v, want %v", field, evt.Metadata[field], want)
			}
		}
	default:
		t.Fatal("alignment diagnostics produced no event")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	NotifyWorkState(r)
	for len(events) > 0 {
		evt := <-events
		if evt.Type == "stage_counts" {
			t.Fatalf("closed reporter published stage event: %+v", evt)
		}
	}
	NotifyWorkState(Nop{})
}
