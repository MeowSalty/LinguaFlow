package backend

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"
)

// The barrier observes the actual registered waiter; timing is only a failure bound.
func awaitLimiterWaiter(t *testing.T, p *LimiterPool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for p.Snapshot().Waiters != 1 {
		if time.Now().After(deadline) {
			t.Fatal("waiter not registered")
		}
		runtime.Gosched()
	}
}
func awaitLimiterResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(time.Second):
		t.Fatal("waiter did not exit")
		return nil
	}
}

func TestLimiterStableHandleRefresh(t *testing.T) {
	p := NewLimiterPool()
	p.Initialize(map[int]int{1: 0})
	defer p.Shutdown()
	h, err := p.Lookup(1)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.Snapshot().WaitDurationSecondsCount != 0 {
		t.Fatal("unlimited call counted as waiting")
	}
	p.Refresh(1, 1)
	current, _ := p.Lookup(1)
	if current != h {
		t.Fatal("refresh replaced stable handle")
	}
	if err = h.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- h.Wait(context.Background()) }()
	awaitLimiterWaiter(t, p)
	p.Refresh(1, 0)
	if err = awaitLimiterResult(t, done); err != nil {
		t.Fatal(err)
	}
	s := p.Snapshot()
	if s.ActiveLimiters != 0 || s.Waiters != 0 || s.WaitDurationSecondsCount != 1 || s.WaitCancelledTotal != 0 {
		t.Fatalf("metrics: %+v", s)
	}
	p.Refresh(1, 2)
	for range 2 {
		if err = h.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	go func() { done <- h.Wait(context.Background()) }()
	awaitLimiterWaiter(t, p)
	p.Refresh(1, 1)
	if err = awaitLimiterResult(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestLimiterCancellationRemoveAndShutdown(t *testing.T) {
	for _, action := range []string{"cancel", "remove", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			p := NewLimiterPool()
			p.Initialize(map[int]int{7: 1})
			defer p.Shutdown()
			h, _ := p.Lookup(7)
			_ = h.Wait(context.Background())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- h.Wait(ctx) }()
			awaitLimiterWaiter(t, p)
			switch action {
			case "cancel":
				cancel()
			case "remove":
				p.Remove(7)
			case "shutdown":
				p.Shutdown()
			}
			err := awaitLimiterResult(t, done)
			if err == nil {
				t.Fatal("expected cancelled/closed waiter")
			}
			s := p.Snapshot()
			if s.Waiters != 0 || s.WaitDurationSecondsCount != 1 || s.WaitCancelledTotal != 1 {
				t.Fatalf("metrics: %+v", s)
			}
			if action != "cancel" {
				if err = h.Wait(context.Background()); !errors.Is(err, ErrLimiterClosed) {
					t.Fatalf("old handle still works: %v", err)
				}
				if err = p.Get(7, 500).Wait(context.Background()); err == nil {
					t.Fatal("stale configuration recreated policy")
				}
			}
		})
	}
}

func TestLimiterCurrentPolicyIgnoresOldSnapshot(t *testing.T) {
	p := NewLimiterPool()
	p.Initialize(map[int]int{1: 1})
	defer p.Shutdown()
	h := p.Get(1, 999)
	_ = h.Wait(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.Wait(ctx) }()
	awaitLimiterWaiter(t, p)
	cancel()
	if err := awaitLimiterResult(t, done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := p.Lookup(999); !errors.Is(err, ErrLimiterMissing) {
		t.Fatalf("missing policy: %v", err)
	}
}

func TestStandaloneLimiterCloseWakesWait(t *testing.T) {
	l := NewRateLimiterPerMinute(1)
	_ = l.Wait(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Wait(context.Background()) }()
	l.Close()
	if err := awaitLimiterResult(t, done); !errors.Is(err, ErrLimiterClosed) {
		t.Fatal(err)
	}
}
