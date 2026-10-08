package event

import (
	"database/sql"
	"errors"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

func TestHistoryReadFailureIsNotAnEmptyPage(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.SQLite, db)))
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store := NewEntEventStore(client, dialect.SQLite)
	broker := NewBroker(store).WithHistorian(store)
	if _, _, _, err := broker.ListHistory(t.Context(), 1, 0, 10); !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatalf("forward history error = %v", err)
	}
	if _, _, _, err := broker.ListHistoryBefore(t.Context(), 1, 10, 10); !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatalf("backward history error = %v", err)
	}
	if _, err := broker.Replay(t.Context(), 1, 0, 10); !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatalf("replay error = %v", err)
	}
	if _, _, err := broker.LatestSeq(t.Context(), 1); !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatalf("sequence error = %v", err)
	}
	if _, _, _, err := NewBroker(nil).ListHistory(t.Context(), 1, 0, 10); !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatalf("missing historian error = %v", err)
	}
}

func TestHybridReadFailureDoesNotFallBackToMisleadingMemory(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.SQLite, db)))
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ring := NewRingBufferStore(DefaultRingBufferConfig())
	ring.AppendWithSeq(1, Event{Seq: 99, JobID: 1})
	hybrid := &HybridStore{ringStore: ring, entStore: NewEntEventStore(client, dialect.SQLite)}
	if _, _, err := hybrid.LatestSeq(t.Context(), 1); !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatalf("sequence lookup used memory after database failure: %v", err)
	}
	hybrid.Purge(1)
	if _, err := hybrid.Replay(t.Context(), 1, 0, 10); !errors.Is(err, ErrHistoryUnavailable) {
		t.Fatalf("missing memory fell back to empty history: %v", err)
	}
}
