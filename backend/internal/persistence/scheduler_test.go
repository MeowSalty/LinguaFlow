package persistence

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestSchedulerQueueDoesNotSpendExecutionBudget(t *testing.T) {
	p := DefaultPolicy()
	p.ExecutionTimeout = 20 * time.Millisecond
	s := New(p, nil)
	defer s.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.Save(context.Background(), Metadata{}, func(context.Context) error { close(entered); <-release; return nil })
	}()
	<-entered
	second := make(chan error, 1)
	go func() {
		second <- s.Save(context.Background(), Metadata{}, func(ctx context.Context) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return nil
		})
	}()
	time.Sleep(40 * time.Millisecond)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatalf("queue charged to SQL: %v", err)
	}
}
func TestSchedulerRetriesReleaseWriterAndStopAtBudget(t *testing.T) {
	p := DefaultPolicy()
	p.RetryDelay = 50 * time.Millisecond
	p.RetryBudget = 80 * time.Millisecond
	s := New(p, nil)
	defer s.Close()
	failed := make(chan struct{})
	done := make(chan error, 1)
	var once sync.Once
	failure := errors.New("busy")
	go func() {
		done <- s.Save(context.Background(), Metadata{Class: Reliable}, func(context.Context) error { once.Do(func() { close(failed) }); return failure })
	}()
	<-failed
	if err := s.Save(context.Background(), Metadata{}, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("retry budget accepted failure")
	}
	if n := s.Snapshot().Saves; n > 5 {
		t.Fatalf("unbounded attempts: %d", n)
	}
}
func TestSchedulerReliableBurstStillServicesNormal(t *testing.T) {
	p := DefaultPolicy()
	s := New(p, nil)
	defer s.Close()
	releaseSlot, err := s.acquire(context.Background(), Normal)
	if err != nil {
		t.Fatal(err)
	}
	var order []Class
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.Save(context.Background(), Metadata{Class: Reliable}, func(context.Context) error { mu.Lock(); order = append(order, Reliable); mu.Unlock(); return nil })
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = s.Save(context.Background(), Metadata{Class: Normal}, func(context.Context) error { mu.Lock(); order = append(order, Normal); mu.Unlock(); return nil })
	}()
	deadline := time.Now().Add(time.Second)
	for {
		snp := s.Snapshot()
		if snp.NormalQueued == 1 && snp.ReliableQueued == 6 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("writers did not queue")
		}
		time.Sleep(time.Millisecond)
	}
	releaseSlot()
	wg.Wait()
	at := -1
	for i, c := range order {
		if c == Normal {
			at = i
		}
	}
	if at > 4 || at < 0 {
		t.Fatalf("normal starved: %v", order)
	}
}

func TestSchedulerSQLAggregatesExcludeRequestIdentity(t *testing.T) {
	s := New(DefaultPolicy(), nil)
	defer s.Close()
	observer := &Collector{}
	for _, requestID := range []string{"request-one", "request-two"} {
		ctx := WithObserver(context.Background(), observer)
		err := s.Save(ctx, Metadata{Operation: "reserve", Class: Normal, RequestID: requestID}, func(ctx context.Context) error {
			ObserveTransaction(WithDialect(ctx, "postgres"), "query", "", 3*time.Millisecond, nil)
			ObserveTransaction(WithDialect(ctx, "postgres"), "exec", "", 5*time.Millisecond, nil)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	rows := s.Snapshot().Aggregates
	if len(rows) != 1 || rows[0].Operation != "reserve" || rows[0].Dialect != "postgres" || rows[0].Outcome != "success" || rows[0].Attempts != 2 || rows[0].SQLCalls != 4 || rows[0].SQLDuration != 16*time.Millisecond {
		t.Fatalf("incorrect per-operation SQL attribution: %+v", rows)
	}
	if observer.Snapshot().Calls != 4 {
		t.Fatal("existing observer lost SQL samples")
	}
}

func TestDeadlineCannotExtendPriorHandoffBudget(t *testing.T) {
	first := time.Now().Add(time.Second)
	ctx := WithDeadline(context.Background(), first)
	ctx = WithDeadline(ctx, first.Add(time.Hour))
	if got := Deadline(ctx); !got.Equal(first) {
		t.Fatalf("deadline extended: %v", got)
	}
}
