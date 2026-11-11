package tasklife

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRegistrationFreezeAllowsExistingClaimCompletion(t *testing.T) {
	ctx := context.Background()
	c := &Coordinator{}
	g, err := c.LockRegistration(ctx, "translation", 1)
	if err != nil {
		t.Fatal(err)
	}
	claim := g.Claim()
	g.Release()
	unfreeze, err := c.FreezeRegistration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer unfreeze()
	control, err := c.Lock(ctx, "translation", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !control.Active() {
		t.Fatal("accepted claim vanished during freeze")
	}
	claim()
	control.Release()
	if c.Busy("translation", 1) {
		t.Fatal("writer join could not retire claim")
	}
	deadline, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	if guard, err := c.LockRegistration(deadline, "translation", 2); !errors.Is(err, context.DeadlineExceeded) {
		if guard != nil {
			guard.Release()
		}
		t.Fatalf("register=%v", err)
	}
	unfreeze()
	unfreeze()
	guard, err := c.LockRegistration(ctx, "translation", 2)
	if err != nil {
		t.Fatal(err)
	}
	guard.Release()
}

func TestRegistrationFreezeCancellationReopensAdmission(t *testing.T) {
	ctx := context.Background()
	c := &Coordinator{}
	g, err := c.LockRegistration(ctx, "translation", 1)
	if err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	if _, err := c.FreezeRegistration(deadline); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("freeze=%v", err)
	}
	g.Release()
	next, err := c.LockRegistration(ctx, "translation", 2)
	if err != nil {
		t.Fatal(err)
	}
	next.Release()
}
