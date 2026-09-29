package event

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

func TestBrokerNormalizesBeforePersistenceAndBroadcast(t *testing.T) {
	local := time.Date(2026, 9, 29, 12, 30, 5, 123456789, time.FixedZone("user", 8*60*60))
	store := NewRingBufferStore(DefaultRingBufferConfig())
	b := NewBroker(store)
	ch := b.Subscribe(1)
	defer b.Unsubscribe(1, ch)
	b.Publish(1, Event{JobID: 1, CreatedAt: local, Metadata: map[string]any{"user_date": "2026-09-29T12:30:05+08:00"}})
	live := <-ch
	replayed := b.Replay(context.Background(), 1, 0, 10)
	history := rowsToEvents([]*ent.SSEEvent{{CreatedAt: local}})
	if len(replayed) != 1 || len(history) != 1 {
		t.Fatalf("unexpected replay/history sizes: %d/%d", len(replayed), len(history))
	}
	for _, evt := range []Event{live, replayed[0], history[0]} {
		if evt.CreatedAt.Location() != time.UTC || !evt.CreatedAt.Equal(local) {
			t.Fatalf("event instant = %v", evt.CreatedAt)
		}
		encoded, err := json.Marshal(evt)
		if err != nil || !strings.Contains(string(encoded), `"created_at":"2026-09-29T04:30:05.123456789Z"`) {
			t.Fatalf("event JSON = %s, %v", encoded, err)
		}
	}
	if live.Metadata["user_date"] != "2026-09-29T12:30:05+08:00" {
		t.Fatal("user metadata changed")
	}
}

type precisionTestStore struct {
	*RingBufferStore
	precision *HybridStore
	degraded  bool
	persisted Event
}

func (s *precisionTestStore) NormalizeTime(t time.Time) time.Time {
	return s.precision.NormalizeTime(t)
}

func (s *precisionTestStore) Append(jobID int, evt Event) (int64, error) {
	// Model the durable store's timestamp encoding independently of what the
	// broker passes in. Broadcasting the original nanos would diverge here.
	s.persisted = evt
	s.persisted.CreatedAt = s.NormalizeTime(evt.CreatedAt)
	seq, err := s.RingBufferStore.Append(jobID, s.persisted)
	if s.degraded {
		return seq, errors.New("durable store unavailable")
	}
	return seq, err
}

func TestBrokerUsesStorePrecisionForLiveAndReplay(t *testing.T) {
	local := time.Date(2026, 9, 29, 12, 30, 5, 123456789, time.FixedZone("user", -7*60*60))
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		for _, degraded := range []bool{false, true} {
			name := driver
			if degraded {
				name += "/degraded"
			}
			t.Run(name, func(t *testing.T) {
				store := &precisionTestStore{
					RingBufferStore: NewRingBufferStore(DefaultRingBufferConfig()),
					precision:       &HybridStore{entStore: NewEntEventStore(nil, driver)},
					degraded:        degraded,
				}
				b := NewBroker(store)
				ch := b.Subscribe(1)
				defer b.Unsubscribe(1, ch)
				b.Publish(1, Event{JobID: 1, CreatedAt: local})
				live := <-ch
				replay := b.Replay(context.Background(), 1, 0, 10)
				want := local.UTC()
				if driver == dialect.Postgres {
					want = want.Truncate(time.Microsecond)
				}
				if len(replay) != 1 {
					t.Fatalf("replay count = %d, want 1", len(replay))
				}
				for _, evt := range []Event{live, replay[0], store.persisted} {
					if evt.CreatedAt != want {
						t.Errorf("time = %v, want exactly %v", evt.CreatedAt, want)
					}
				}
			})
		}
	}
}
