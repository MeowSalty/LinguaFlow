package backend_test

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

func contractAdmission(t *testing.T, s, c int, j int64) *backend.RequestAdmission {
	t.Helper()
	a, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: c}, AlignmentConcurrency: 8})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.ConfigurePipelineBudget(backend.PipelineBudgetLimits{Segments: s, Candidates: 16 << 20, Item: 1 << 20, Responses: 1 << 20, Handoff: j}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a
}
func contractPrepare(t *testing.T, a *backend.RequestAdmission, id string, n int, b int64) error {
	t.Helper()
	if _, err := a.PrepareMembers(id, n, 1, 0, nil); err != nil {
		return err
	}
	_, err := a.PrepareMain(id, 0, 1, 8<<10, 0, b, false)
	return err
}

func TestBudgetContractCompleteBatchesAcrossConcurrency(t *testing.T) {
	for _, tc := range []struct{ s, c, want int }{{256, 32, 25}, {321, 32, 32}, {256, 4, 4}} {
		a := contractAdmission(t, tc.s, tc.c, 64<<20)
		held := []*backend.RequestPermit{}
		for i := 0; i < tc.c; i++ {
			id := string(rune('a' + i))
			if err := contractPrepare(t, a, id, 10, 10<<20); err != nil {
				t.Fatal(err)
			}
			before := a.PipelineSnapshot()
			if before.Segments != len(held)*10 {
				t.Fatalf("prepared descriptor took S: %+v", before)
			}
			p, w, err := a.TryAdmit(context.Background(), backend.RequestAdmissionIntent{RequestID: id, RoundIndex: 0, Stage: backend.RequestStageMain}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if p == nil {
				if w.Reason != "member_capacity" {
					t.Fatal(w)
				}
				a.ReleasePrepared(id)
				break
			}
			held = append(held, p)
		}
		if len(held) != tc.want || a.PipelineSnapshot().Segments != tc.want*10 {
			t.Fatalf("s=%d c=%d admitted %d batches, %+v", tc.s, tc.c, len(held), a.PipelineSnapshot())
		}
		for _, p := range held {
			p.Release()
		}
	}
}
func TestBudgetContractOrdinaryAndFullReplaceCredit(t *testing.T) {
	for _, tc := range []struct {
		s, n      int
		j         int64
		b         int64
		wantError bool
	}{{1, 1, 25 << 17, 1 << 20, false}, {10, 10, 16 << 20, 10 << 20, false}, {256, 16, 16 << 20, 16 << 20, true}} {
		a := contractAdmission(t, tc.s, 32, tc.j)
		err := contractPrepare(t, a, "main", tc.n, tc.b)
		if tc.wantError {
			if !errors.Is(err, backend.ErrMainBatchTooLarge) {
				t.Fatalf("self E must refuse before HTTP: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		p, w, err := a.TryAdmit(context.Background(), backend.RequestAdmissionIntent{RequestID: "main", RoundIndex: 0, Stage: backend.RequestStageMain}, nil)
		if err != nil || p == nil {
			t.Fatalf("complete owner refused: %+v %v", w, err)
		}
		s := a.PipelineSnapshot()
		if s.HandoffBytes > tc.j || s.Segments != tc.n {
			t.Fatalf("full owner doubled standby or fragmented batch: %+v", s)
		}
		if s.HandoffBytes != (8<<10)+(1<<20)+backend.RequestMetadataBytes+tc.b || s.ProgressLane != 0 {
			t.Fatalf("full E is not one absolute replacement: %+v", s)
		}
		p.Release()
	}
}
func TestBudgetContractFixedOverageCanDrainWithoutExpansion(t *testing.T) {
	a := contractAdmission(t, 17, 4, 64<<20)
	if err := a.RestoreInventory(20, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := a.RestoreBudgetEntry("old", backend.PipelineBudgetEntry{Count: 1, Reservation: true, Durable: true}); err != nil {
		t.Fatal(err)
	}
	if err := a.DropBudgetMember("old"); err != nil {
		t.Fatal(err)
	}
	if a.PipelineSnapshot().Segments != 19 {
		t.Fatal("overage drain recounted or lost old S")
	}
	if err := contractPrepare(t, a, "new", 16, 0); err != nil {
		t.Fatal(err)
	}
	if p, w, err := a.TryAdmit(context.Background(), backend.RequestAdmissionIntent{RequestID: "new", RoundIndex: 0, Stage: backend.RequestStageMain}, nil); p != nil || err != nil || w.Reason != "member_capacity" {
		t.Fatalf("fixed S expanded: %+v %v", w, err)
	}
}
func TestBudgetContractArithmeticAndStaleLimiterBeforeCall(t *testing.T) {
	if _, err := backend.CheckedProduct(math.MaxInt64, 2); err == nil {
		t.Fatal("unchecked multiplication")
	}
	pool := backend.NewLimiterPool()
	pool.Register(1, 10)
	a, err := backend.NewRequestAdmission(pool, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: math.MaxInt}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err = a.ConfigurePipelineBudget(backend.PipelineBudgetLimits{Segments: 256, Candidates: 16 << 20, Item: 1 << 20, Responses: 1 << 20, Handoff: 64 << 20}); err != nil {
		t.Fatal(err)
	}
	if err = contractPrepare(t, a, "request", 1, 0); err != nil {
		t.Fatal(err)
	}
	p, _, err := a.TryAdmit(context.Background(), backend.RequestAdmissionIntent{RequestID: "request", RoundIndex: 0, BackendID: 1, Stage: backend.RequestStageMain}, nil)
	if err != nil || p == nil {
		t.Fatal(err)
	}
	pool.Refresh(1, 20)
	if !errors.Is(p.StartDispatch(context.Background()), backend.ErrStalePermit) {
		t.Fatal("refreshed limiter accepted old permit")
	}
	p.Release()
}

func TestBudgetContractS17Future16CannotBorrowAndS32FitsOriginal16(t *testing.T) {
	for _, s := range []int{17, 32} {
		a := contractAdmission(t, s, 4, 64<<20)
		if err := a.RestoreInventory(16, 1024, 0, 0); err != nil {
			t.Fatal(err)
		}
		if err := contractPrepare(t, a, "original", 16, 0); err != nil {
			t.Fatal(err)
		}
		p, w, err := a.TryAdmit(context.Background(), backend.RequestAdmissionIntent{RequestID: "original", RoundIndex: 0, ResourceID: 1, Stage: backend.RequestStageMain}, nil)
		if s == 17 {
			if err != nil || p != nil || w.Reason != "member_capacity" || a.PipelineSnapshot().Segments != 16 {
				t.Fatalf("fixed S17 borrowed or lost future16: %+v %v", w, err)
			}
		} else {
			if err != nil || p == nil || a.PipelineSnapshot().Segments != 32 {
				t.Fatalf("S32 did not admit full original16: %+v %v", w, err)
			}
			p.Release()
		}
	}
}
func contractRoot(t *testing.T, storage int64) *resourcebudget.RootBudget {
	l := resourcebudget.Limits{MaxUnconfirmedMembers: 1024, MaxPayloadBytes: 1 << 30, MaxPrepareOwners: 32, MaxMainResponseOwners: 64, MaxAuxResponseOwners: 64, MaxBuildWorkers: 32, MaxQueuedDescriptors: 64, MaxLogicalStorageBytes: storage}
	r, err := resourcebudget.New(resourcebudget.Policy{Actor: l, Instance: l})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestBudgetContractLogicalStorageFullConsumesNoRPM(t *testing.T) {
	r := contractRoot(t, 1<<20)
	a := contractAdmission(t, 256, 4, 64<<20)
	if err := a.AttachRootBudget(r, resourcebudget.Scope{ActorID: 1, JobID: 1}); err != nil {
		t.Fatal(err)
	}
	if err := contractPrepare(t, a, "request", 1, 1<<20); err != nil {
		t.Fatal(err)
	}
	p, w, err := a.TryAdmit(context.Background(), backend.RequestAdmissionIntent{RequestID: "request", RoundIndex: 0, Stage: backend.RequestStageMain}, nil)
	if !errors.Is(err, backend.ErrNoCapacityDrain) || p != nil {
		t.Fatalf("storage bound ignored: %+v %v", w, err)
	}
	if a.PipelineSnapshot().MainOwners[0] != 0 || a.PipelineSnapshot().Segments != 0 {
		t.Fatal("failed joint grant leaked S/response owner")
	}
}

func TestBudgetContractStorageReceiptRollbackAndSiblingIsolation(t *testing.T) {
	r := contractRoot(t, 64<<20)
	a := contractAdmission(t, 256, 4, 64<<20)
	scope := resourcebudget.Scope{ActorID: 1, JobID: 1}
	if err := a.AttachRootBudget(r, scope); err != nil {
		t.Fatal(err)
	}
	var permits []*backend.RequestPermit
	for _, id := range []string{"first", "second"} {
		if err := contractPrepare(t, a, id, 1, 0); err != nil {
			t.Fatal(err)
		}
		p, _, err := a.TryAdmit(context.Background(), backend.RequestAdmissionIntent{RequestID: id, RoundIndex: 0, Stage: backend.RequestStageMain}, nil)
		if err != nil || p == nil {
			t.Fatalf("grant %s: %v", id, err)
		}
		permits = append(permits, p)
	}
	before, _ := r.Usage(scope, "job:1:runtime")
	if err := a.ConsumeStorage("first", 100); err != nil {
		t.Fatal(err)
	}
	if err := a.RefundStorage("first", 100); err != nil {
		t.Fatal(err)
	}
	after, _ := r.Usage(scope, "job:1:runtime")
	if before.LogicalStorageBytes != after.LogicalStorageBytes {
		t.Fatal("known rollback lost receipt")
	}
	if err := a.ConsumeStorage("first", 100); err != nil {
		t.Fatal(err)
	}
	permits[0].Release()
	remaining, _ := r.Usage(scope, "job:1:runtime")
	if remaining.LogicalStorageBytes <= 0 || remaining.LogicalStorageBytes >= before.LogicalStorageBytes {
		t.Fatal("release refunded sibling receipt")
	}
	if err := a.ConsumeStorage("second", 100); err != nil {
		t.Fatal(err)
	}
	if err := a.ConsumeStorage("first", 1); err == nil {
		t.Fatal("retired receipt accepted storage")
	}
	permits[1].Release()
	final, _ := r.Usage(scope, "job:1:runtime")
	stock, _ := r.Usage(scope, "job:1:storage")
	if final.LogicalStorageBytes != 0 || stock.LogicalStorageBytes != 200 {
		t.Fatalf("logical receipt cleanup: runtime=%+v storage=%+v", final, stock)
	}
	if err := a.ChangeStation(math.MinInt64, 0, 0); err == nil {
		t.Fatal("station signed underflow")
	}
}

func TestBudgetContractLoweredCandidateInventoryBlocksPositiveGrowth(t *testing.T) {
	a := contractAdmission(t, 256, 4, 64<<20)
	if err := a.RestoreInventory(2, 17<<20, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := a.RestoreBudgetEntry("active", backend.PipelineBudgetEntry{Count: 1, Bytes: 128, Durable: true}); err != nil {
		t.Fatal(err)
	}
	ok, err := a.ReserveCandidateGrowth("active")
	if err != nil || ok || a.PipelineSnapshot().GrowthBytes != 0 {
		t.Fatalf("over-W minted growth: ok=%v err=%v", ok, err)
	}
}

func TestBudgetContractCallCloseReleasesScratchAndKeepsInputLease(t *testing.T) {
	r := contractRoot(t, 64<<20)
	scope := resourcebudget.Scope{ActorID: 1, CallID: "scratch"}
	input := resourcebudget.Vector{PayloadBytes: 4096, PrepareOwners: 1}
	if err := r.Claim(scope, "input:scratch:1", input); err != nil {
		t.Fatal(err)
	}
	a := contractAdmission(t, 256, 4, 64<<20)
	if err := a.AttachRootBudget(r, scope); err != nil {
		t.Fatal(err)
	}
	if err := contractPrepare(t, a, "main", 2, 0); err != nil {
		t.Fatal(err)
	}
	p, _, err := a.TryAdmit(context.Background(), backend.RequestAdmissionIntent{RequestID: "main", RoundIndex: 0, Stage: backend.RequestStageMain}, nil)
	if err != nil || p == nil {
		t.Fatal(err)
	}
	a.Close()
	if _, ok := r.Usage(scope, "call:scratch:runtime"); ok {
		t.Fatal("short call retained scratch inventory")
	}
	if actual, ok := r.Usage(scope, "input:scratch:1"); !ok || actual != input {
		t.Fatal("runtime retired separately owned returned input lease")
	}
	p.Release()
}

func TestBudgetContractStorageRepresentationPromiseDoesNotChangeJobMemory(t *testing.T) {
	for _, limit := range []int64{4 << 20, 64 << 20} {
		root := contractRoot(t, limit)
		scope := resourcebudget.Scope{ActorID: 1, JobID: 1}
		a := contractAdmission(t, 256, 4, 64<<20)
		if err := a.SetStorageRepresentationBound(func(n int64) (int64, error) { return backend.CheckedProduct(n, 8) }); err != nil {
			t.Fatal(err)
		}
		if err := a.AttachRootBudget(root, scope); err != nil {
			t.Fatal(err)
		}
		if err := contractPrepare(t, a, "request", 1, 0); err != nil {
			t.Fatal(err)
		}
		p, _, err := a.TryAdmit(context.Background(), backend.RequestAdmissionIntent{RequestID: "request", RoundIndex: 0, Stage: backend.RequestStageMain}, nil)
		if limit == 4<<20 {
			if p != nil || !errors.Is(err, backend.ErrNoCapacityDrain) {
				t.Fatalf("physical bound granted without reliable credit: %v", err)
			}
			continue
		}
		if err != nil || p == nil {
			t.Fatal(err)
		}
		credit, ok := a.StorageCredit("request")
		if !ok || credit < 9<<20 {
			t.Fatalf("physical representation receipt=%d", credit)
		}
		if a.PipelineSnapshot().HandoffBytes >= 8<<20 {
			t.Fatal("storage bound silently changed Job J/I/Q")
		}
		p.Release()
	}
	bad := contractAdmission(t, 256, 1, 64<<20)
	if err := bad.SetStorageRepresentationBound(func(n int64) (int64, error) { return n - 1, nil }); err == nil {
		t.Fatal("unproved storage upper accepted")
	}
}
