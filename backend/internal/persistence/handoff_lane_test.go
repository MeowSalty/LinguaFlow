package persistence

import (
	"context"
	"testing"
	"time"
)

func TestHandoffLaneDrainsRawWhileCandidateSaveIsBlocked(t *testing.T) {
	s := New(DefaultPolicy(), nil)
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	ordinary := make(chan error, 1)
	go func() {
		ordinary <- s.Save(ctx, Metadata{Class: Reliable, Operation: "member_promotion"}, func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	defer func() { close(release); <-ordinary }()
	for i := 0; i < 32; i++ {
		if err := s.Save(ctx, Metadata{Class: Handoff, Operation: "response_handoff"}, func(context.Context) error { return nil }); err != nil {
			t.Fatalf("raw %d waited behind candidate persistence: %v", i, err)
		}
	}
	if snap := s.Snapshot(); !snap.Executing || snap.HandoffExecuting || snap.Saves != 32 {
		t.Fatalf("raw drainage changed candidate ownership: %+v", snap)
	}
}

func TestHandoffLaneIsSerialAndSharesReliableQueueBound(t *testing.T) {
	p := DefaultPolicy()
	p.ReliableQueue = 1
	s := New(p, nil)
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	handoff, err := s.acquire(ctx, Handoff)
	if err != nil {
		t.Fatal(err)
	}
	defer handoff()
	normal, err := s.acquire(ctx, Reliable)
	if err != nil {
		t.Fatal(err)
	}
	defer normal()
	queued := make(chan error, 1)
	go func() {
		_, err := s.acquire(ctx, Handoff)
		queued <- err
	}()
	for s.Snapshot().HandoffQueued != 1 {
		if ctx.Err() != nil {
			t.Fatal("handoff did not queue")
		}
		time.Sleep(time.Millisecond)
	}
	if snap := s.Snapshot(); snap.ReliableQueued != 1 || snap.PeakReliableQueued > p.ReliableQueue {
		t.Fatalf("reserved lane expanded reliable metadata queue: %+v", snap)
	}
	cancel()
	if err := <-queued; err == nil {
		t.Fatal("second raw executed concurrently on the single handoff lane")
	}
}
