package event

import (
	"sync"
	"testing"
)

func TestRingBufferAppendAndReplay(t *testing.T) {
	store := NewRingBufferStore(RingBufferConfig{Capacity: 16})

	evt1 := Event{Type: "a", JobID: 1, Message: "first"}
	evt2 := Event{Type: "b", JobID: 1, Message: "second"}

	seq1 := store.Append(1, evt1)
	seq2 := store.Append(1, evt2)

	if seq1 != 1 {
		t.Fatalf("expected seq1=1, got %d", seq1)
	}
	if seq2 != 2 {
		t.Fatalf("expected seq2=2, got %d", seq2)
	}

	events := store.Replay(1, 0)
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Seq != 1 || events[0].Message != "first" {
		t.Errorf("unexpected events[0]: %+v", events[0])
	}
	if events[1].Seq != 2 || events[1].Message != "second" {
		t.Errorf("unexpected events[1]: %+v", events[1])
	}
}

func TestRingBufferOverflow(t *testing.T) {
	capacity := 4
	store := NewRingBufferStore(RingBufferConfig{Capacity: capacity})

	for i := 0; i < capacity+3; i++ {
		store.Append(1, Event{Type: "ev", JobID: 1, Message: "msg"})
	}

	events := store.Replay(1, 0)
	if len(events) != capacity {
		t.Fatalf("expected %d events after overflow, got %d", capacity, len(events))
	}

	// Should have the last 4 events (seq 4,5,6,7)
	if events[0].Seq != 4 {
		t.Errorf("expected first replayed seq=4, got %d", events[0].Seq)
	}
	if events[capacity-1].Seq != 7 {
		t.Errorf("expected last replayed seq=7, got %d", events[capacity-1].Seq)
	}
}

func TestRingBufferReplayEmpty(t *testing.T) {
	store := NewRingBufferStore(DefaultRingBufferConfig())

	events := store.Replay(999, 0)
	if events != nil {
		t.Fatalf("expected nil for empty buffer, got %v", events)
	}
}

func TestRingBufferPurge(t *testing.T) {
	store := NewRingBufferStore(DefaultRingBufferConfig())

	store.Append(1, Event{Type: "a", JobID: 1, Message: "msg"})
	store.Purge(1)

	events := store.Replay(1, 0)
	if events != nil {
		t.Fatalf("expected nil after purge, got %v", events)
	}
}

func TestRingBufferConcurrent(t *testing.T) {
	store := NewRingBufferStore(RingBufferConfig{Capacity: 256})

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				store.Append(id, Event{Type: "test", JobID: id, Message: "msg"})
			}
		}(i)
	}
	wg.Wait()

	for i := 0; i < 10; i++ {
		events := store.Replay(i, 0)
		if len(events) != 100 {
			t.Errorf("job %d: expected 100 events, got %d", i, len(events))
		}
	}
}

func TestRingBufferReplayGap(t *testing.T) {
	store := NewRingBufferStore(RingBufferConfig{Capacity: 16})

	store.Append(1, Event{Type: "a", JobID: 1, Message: "first"})
	store.Append(1, Event{Type: "b", JobID: 1, Message: "second"})
	store.Append(1, Event{Type: "c", JobID: 1, Message: "third"})

	// Replay after seq 1 — should get seq 2 and 3
	events := store.Replay(1, 1)
	if len(events) != 2 {
		t.Fatalf("expected 2 events after seq 1, got %d", len(events))
	}
	if events[0].Seq != 2 {
		t.Errorf("expected seq=2, got %d", events[0].Seq)
	}
	if events[1].Seq != 3 {
		t.Errorf("expected seq=3, got %d", events[1].Seq)
	}

	// Replay after seq 2 — should get seq 3
	events = store.Replay(1, 2)
	if len(events) != 1 {
		t.Fatalf("expected 1 event after seq 2, got %d", len(events))
	}
	if events[0].Seq != 3 {
		t.Errorf("expected seq=3, got %d", events[0].Seq)
	}

	// Replay after seq 3 — should get nothing
	events = store.Replay(1, 3)
	if len(events) != 0 {
		t.Fatalf("expected 0 events after seq 3, got %d", len(events))
	}
}
