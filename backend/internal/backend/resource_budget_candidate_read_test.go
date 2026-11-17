package backend_test

import (
	"context"
	"errors"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

func TestPaidAuxCandidateReadUsesPrepaidPayload(t *testing.T) {
	a := contractAdmission(t, 256, 4, 64<<20)
	root := contractRoot(t, 64<<20)
	scope := resourcebudget.Scope{ActorID: 1, JobID: 1}
	const arena = int64(8 << 20)
	if err := a.AttachRootBudget(root, scope); err != nil {
		t.Fatal(err)
	}
	if err := a.RestoreBudgetEntry("candidate", backend.PipelineBudgetEntry{Count: 1, Bytes: 512, Durable: true}); err != nil {
		t.Fatal(err)
	}
	intent := backend.RequestAdmissionIntent{RequestID: "aux", CandidateIDs: []string{"candidate"}, InputBytes: 4096, Stage: backend.RequestStageAlignment}
	permit, wait, err := a.TryAdmit(context.Background(), intent, nil)
	if err != nil || permit == nil {
		t.Fatalf("joint aux grant: %+v %v", wait, err)
	}
	defer permit.Release()
	before, _ := root.Usage(scope, "job:1:runtime")
	if before.PayloadBytes != (1<<20)+(1<<20)+4096+backend.RequestMetadataBytes {
		t.Fatalf("candidate Q and aux I/R/O not distinct: %+v", before)
	}
	blocker := resourcebudget.Scope{ActorID: 1, CallID: "full"}
	if err := root.Claim(blocker, "full", resourcebudget.Vector{PayloadBytes: (1 << 30) - before.PayloadBytes}); err != nil {
		t.Fatal(err)
	}
	defer root.Transfer(blocker, "full", resourcebudget.Vector{})
	permit.RequestDone() // Closing the network must retain the read/response owner.
	release, owned, err := a.BorrowCandidateRead("candidate", arena)
	if !owned || err != nil {
		t.Fatalf("paid read under full root: owned=%v err=%v", owned, err)
	}
	if _, _, err := a.BorrowCandidateRead("candidate", 1); !errors.Is(err, backend.ErrBuildCapacity) {
		t.Fatal("one aux owner lent two simultaneous read arenas")
	}
	if err := a.ChangeStation(0, 1, 0); err != nil {
		t.Fatalf("short build needs no new P: %v", err)
	}
	if err := a.ResizeBudgetEntry("candidate", 4096, false); err != nil {
		t.Fatalf("candidate growth within already-granted Q: %v", err)
	}
	if err := a.ReserveFinalResultCredit("candidate", 8192); err != nil {
		t.Fatalf("final commit within durable Q: %v", err)
	}
	if err := a.ChangeStation(0, -1, 0); err != nil {
		t.Fatal(err)
	}
	release()
	release()
	still, _ := root.Usage(scope, "job:1:runtime")
	if still.PayloadBytes != before.PayloadBytes {
		t.Fatal("read release returned a still-owned response promise")
	}
	permit.Release()
	finished, _ := root.Usage(scope, "job:1:runtime")
	if finished.PayloadBytes != 1<<20 || finished.AuxResponseOwners != 0 {
		t.Fatalf("owner finish retained transient arena/I/R/O: %+v", finished)
	}
	if _, owned, err := a.BorrowCandidateRead("candidate", 1); owned || err != nil {
		t.Fatal("ended aux owner still lent its credit")
	}
}

func TestPaidCandidateReadAboveRUsesActualRootExcess(t *testing.T) {
	a := contractAdmission(t, 256, 4, 64<<20)
	root := contractRoot(t, 64<<20)
	scope := resourcebudget.Scope{ActorID: 1, JobID: 1}
	if err := a.AttachRootBudget(root, scope); err != nil {
		t.Fatal(err)
	}
	if err := a.RestoreBudgetEntry("candidate", backend.PipelineBudgetEntry{Count: 1, Bytes: 512, Durable: true}); err != nil {
		t.Fatal(err)
	}
	permit, _, err := a.TryAdmit(context.Background(), backend.RequestAdmissionIntent{RequestID: "aux", CandidateIDs: []string{"candidate"}, Stage: backend.RequestStageAlignment}, nil)
	if err != nil || permit == nil {
		t.Fatal(err)
	}
	defer permit.Release()
	ctx := backend.WithBuildOwnership(context.Background(), a, "response:aux")
	ctx = backend.WithBuildReservation(ctx, func(n int64) error { return a.ClaimBuild("response:aux", n) }, func(n int64) {
		if e := a.ReleaseBuildBytes("response:aux", n); e != nil {
			t.Error(e)
		}
	})
	ctx = backend.WithProviderResponseOwnership(ctx, a, "aux", backend.RequestStageAlignment)
	ctx, err = backend.WithProviderResponseAllocation(ctx, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	before := root.Snapshot(1).Actor.PayloadBytes
	release, owned, err := a.BorrowCandidateRead("candidate", 2<<20)
	if err != nil || !owned {
		t.Fatal(owned, err)
	}
	owner, err := backend.OwnProviderResponseAllocation(ctx, 2<<20, 1)
	if err != nil || root.Snapshot(1).Actor.PayloadBytes-before != 1<<20 {
		t.Fatal(err, root.Snapshot(1))
	}
	owner.Release()
	release()
	if root.Snapshot(1).Actor.PayloadBytes != before {
		t.Fatal(root.Snapshot(1))
	}
	if err = backend.CloseProviderResponseAllocation(ctx); err != nil {
		t.Fatal(err)
	}
}
