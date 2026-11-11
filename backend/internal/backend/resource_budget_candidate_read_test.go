package backend_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

func TestPaidAuxCandidateReadUsesPrepaidPayload(t *testing.T) {
	a := contractAdmission(t, 256, 4, 64<<20)
	root := contractRoot(t, 64<<20)
	scope := resourcebudget.Scope{ActorID: 1, JobID: 1}
	const arena = int64(8 << 20)
	if err := a.SetCandidateReadAllocationBound(func(int64) (int64, error) { return arena, nil }); err != nil {
		t.Fatal(err)
	}
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
	if before.PayloadBytes != arena+(1<<20)+(1<<20)+4096+backend.RequestMetadataBytes {
		t.Fatalf("arena, candidate Q, aux I/R/O not distinct: %+v", before)
	}
	blocker := resourcebudget.Scope{ActorID: 1, CallID: "full"}
	if err := root.Claim(blocker, "full", resourcebudget.Vector{PayloadBytes: (1 << 30) - before.PayloadBytes}); err != nil {
		t.Fatal(err)
	}
	defer root.Transfer(blocker, "full", resourcebudget.Vector{})
	permit.RequestDone() // Closing the network must retain the read/response owner.
	if _, owned, err := a.BorrowCandidateRead("candidate", arena+1); !owned || !errors.Is(err, backend.ErrBuildCapacity) {
		t.Fatalf("over-bound allocation: owned=%v err=%v", owned, err)
	}
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

func TestAuxCandidateReadPromiseRefusesBeforeDispatch(t *testing.T) {
	for _, maximum := range []int64{1 << 30, math.MaxInt64} {
		a := contractAdmission(t, 256, 4, 64<<20)
		root := contractRoot(t, 64<<20)
		if err := a.SetCandidateReadAllocationBound(func(int64) (int64, error) { return maximum, nil }); err != nil {
			t.Fatal(err)
		}
		scope := resourcebudget.Scope{ActorID: 1, JobID: 1}
		if err := a.AttachRootBudget(root, scope); err != nil {
			t.Fatal(err)
		}
		if err := a.RestoreBudgetEntry("candidate", backend.PipelineBudgetEntry{Count: 1, Bytes: 512, Durable: true}); err != nil {
			t.Fatal(err)
		}
		before, _ := root.Usage(scope, "job:1:runtime")
		permit, _, err := a.TryAdmit(context.Background(), backend.RequestAdmissionIntent{RequestID: "aux", CandidateIDs: []string{"candidate"}, Stage: backend.RequestStageAlignment}, nil)
		if permit != nil || err == nil {
			t.Fatalf("oversized read arena admitted: maximum=%d err=%v", maximum, err)
		}
		after, _ := root.Usage(scope, "job:1:runtime")
		if before != after || a.PipelineSnapshot().AuxOwners != 0 {
			t.Fatalf("failed grant leaked arena or candidate growth: before=%+v after=%+v", before, after)
		}
	}
}
