package tasklife

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestClaimSurvivesGuardAndCannotReleaseLaterClaim(t *testing.T) {
	var c Coordinator
	g, err := c.Lock(context.Background(), "translation", 1)
	if err != nil {
		t.Fatal(err)
	}
	old := g.Claim()
	g.Release()
	g.Release()
	if !c.Busy("translation", 1) || c.Busy("glossary_sync", 1) {
		t.Fatal("claim is not type scoped")
	}
	next, err := c.Lock(context.Background(), "translation", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !next.Active() {
		t.Fatal("control lost running claim")
	}
	fresh := next.Claim()
	old()
	old()
	if !next.Active() {
		t.Fatal("old release removed fresh claim")
	}
	next.Release()
	fresh()
	if c.Busy("translation", 1) {
		t.Fatal("claim leaked")
	}
	if len(c.entries) != 0 {
		t.Fatal("idle task entries leaked")
	}
}

func TestLockCancellationAndConcurrentRetirement(t *testing.T) {
	var c Coordinator
	g, err := c.Lock(context.Background(), "translation", 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Lock(ctx, "translation", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	g.Release()
	var wg sync.WaitGroup
	count := 0
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				guard, err := c.Lock(context.Background(), "translation", 1)
				if err != nil {
					t.Error(err)
					return
				}
				count++
				guard.Release()
			}
		}()
	}
	wg.Wait()
	if count != 800 || len(c.entries) != 0 {
		t.Fatalf("count=%d entries=%d", count, len(c.entries))
	}
}
