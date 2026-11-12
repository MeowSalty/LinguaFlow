package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
)

func TestStagedCancelledIdleDrainDoesNotWaitForEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("caller cancellation was not established")
	}
	events := make(chan stagedEvent)
	done := make(chan bool, 1)
	go func() { _, received := (stagedInFlight{}).drain(events); done <- received }()
	select {
	case received := <-done:
		if received {
			t.Fatal("idle cancellation invented a worker result")
		}
	case <-time.After(time.Second):
		// Also releases the deliberately broken guard in the reverse check;
		// the failed test must not leave its synthetic receiver behind.
		close(events)
		<-done
		t.Fatal("cancelled idle coordinator waited on an empty event channel")
	}
}

func TestStagedCancelledDrainRetainsInFlightPaidResults(t *testing.T) {
	for _, tc := range []struct {
		name   string
		flight stagedInFlight
	}{
		{"main_handoff", stagedInFlight{main: 1}},
		{"candidate_commit", stagedInFlight{candidates: 1}},
		{"raw_promotion", stagedInFlight{promotions: 1}},
		{"candidate_recovery_page", stagedInFlight{candidatePage: true}},
		{"response_recovery_page", stagedInFlight{responsePage: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			r := stagedTestRuntime(t, DefaultCandidateLimits())
			store := NewMemoryRoundStore(nil)
			c := &Candidate{ID: "paid", Index: 0}
			events := make(chan stagedEvent, 1)
			terminal := make(chan stagedEvent, 1)
			started, release, writerDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
			go func() {
				defer close(writerDone)
				close(started)
				<-release
				err := r.SaveNamed(r.CompletionContext(ctx), persistence.Metadata{Operation: "commit", Class: persistence.Reliable}, func(saveCtx context.Context) error {
					_, err := store.Commit(saveCtx, c, TranslatedSegment{TargetText: "confirmed"})
					return err
				})
				events <- stagedEvent{candidate: c, err: err}
			}()
			<-started
			go func() {
				event, received := tc.flight.drain(events)
				if !received {
					event.err = errors.New("paid worker was abandoned")
				}
				terminal <- event
			}()
			select {
			case <-terminal:
				close(release)
				<-writerDone
				t.Fatal("cancellation returned before the in-flight paid result")
			default:
			}
			close(release)
			select {
			case event := <-terminal:
				<-writerDone
				if event.err != nil || event.candidate != c {
					t.Fatalf("paid terminal result lost: %+v", event)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("paid worker did not drain after its commit barrier opened")
			}
			state, err := store.Load(t.Context())
			if err != nil || len(state.Completed) != 1 || state.Completed[0] != 0 || c.Segment.Target != "confirmed" {
				t.Fatalf("cancellation lost confirmed paid fact: %+v %v", state, err)
			}
		})
	}
}
