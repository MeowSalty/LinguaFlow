package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

func TestExecutionDocumentSlotReplacementRetiresPriorBacking(t *testing.T) {
	root, err := resourcebudget.New(resourcebudget.Policy{Actor: resourcebudget.Limits{MaxPayloadBytes: 1000}, Instance: resourcebudget.Limits{MaxPayloadBytes: 1000}})
	if err != nil {
		t.Fatal(err)
	}
	scope := resourcebudget.Scope{ActorID: 1, CallID: "slots"}
	ctx := WithExecutionInputBudget(context.Background(), root, scope, DefaultExecutionInputBytes)
	lease, err := NewExecutionInputLease(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if err = lease.InitializeBorrowedDocumentSlots(ctx, 1); err != nil {
		t.Fatal(err)
	}
	baseline := root.Snapshot(1).Actor.PayloadBytes
	admission, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Close()
	if err = admission.AttachRootBudget(root, scope); err != nil {
		t.Fatal(err)
	}
	var target string
	var retained int64
	for i := 0; i < 20; i++ {
		body := strings.Repeat("x", 16+i%2*16)
		credit, e := backend.AllocationBytes(int64(len(body)), 1)
		if e != nil {
			t.Fatal(e)
		}
		if err = admission.ClaimBuild("candidate", 200); err != nil {
			t.Fatal(err)
		}
		before := root.Snapshot(1).Actor.PayloadBytes
		if err = lease.ReplaceOwnedSlot(0, ExecutionDocumentAllocation{TargetBytes: credit}, 0, admission, "candidate", func() {
			if got := root.Snapshot(1).Actor.PayloadBytes; got != before {
				t.Errorf("handoff changed aggregate credit: %d != %d", got, before)
			}
			target = body
		}); err != nil {
			t.Fatal(err)
		}
		if err = admission.ReleaseBuild("candidate"); err != nil {
			t.Fatal(err)
		}
		retained = credit
		if got := root.Snapshot(1).Actor.PayloadBytes; got != baseline+retained {
			t.Fatalf("completed slots accumulated: %d != %d", got, baseline+retained)
		}
	}
	if err = admission.ClaimBuild("noop", 20); err != nil {
		t.Fatal(err)
	}
	if err = lease.ReplaceOwnedSlot(0, ExecutionDocumentAllocation{}, DocumentBorrowTarget, admission, "noop", func() {}); err != nil {
		t.Fatal(err)
	}
	if err = admission.ReleaseBuild("noop"); err != nil {
		t.Fatal(err)
	}
	if target == "" || root.Snapshot(1).Actor.PayloadBytes != baseline+retained {
		t.Fatal("borrowed target was charged twice or refunded")
	}
	target = ""
	lease.Close()
	if got := root.Snapshot(1).Actor.PayloadBytes; got != 0 {
		t.Fatalf("document leaked %d", got)
	}
}

func TestExecutionDocumentSlotRejectedTransferDoesNotInstall(t *testing.T) {
	root, err := resourcebudget.New(resourcebudget.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithExecutionInputBudget(context.Background(), root, resourcebudget.Scope{ActorID: 1, CallID: "receiver"}, DefaultExecutionInputBytes)
	lease, err := NewExecutionInputLease(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if err = lease.InitializeBorrowedDocumentSlots(ctx, 1); err != nil {
		t.Fatal(err)
	}
	admission, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Close()
	if err = admission.AttachRootBudget(root, resourcebudget.Scope{ActorID: 2, CallID: "builder"}); err != nil {
		t.Fatal(err)
	}
	if err = admission.ClaimBuild("candidate", 20); err != nil {
		t.Fatal(err)
	}
	defer admission.ReleaseBuild("candidate")
	installed := false
	err = lease.ReplaceOwnedSlot(0, ExecutionDocumentAllocation{TargetBytes: 16}, 0, admission, "candidate", func() { installed = true })
	if !errors.Is(err, resourcebudget.ErrIdentity) || installed || admission.BuildBytes("candidate") != 20 {
		t.Fatal(err, installed)
	}
}
