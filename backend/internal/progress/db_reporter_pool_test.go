package progress

import (
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
)

func TestDBReporter_OnPoolEvent_PoolStart(t *testing.T) {
	broker := event.NewBroker(event.NewRingBufferStore(event.DefaultRingBufferConfig()))
	ch := broker.Subscribe(10)
	defer broker.Unsubscribe(10, ch)

	r := &DBReporter{broker: broker, jobID: 10}
	r.OnPoolEvent(PoolEvent{
		Mode:       "translate",
		PoolIndex:  0,
		MaxPools:   3,
		Batches:    5,
		Pending:    42,
		ShrinkRate: 0.5,
		Phase:      "pool_start",
	})

	var evt event.Event
	select {
	case evt = <-ch:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for pool event")
	}
	if evt.Type != "pool" {
		t.Fatalf("type=%q want pool", evt.Type)
	}
	if evt.Level != "info" {
		t.Fatalf("level=%q want info", evt.Level)
	}
	if evt.Stage != "translate" {
		t.Fatalf("stage=%q want translate", evt.Stage)
	}
	md := evt.Metadata
	if md["pool_index"] != 0 {
		t.Fatalf("pool_index=%v want 0", md["pool_index"])
	}
	if md["max_pools"] != 3 {
		t.Fatalf("max_pools=%v want 3", md["max_pools"])
	}
	if md["batches"] != 5 {
		t.Fatalf("batches=%v want 5", md["batches"])
	}
	if md["pending"] != 42 {
		t.Fatalf("pending=%v want 42", md["pending"])
	}
	if md["shrink_rate"] != 0.5 {
		t.Fatalf("shrink_rate=%v want 0.5", md["shrink_rate"])
	}
	if md["phase"] != "pool_start" {
		t.Fatalf("phase=%v want pool_start", md["phase"])
	}
	if evt.Message == "" {
		t.Fatal("expected non-empty message")
	}
}

func TestDBReporter_OnPoolEvent_PoolAdvance(t *testing.T) {
	broker := event.NewBroker(event.NewRingBufferStore(event.DefaultRingBufferConfig()))
	ch := broker.Subscribe(11)
	defer broker.Unsubscribe(11, ch)

	r := &DBReporter{broker: broker, jobID: 11}
	r.OnPoolEvent(PoolEvent{
		Mode:       "translate",
		PoolIndex:  0,
		MaxPools:   3,
		Batches:    5,
		Pending:    20,
		ShrinkRate: 0.5,
		Phase:      "pool_advance",
	})

	var evt event.Event
	select {
	case evt = <-ch:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for pool event")
	}
	if evt.Level != "warn" {
		t.Fatalf("level=%q want warn for pool_advance", evt.Level)
	}
	if md := evt.Metadata["phase"]; md != "pool_advance" {
		t.Fatalf("phase=%v want pool_advance", md)
	}
}

func TestDBReporter_OnPoolEvent_NilBrokerNoOp(t *testing.T) {
	r := &DBReporter{broker: nil, jobID: 12}
	// 不应 panic
	r.OnPoolEvent(PoolEvent{Mode: "translate", Phase: "pool_start"})
}
