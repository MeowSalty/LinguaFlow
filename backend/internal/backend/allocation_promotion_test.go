package backend

import (
	"context"
	"errors"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

func promotionPaidFixture(t *testing.T) (*RequestAdmission, *resourcebudget.RootBudget, context.Context, RequestAdmissionIntent) {
	t.Helper()
	a, root, ctx, intent := paidCapacityFixture(t, RequestStageMain)
	a.mu.Lock()
	a.budget.limits.Segments, a.budget.limits.Item, a.budget.limits.Candidates = 4, 1024, 4096
	a.budget.entries["paid"] = PipelineBudgetEntry{Reservation: true, Count: 1}
	err := a.syncRootLocked(0)
	a.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return a, root, ctx, intent
}

func TestPaidCapacityPromotionMovesActualPayloadAtFullRoot(t *testing.T) {
	a, root, ctx, intent := promotionPaidFixture(t)
	rawOwner, err := OwnProviderResponseAllocation(ctx, 35, 1)
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 35)
	payloadOwner, err := OwnProviderResponseAllocation(ctx, 939, 1)
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 939)
	before := root.Snapshot(1).Instance.PayloadBytes
	if before != root.Snapshot(1).Policy.Instance.MaxPayloadBytes {
		t.Fatal("Root was not full")
	}
	if ok, err := a.BeginPromotion("paid", "candidate", 939, false); err != nil || ok {
		t.Fatalf("ordinary extra claim unexpectedly fit full Root: %t %v", ok, err)
	}
	ok, err := a.BeginPromotionWithAllocation(ctx, "paid", "candidate", int64(cap(payload)), false, payloadOwner)
	if err != nil || !ok {
		t.Fatalf("actual payload transfer failed: %t %v", ok, err)
	}
	if root.Snapshot(1).Instance.PayloadBytes != before || ProviderResponseSnapshot(ctx).LiveBytes != int64(cap(raw)) {
		t.Fatal("payload duplicated or live raw refunded")
	}
	if err = payloadOwner.ReturnToBuild(); !errors.Is(err, ErrBuildCapacity) {
		t.Fatal("transferred receipt restored")
	}
	payloadOwner.Release()
	if err = a.ApplyPromotion("paid", "candidate", 939); err != nil {
		t.Fatal(err)
	}
	clear(raw)
	raw = nil
	rawOwner.Release()
	finishPaidFixture(t, a, ctx, intent)
	if root.Snapshot(1).Instance.PayloadBytes != int64(cap(payload)) {
		t.Fatal("response close refunded live candidate")
	}
	clear(payload)
	payload = nil
	a.ReleaseBudgetEntry("candidate")
	if root.Snapshot(1).Instance != (resourcebudget.Vector{}) {
		t.Fatal("promotion inventory remained")
	}
}

