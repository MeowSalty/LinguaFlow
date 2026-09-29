package worker

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// Done is evaluated after Acquire registers its lock reference, exposing the
// exact waiting boundary without timing guesses or sleeping.
type observedWaitContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *observedWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Done()
}

func TestResourceMutexCancelledWaiterReturnsBeforeHolder(t *testing.T) {
	rm := NewResourceMutex()
	release, err := rm.Acquire(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	base, cancel := context.WithCancel(context.Background())
	ctx := &observedWaitContext{Context: base, entered: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		rel, err := rm.Acquire(ctx, 7)
		if rel != nil {
			rel()
		}
		done <- err
	}()
	receiveWorker(t, ctx.entered)
	rm.mu.Lock()
	original := rm.locks[7]
	refs := original.refCount
	rm.mu.Unlock()
	if refs != 2 {
		t.Fatalf("references=%d, want holder+waiter", refs)
	}
	cancel()
	if err := receiveWorker(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled lock wait: %v", err)
	}
	rm.mu.Lock()
	sameLock := rm.locks[7] == original
	refs = original.refCount
	rm.mu.Unlock()
	if !sameLock || refs != 1 {
		t.Fatal("cancel removed a live holder's lock")
	}

	nextBase, nextCancel := context.WithCancel(context.Background())
	defer nextCancel()
	next := &observedWaitContext{Context: nextBase, entered: make(chan struct{})}
	acquired := make(chan func(), 1)
	go func() {
		rel, err := rm.Acquire(next, 7)
		if err != nil {
			acquired <- nil
			return
		}
		acquired <- rel
	}()
	receiveWorker(t, next.entered)
	rm.mu.Lock()
	sameLock = rm.locks[7] == original
	rm.mu.Unlock()
	if !sameLock {
		t.Fatal("new waiter created a parallel lock")
	}
	release()
	release()
	nextRelease := receiveWorker(t, acquired)
	if nextRelease == nil {
		t.Fatal("next waiter did not acquire")
	}
	nextRelease()
	nextRelease()
	rm.mu.Lock()
	remaining := len(rm.locks)
	rm.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("released resources retained %d locks", remaining)
	}
}

func TestResourceMutexCancelledAndIndependentResources(t *testing.T) {
	rm := NewResourceMutex()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if rel, err := rm.Acquire(ctx, 1); rel != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("already cancelled acquire: %v %v", rel != nil, err)
	}
	first, err := rm.Acquire(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	second, err := rm.Acquire(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	second()
}
