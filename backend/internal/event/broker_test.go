package event

import (
	"sync"
	"testing"
	"time"
)

func TestBrokerSubscribePublish(t *testing.T) {
	store := NewRingBufferStore(DefaultRingBufferConfig())
	b := NewBroker(store)

	ch := b.Subscribe(1)
	defer b.Unsubscribe(1, ch)

	evt := Event{
		Type:    "job_started",
		JobID:   1,
		Level:   "info",
		Message: "job started",
	}
	b.Publish(1, evt)

	select {
	case received := <-ch:
		if received.Type != "job_started" {
			t.Errorf("expected type job_started, got %s", received.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestBrokerUnsubscribe(t *testing.T) {
	store := NewRingBufferStore(DefaultRingBufferConfig())
	b := NewBroker(store)

	ch := b.Subscribe(1)
	b.Unsubscribe(1, ch)

	// Channel should be closed
	_, ok := <-ch
	if ok {
		t.Error("expected channel to be closed after unsubscribe")
	}
}

func TestBrokerMultipleSubscribers(t *testing.T) {
	store := NewRingBufferStore(DefaultRingBufferConfig())
	b := NewBroker(store)

	ch1 := b.Subscribe(1)
	ch2 := b.Subscribe(1)
	defer b.Unsubscribe(1, ch1)
	defer b.Unsubscribe(1, ch2)

	evt := Event{Type: "test", JobID: 1, Message: "hello"}
	b.Publish(1, evt)

	for i, ch := range []chan Event{ch1, ch2} {
		select {
		case received := <-ch:
			if received.Type != "test" {
				t.Errorf("subscriber %d: expected type test, got %s", i, received.Type)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d: timed out", i)
		}
	}
}

func TestBrokerIsolation(t *testing.T) {
	store := NewRingBufferStore(DefaultRingBufferConfig())
	b := NewBroker(store)

	ch1 := b.Subscribe(1)
	ch2 := b.Subscribe(2)
	defer b.Unsubscribe(1, ch1)
	defer b.Unsubscribe(2, ch2)

	b.Publish(1, Event{Type: "job1", JobID: 1, Message: "for job 1"})

	select {
	case received := <-ch1:
		if received.Type != "job1" {
			t.Errorf("expected job1, got %s", received.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out on ch1")
	}

	// ch2 should not receive the event
	select {
	case <-ch2:
		t.Error("ch2 should not have received event for job 1")
	case <-time.After(50 * time.Millisecond):
		// expected
	}
}

func TestBrokerNonBlockingPublish(t *testing.T) {
	store := NewRingBufferStore(DefaultRingBufferConfig())
	b := NewBroker(store)

	ch := b.Subscribe(1)
	defer b.Unsubscribe(1, ch)

	// Fill the buffer (capacity 64)
	for i := 0; i < 64; i++ {
		b.Publish(1, Event{Type: "fill", JobID: 1, Message: "fill"})
	}

	// This should not block
	done := make(chan struct{})
	go func() {
		b.Publish(1, Event{Type: "overflow", JobID: 1, Message: "overflow"})
		close(done)
	}()

	select {
	case <-done:
		// expected: non-blocking
	case <-time.After(time.Second):
		t.Fatal("Publish blocked on full channel")
	}
}

func TestBrokerConcurrentPublish(t *testing.T) {
	store := NewRingBufferStore(DefaultRingBufferConfig())
	b := NewBroker(store)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			ch := b.Subscribe(id)
			defer b.Unsubscribe(id, ch)
			for j := 0; j < 100; j++ {
				b.Publish(id, Event{Type: "test", JobID: id, Message: "msg"})
			}
		}(i)
	}
	wg.Wait()
}

func TestBrokerLatestSeq(t *testing.T) {
	store := NewRingBufferStore(DefaultRingBufferConfig())
	b := NewBroker(store)

	if _, ok := latestSeqForTest(t, b, t.Context(), 1); ok {
		t.Fatal("expected ok=false when store has no events")
	}
	b.Publish(1, Event{Type: "ev", JobID: 1, Message: "msg"})
	b.Publish(1, Event{Type: "ev", JobID: 1, Message: "msg"})
	seq, ok := latestSeqForTest(t, b, t.Context(), 1)
	if !ok || seq != 2 {
		t.Fatalf("expected latest seq=2, got seq=%d ok=%v", seq, ok)
	}

	empty := NewBroker(nil)
	if _, ok := latestSeqForTest(t, empty, t.Context(), 1); ok {
		t.Fatal("expected ok=false when no store is configured")
	}
}

func TestBrokerCloseJobRacesDisconnectAndIsIdempotent(t *testing.T) {
	store := NewRingBufferStore(DefaultRingBufferConfig())
	b := NewBroker(store)
	channels := make([]chan Event, 100)
	for i := range channels {
		channels[i] = b.Subscribe(1)
	}
	other := b.Subscribe(2)
	defer b.Unsubscribe(2, other)
	b.Publish(1, Event{JobID: 1, Type: "retained"})
	b.Publish(2, Event{JobID: 2, Type: "other"})
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, ch := range channels {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			b.Unsubscribe(1, ch)
			b.Unsubscribe(1, ch)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		b.CloseJob(1)
		b.CloseJob(1)
	}()
	close(start)
	wg.Wait()
	for _, ch := range channels {
		for range ch {
		}
	}
	if got := replayForTest(t, b, t.Context(), 1, 0, 100); len(got) != 0 {
		t.Fatalf("deleted job replay survived: %+v", got)
	}
	if got := replayForTest(t, b, t.Context(), 2, 0, 100); len(got) != 1 {
		t.Fatalf("other job history changed: %+v", got)
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	if len(b.subscribers[1]) != 0 || len(b.subscribers[2]) != 1 {
		t.Fatal("close affected the wrong subscriptions")
	}
}
