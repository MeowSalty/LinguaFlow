package worker

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/jobroundsegment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func TestStagedSQLitePoolProgressAndPartialResult(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	f := newRubyPipelineFixture(t, 3)
	f.snapshot.Rounds[0].Translate.Retry.MaxAttempts = 1
	f.snapshot.Rounds[0].Translate.FallbackShrink = 0.5
	f.persistSnapshot(t)
	f.respond = func(_ context.Context, req rubyPipelineRequest) (any, error) {
		if req.Segments == nil {
			return rubyPipelineResponse(req), nil
		}
		translations := make(map[string]string)
		for id, seg := range req.Segments {
			if seg.Translate && !strings.Contains(seg.Source, "segment-0-1") {
				translations[id] = "alpha beta"
			}
		}
		return map[string]any{"translations": translations}, nil
	}
	events := f.runner.eventBroker.Subscribe(f.jobID)
	defer f.runner.eventBroker.Unsubscribe(f.jobID, events)
	if err := f.runner.processJob(ctx, f.jobID); err != nil {
		t.Fatal(err)
	}
	var got [][4]any
	for len(events) > 0 {
		e := <-events
		if e.Type == "pool" && e.Stage == "translate" {
			got = append(got, [4]any{e.Metadata["phase"], e.Metadata["pool_index"], e.Metadata["batches"], e.Metadata["pending"]})
		}
	}
	want := [][4]any{{"pool_start", 0, 2, 3}, {"pool_advance", 0, 2, 1}, {"pool_start", 1, 1, 1}}
	if !slices.Equal(got, want) {
		t.Fatalf("pool events = %v, want %v", got, want)
	}
	round := f.client.JobRound.GetX(ctx, f.roundIDs[0])
	resource := f.client.JobResource.GetX(ctx, f.jobResources[0])
	confirmed := f.client.JobRoundSegment.Query().Where(jobroundsegment.JobRoundIDEQ(round.ID)).CountX(ctx)
	if round.Status != service.JobRoundStatusFailed || round.SegmentCompleted != 2 || confirmed != 2 || resource.Status != service.JobResourceStatusFailed || resource.CompletedSegments != 2 {
		t.Fatalf("round status=%q completed=%d, confirmations=%d, resource status=%q completed=%d", round.Status, round.SegmentCompleted, confirmed, resource.Status, resource.CompletedSegments)
	}
}
