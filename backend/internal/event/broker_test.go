package event

import (
	"sync"
	"testing"
	"time"
)

func TestBrokerSubscribePublish(t *testing.T) {
	b := NewBroker()

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
	b := NewBroker()

	ch := b.Subscribe(1)
	b.Unsubscribe(1, ch)

	// Channel should be closed
	_, ok := <-ch
	if ok {
		t.Error("expected channel to be closed after unsubscribe")
	}
}

func TestBrokerMultipleSubscribers(t *testing.T) {
	b := NewBroker()

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
	b := NewBroker()

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
	b := NewBroker()

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
	b := NewBroker()

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
