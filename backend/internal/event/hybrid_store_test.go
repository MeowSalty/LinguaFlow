package event

import (
	"context"
	"slices"
	"sync"
	"testing"
	"testing/synctest"

	"entgo.io/ent/dialect"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

func TestHybridReplayJoinsPersistedHistoryAndDegradedWindow(t *testing.T) {
	ctx := t.Context()
	db, client := eventDatabase(t)
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER reject_degraded_event BEFORE INSERT ON sse_events
		WHEN NEW.message = 'degraded' BEGIN SELECT RAISE(ABORT, 'test append failure'); END`); err != nil {
		t.Fatal(err)
	}
	entStore := NewEntEventStore(client, dialect.SQLite)
	newStore := func() *HybridStore {
		s, err := NewHybridStore(NewRingBufferStore(RingBufferConfig{Capacity: 2}), entStore)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	appendEvent := func(s *HybridStore, jobID int, message string) int64 {
		seq, err := s.Append(jobID, Event{JobID: jobID, Type: "batch", Level: "info", Message: message})
		if (err != nil) != (message == "degraded") {
			t.Fatalf("append %q: %v", message, err)
		}
		return seq
	}
	beforeRestart := newStore()
	cursor := appendEvent(beforeRestart, 1, "cursor")
	older := appendEvent(beforeRestart, 1, "older")

	store := newStore()
	evicted := appendEvent(store, 1, "evicted from ring")
	appendEvent(store, 2, "another job")
	degraded := appendEvent(store, 1, "degraded")
	if seq, ok := latestSeqForTest(t, store, ctx, 1); !ok || seq != evicted {
		t.Fatalf("latest with a DB row = %d, %t; want persisted seq %d", seq, ok, evicted)
	}
	degradedOnly := appendEvent(store, 3, "degraded")
	if seq, ok := latestSeqForTest(t, store, ctx, 3); !ok || seq != degradedOnly {
		t.Fatalf("latest without a DB row = %d, %t; want memory seq %d", seq, ok, degradedOnly)
	}
	latest := appendEvent(store, 1, "latest")

	assertPage := func(after int64, limit int, want ...int64) {
		t.Helper()
		events := replayForTest(t, store, ctx, 1, after, limit)
		seqs := make([]int64, len(events))
		for i, evt := range events {
			seqs[i] = evt.Seq
		}
		if !slices.Equal(seqs, want) {
			t.Fatalf("replay after %d (limit %d) = %v, want %v", after, limit, seqs, want)
		}
	}
	assertPage(cursor, 2, older, evicted)
	assertPage(evicted, 2, degraded, latest)
	assertPage(degraded, 2, latest)
	assertPage(cursor, 0, older, evicted, degraded, latest)
}

func TestBrokerPublishesEachJobInSequenceOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, client := eventDatabase(t)
		entered, release := make(chan struct{}), make(chan struct{})
		resume := sync.OnceFunc(func() { close(release) })
		defer resume()
		client.SSEEvent.Use(func(next ent.Mutator) ent.Mutator {
			return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
				message, _ := mutation.(*ent.SSEEventMutation).Message()
				if message == "slow" {
					// The HybridStore has assigned the sequence but has not written it.
					close(entered)
					<-release
				}
				return next.Mutate(ctx, mutation)
			})
		})
		store, err := NewHybridStore(NewRingBufferStore(DefaultRingBufferConfig()), NewEntEventStore(client, dialect.SQLite))
		if err != nil {
			t.Fatal(err)
		}
		broker := NewBroker(store)
		firstJob, secondJob := broker.Subscribe(1), broker.Subscribe(2)
		publish := func(jobID int, message string) {
			broker.Publish(jobID, Event{JobID: jobID, Type: "batch", Level: "info", Message: message})
		}
		publish(1, "cursor")
		cursor := (<-firstJob).Seq
		go publish(1, "slow")
		<-entered
		go publish(1, "fast")
		go publish(2, "independent")
		synctest.Wait()

		if got := replayForTest(t, broker, t.Context(), 1, cursor, 1); len(got) != 0 {
			t.Fatalf("cursor advanced while its next event was unpublished: %+v", got)
		}
		select {
		case evt := <-firstJob:
			t.Fatalf("later event broadcast before its predecessor: %+v", evt)
		default:
		}
		select {
		case evt := <-secondJob:
			if evt.Message != "independent" {
				t.Fatalf("independent job received %+v", evt)
			}
		default:
			t.Fatal("publication for a different job waited on the blocked write")
		}

		resume()
		synctest.Wait()
		for _, want := range []string{"slow", "fast"} {
			var live Event
			select {
			case live = <-firstJob:
			default:
				t.Fatalf("expected live event %q", want)
			}
			page := replayForTest(t, broker, t.Context(), 1, cursor, 1)
			if live.Message != want || live.Seq <= cursor || len(page) != 1 || page[0].Message != want || page[0].Seq != live.Seq {
				t.Fatalf("after seq %d: live=%+v page=%+v, want %q", cursor, live, page, want)
			}
			cursor = live.Seq
		}
		if len(broker.publications) != 0 {
			t.Fatal("idle job publication locks were retained")
		}
	})
}
