package event

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

func eventDatabase(t *testing.T) (*sql.DB, *ent.Client) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(t.Context(), `CREATE TABLE sse_events (
		id INTEGER PRIMARY KEY, job_id INTEGER NOT NULL, seq INTEGER NOT NULL,
		type TEXT NOT NULL, level TEXT NOT NULL, stage TEXT,
		message TEXT NOT NULL, metadata JSON, created_at DATETIME NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	return db, ent.NewClient(ent.Driver(database.NewDriver(entsql.OpenDB(dialect.SQLite, db))))
}

func TestEntAppendPoolWaitHasDeadlineAndDegradesSynchronously(t *testing.T) {
	db, client := eventDatabase(t)
	store := NewEntEventStore(client, dialect.SQLite)
	store.appendTimeout = 30 * time.Millisecond
	hybrid, err := NewHybridStore(NewRingBufferStore(DefaultRingBufferConfig()), store)
	if err != nil {
		t.Fatal(err)
	}
	broker := NewBroker(hybrid)
	ch := broker.Subscribe(1)
	defer broker.Unsubscribe(1, ch)
	held, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	started := time.Now()
	seq, err := store.Append(1, Event{Seq: 1, Type: "batch", Level: "info", CreatedAt: started})
	if !errors.Is(err, context.DeadlineExceeded) || seq != 1 {
		t.Fatalf("append: seq=%d error=%v", seq, err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("pool wait ignored the append deadline")
	}
	broker.Publish(1, Event{Type: "batch", Level: "info", Message: "bounded failure", CreatedAt: started})
	select {
	case evt := <-ch:
		if evt.Seq != 1 || evt.Message != "bounded failure" {
			t.Fatalf("degraded event: %+v", evt)
		}
	default:
		t.Fatal("synchronous append returned without degraded broadcast")
	}
	var persisted int
	if err := held.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM sse_events").Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if persisted != 0 {
		t.Fatal("expired append continued writing in the background")
	}
	replay, err := hybrid.ringStore.Replay(t.Context(), 1, 0, 10)
	if err != nil || len(replay) != 1 {
		t.Fatalf("degraded replay: %v %v", replay, err)
	}
}

func TestHybridStartupUsesOneMaxQueryForEmptyAndExistingHistory(t *testing.T) {
	db, _ := eventDatabase(t)
	for _, maxSeq := range []int64{0, 9223372036854775000} {
		if _, err := db.ExecContext(t.Context(), "DELETE FROM sse_events"); err != nil {
			t.Fatal(err)
		}
		if maxSeq != 0 {
			if _, err := db.ExecContext(t.Context(), "INSERT INTO sse_events (job_id,seq,type,level,message,created_at) VALUES (1,?,'batch','info','history',?)", maxSeq, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
		queries, counts := 0, 0
		driver := dialect.Debug(database.NewDriver(entsql.OpenDB(dialect.SQLite, db)), func(args ...any) {
			line := strings.ToUpper(fmt.Sprint(args...))
			if strings.Contains(line, "SELECT") {
				queries++
			}
			if strings.Contains(line, "COUNT(") {
				counts++
			}
		})
		store := NewEntEventStore(ent.NewClient(ent.Driver(driver)), dialect.SQLite)
		hybrid, err := NewHybridStore(NewRingBufferStore(DefaultRingBufferConfig()), store)
		if err != nil {
			t.Fatal(err)
		}
		if hybrid.nextSeq.Load() != maxSeq || queries != 1 || counts != 0 {
			t.Fatalf("startup: max=%d queries=%d counts=%d, want %d/1/0", hybrid.nextSeq.Load(), queries, counts, maxSeq)
		}
	}
}
