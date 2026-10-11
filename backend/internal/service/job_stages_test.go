package service

import (
	"context"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
)

func TestPausePublishesDurableStageObservation(t *testing.T) {
	ctx := context.Background()
	broker := event.NewBroker(nil)
	env := newJobRoundTestEnv(t, broker)
	row, _, _ := seedJobWithRounds(t, env, JobStatusRunning, 10, 4, []jobResourceSpec{{status: JobResourceStatusRunning, segmentCount: 10,
		rounds: []jobRoundSpec{{roundIndex: 0, mode: "translate", status: JobRoundStatusRunning, total: 10, completed: 4, resolvedCount: 4}},
	}})
	events := broker.Subscribe(row.ID)
	defer broker.Unsubscribe(row.ID, events)
	result, err := env.svc.PauseJob(ctx, env.user.ID, row.ID)
	if err != nil || !result.NeedsDrain || result.Job.Status != JobStatusPausing {
		t.Fatalf("pause=%+v %v", result, err)
	}
	for _, expected := range []string{"job_pausing", "stage_counts"} {
		select {
		case evt := <-events:
			if evt.Type != expected {
				t.Fatalf("event=%s, want %s", evt.Type, expected)
			}
			if evt.Type == "stage_counts" {
				counts, ok := evt.Metadata["stages"].(*JobStageCounts)
				if !ok || counts.ConfirmedWork != 4 {
					t.Fatalf("pause changed confirmed progress: %+v", evt)
				}
			}
		default:
			t.Fatalf("missing event %s", expected)
		}
	}
	if err := env.svc.MarkJobPaused(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"job_paused", "stage_counts"} {
		select {
		case evt := <-events:
			if evt.Type != expected {
				t.Fatalf("event=%s, want %s", evt.Type, expected)
			}
		default:
			t.Fatalf("missing event %s", expected)
		}
	}
	other := createTestUser(t, env.client, "stages-other")
	if _, err := env.svc.GetJobStageCounts(ctx, other.ID, row.ID); err == nil {
		t.Fatal("stage counts leaked across project access boundary")
	}
}