func TestPaidCapacityPromotionFailurePreservesOriginalReceiptAndCredit(t *testing.T) {
	a, root, ctx, intent := promotionPaidFixture(t)
	owner, err := OwnProviderResponseAllocation(ctx, 939, 1)
	if err != nil {
		t.Fatal(err)
	}
	backing := make([]byte, 939)
	before := ProviderResponseSnapshot(ctx)
	payloadBefore := root.Snapshot(1).Instance.PayloadBytes
	check := func(t *testing.T) {
		t.Helper()
		if ProviderResponseSnapshot(ctx) != before || root.Snapshot(1).Instance.PayloadBytes != payloadBefore || owner.bounds == nil || len(a.BudgetEntries()) != 1 {
			t.Fatal("failed move changed ownership or Root")
		}
	}
	for _, kind := range []string{"size", "request", "stage", "restored", "closed", "root_identity", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			callCtx, size := ctx, int64(939)
			cleanup := func() {}
			switch kind {
			case "cancelled":
				var cancel context.CancelFunc
				callCtx, cancel = context.WithCancel(ctx)
				cancel()
			case "size":
				size++
			case "request":
				callCtx = WithProviderResponseOwnership(ctx, a, "other", RequestStageMain)
			case "stage":
				callCtx = WithProviderResponseOwnership(ctx, a, "paid", RequestStageAlignment)
			case "restored":
				r := a.budget.requests["paid"]
				r.restored = true
				a.budget.requests["paid"] = r
				cleanup = func() { r.restored = false; a.budget.requests["paid"] = r }
			case "closed":
				p := owner.bounds.meterSource
				p.closed = true
				cleanup = func() { p.closed = false }
			case "root_identity":
				other := resourcebudget.Scope{ActorID: 1, CallID: "collision"}
				if err := root.Claim(other, "collision", resourcebudget.Vector{BuildWorkers: 1}); err != nil {
					t.Fatal(err)
				}
				originalID := a.rootID
				a.rootID = "collision"
				cleanup = func() {
					a.rootID = originalID
					if err := root.Transfer(other, "collision", resourcebudget.Vector{}); err != nil {
						t.Fatal(err)
					}
				}
			}
			ok, err := a.BeginPromotionWithAllocation(callCtx, "paid", "candidate", size, false, owner)
			cleanup()
			if ok || err == nil {
				t.Fatalf("invalid %s move succeeded: %t %v", kind, ok, err)
			}
			check(t)
		})
	}
	originalBounds := owner.bounds
	if err = owner.ReturnToBuild(); err != nil {
		t.Fatal("rollback lost original receipt", err)
	}
	if owner.bounds != nil {
		t.Fatal("invalidated handle retained its bounds")
	}
	if ok, err := a.BeginPromotionWithAllocation(ctx, "paid", "candidate", 939, false, owner); ok || !errors.Is(err, ErrBuildCapacity) {
		t.Fatalf("stale receipt reused: %t %v", ok, err)
	}
	if ProviderResponseSnapshot(ctx) != before || root.Snapshot(1).Instance.PayloadBytes != payloadBefore {
		t.Fatal("stale receipt changed live backing")
	}
	owner, err = originalBounds.RetainedAllocation(939)
	if err != nil {
		t.Fatal(err)
	}
	clear(backing)
	backing = nil
	owner.Release()
	finishPaidFixture(t, a, ctx, intent)
	a.ReleaseBudgetEntry("paid")
	if root.Snapshot(1).Instance != (resourcebudget.Vector{}) {
		t.Fatal("failed promotion leaked")
	}
}

func TestAllocationPromotionMovesOrdinaryOwnedBackingWithoutResponseCredit(t *testing.T) {
	a, root := buildAdmission(t, 939)
	if err := a.ConfigurePipelineBudget(PipelineBudgetLimits{Segments: 4, Candidates: 4096, Item: 1024, Responses: 1024, Handoff: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	a.budget.entries["restored"] = PipelineBudgetEntry{Reservation: true, Count: 1}
	ctx := WithBuildOwnership(context.Background(), a, "promotion:restored")
	ctx = WithBuildReservation(ctx, func(n int64) error { return a.ClaimBuild("promotion:restored", n) }, func(n int64) {
		if err := a.ReleaseBuildBytes("promotion:restored", n); err != nil {
			t.Error(err)
		}
	})
	b, err := NewBuildBoundsFromContext(ctx, 1024, 1024)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := b.OwnAllocation(939, 1)
	if err != nil {
		t.Fatal(err)
	}
	backing := make([]byte, 939)
	ok, err := a.BeginPromotionWithAllocation(ctx, "restored", "candidate", int64(cap(backing)), false, owner)
	if err != nil || !ok || root.Snapshot(1).Instance.PayloadBytes != 939 || len(a.budget.requests) != 0 {
		t.Fatalf("ordinary move manufactured credit or duplicated backing: %t %v", ok, err)
	}
	b.CloseRetired()
	clear(backing)
	backing = nil
	a.AbortPromotion("candidate")
	a.ReleaseBudgetEntry("restored")
	if root.Snapshot(1).Instance != (resourcebudget.Vector{}) {
		t.Fatal("ordinary inventory remained")
	}
}
