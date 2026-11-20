package backend

import (
	"context"
	"errors"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
	"reflect"
	"testing"
)

func buildAdmission(t *testing.T, bytes int64) (*RequestAdmission, *resourcebudget.RootBudget) {
	t.Helper()
	r, err := resourcebudget.New(resourcebudget.Policy{Actor: resourcebudget.Limits{MaxPayloadBytes: bytes}, Instance: resourcebudget.Limits{MaxPayloadBytes: bytes}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewRequestAdmission(nil, RequestAdmissionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.AttachRootBudget(r, resourcebudget.Scope{ActorID: 1, CallID: "build"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a, r
}

func TestBuildCapacityLeaseUsesRootAndTransfersLiveBacking(t *testing.T) {
	a, r := buildAdmission(t, 100)
	localChanged, rootChanged, version := a.changed, r.Changed(), a.version
	l, err := a.BeginBuildCapacity("prepare", 80)
	if err != nil {
		t.Fatal(err)
	}
	if l.Bytes() != 80 || r.Snapshot(1).Actor.PayloadBytes != 80 {
		t.Fatal(r.Snapshot(1))
	}
	select {
	case <-localChanged:
	default:
		t.Fatal("successful build claim did not wake local admission")
	}
	select {
	case <-rootChanged:
	default:
		t.Fatal("successful build claim did not wake Root waiters")
	}
	if a.version != version+1 {
		t.Fatal("successful build claim lost its admission turn change")
	}
	localChanged, rootChanged, version = a.changed, r.Changed(), a.version
	if err = l.Reserve(0); err != nil {
		t.Fatal(err)
	}
	if a.version != version || a.changed != localChanged || r.Changed() != rootChanged || l.Bytes() != 80 {
		t.Fatal("unchanged build claim changed admission, Root, or backing")
	}
	if err = l.Transfer(30, "candidate"); err != nil {
		t.Fatal(err)
	}
	if l.Bytes() != 0 || a.BuildBytes("candidate") != 30 || r.Snapshot(1).Actor.PayloadBytes != 30 {
		t.Fatal(r.Snapshot(1))
	}
	if a.version != version+1 || a.changed == localChanged || r.Changed() == rootChanged {
		t.Fatal("successful build transfer did not publish its ownership change")
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	if r.Snapshot(1).Actor.PayloadBytes != 30 {
		t.Fatal("old owner refunded transferred candidate")
	}
	if err = a.ReleaseBuild("candidate"); err != nil {
		t.Fatal(err)
	}
	if r.Snapshot(1).Actor.PayloadBytes != 0 {
		t.Fatal(r.Snapshot(1))
	}
}

func TestBuildCapacityRejectedGrowthLeavesBothLedgersIntact(t *testing.T) {
	a, r := buildAdmission(t, 100)
	l, err := a.BeginBuildCapacity("prepare", 80)
	if err != nil {
		t.Fatal(err)
	}
	localChanged, rootChanged, version := a.changed, r.Changed(), a.version
	if err = l.Reserve(21); !errors.Is(err, ErrNoCapacityDrain) {
		t.Fatal(err)
	}
	if l.Bytes() != 80 || r.Snapshot(1).Actor.PayloadBytes != 80 {
		t.Fatal(r.Snapshot(1))
	}
	if a.version != version || a.changed != localChanged || r.Changed() != rootChanged {
		t.Fatal("rejected build growth published an admission change")
	}
	if err = l.Release(50); err != nil {
		t.Fatal(err)
	}
	if l.Bytes() != 30 || r.Snapshot(1).Actor.PayloadBytes != 30 {
		t.Fatal(r.Snapshot(1))
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBuildCapacitySelfCPUAndPreparationCannotDrainFailedClaim(t *testing.T) {
	a, root := buildAdmission(t, 100)
	if err := a.ChangeStation(1, 1, 0); err != nil {
		t.Fatal(err)
	}
	defer a.ChangeStation(-1, -1, 0)
	if err := a.ClaimBuild("self", 80); err != nil {
		t.Fatal(err)
	}
	err := a.ClaimBuildBounded("self", 21, 0)
	var detail *resourcebudget.CapacityError
	if !errors.Is(err, ErrNoCapacityDrain) || errors.Is(err, ErrAggregateCapacity) || !errors.As(err, &detail) || detail.DrainOutside || detail.Used != 80 || detail.Requested != 21 || detail.Limit != 100 {
		t.Fatalf("self CPU invented a consumer or lost capacity coordinates: %v / %+v", err, detail)
	}
	if a.BuildBytes("self") != 80 || root.Snapshot(1).Actor.PayloadBytes != 80 {
		t.Fatal("rejected claim changed ownership")
	}
	if err := a.ReleaseBuild("self"); err != nil {
		t.Fatal(err)
	}
}

func TestBuildCapacitySameScopeResponseCanDrainFailedClaim(t *testing.T) {
	for _, stage := range []string{"main", "aux"} {
		t.Run(stage, func(t *testing.T) {
			a, root := buildAdmission(t, 100000)
			if err := a.ConfigurePipelineBudget(PipelineBudgetLimits{Segments: 1, Candidates: 1, Item: 1, Responses: 1, Handoff: 1 << 20}); err != nil {
				t.Fatal(err)
			}
			// These are the dispatch-owner counts maintained under a.mu. The
			// integration path tests the corresponding actual HTTP consumers.
			a.mu.Lock()
			if stage == "main" {
				a.budget.mainOwners[0] = 1
			} else {
				a.budget.auxOwners = 1
			}
			a.mu.Unlock()
			defer func() {
				a.mu.Lock()
				a.budget.mainOwners[0], a.budget.auxOwners = 0, 0
				a.mu.Unlock()
				a.ReleaseBuild("waiting")
			}()
			if err := a.ClaimBuild("waiting", 50000); err != nil {
				t.Fatal(err)
			}
			err := a.ClaimBuildBounded("waiting", 60000, 0)
			var detail *resourcebudget.CapacityError
			if !errors.Is(err, ErrAggregateCapacity) || errors.Is(err, ErrNoCapacityDrain) || !errors.As(err, &detail) || detail.DrainOutside {
				t.Fatalf("same-scope response consumer lost: %v / %+v", err, detail)
			}
			if a.BuildAllocationSnapshot()["waiting"] != 50000 || root.Snapshot(1).Actor.PayloadBytes < 50000 {
				t.Fatal("failed claim changed live build ownership")
			}
		})
	}
}

func TestBuildCapacityCloseRetainsOutstandingOwnerUntilRelease(t *testing.T) {
	a, r := buildAdmission(t, 100)
	l, err := a.BeginBuildCapacity("response", 40)
	if err != nil {
		t.Fatal(err)
	}
	a.Close()
	if r.Snapshot(1).Actor.PayloadBytes != 40 {
		t.Fatal("close refunded a live response")
	}
	if err = l.Reserve(1); !errors.Is(err, ErrAdmissionClosed) {
		t.Fatal(err)
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	if s := r.Snapshot(1); s.Actor.PayloadBytes != 0 || s.ActiveIdentities != 0 {
		t.Fatal(s)
	}
}

func TestBuildCapacityMovesToExistingReceiverWithoutDoubleCharge(t *testing.T) {
	a, r := buildAdmission(t, 100)
	scope := resourcebudget.Scope{ActorID: 1, CallID: "build"}
	if err := r.Claim(scope, "input:receiver", resourcebudget.Vector{PayloadBytes: 10}); err != nil {
		t.Fatal(err)
	}
	l, err := a.BeginBuildCapacity("promotion", 80)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.MoveBuildTo("promotion", r, resourcebudget.Scope{ActorID: 2, CallID: "build"}, "input:receiver", 30); !errors.Is(err, resourcebudget.ErrIdentity) {
		t.Fatal(err)
	}
	if l.Bytes() != 80 {
		t.Fatal("rejected transfer changed owner")
	}
	if err = a.MoveBuildTo("promotion", r, scope, "input:receiver", 30); err != nil {
		t.Fatal(err)
	}
	received, ok := r.Usage(scope, "input:receiver")
	if !ok || received.PayloadBytes != 40 || l.Bytes() != 50 || r.Snapshot(1).Actor.PayloadBytes != 90 {
		t.Fatal(received, r.Snapshot(1))
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	if r.Snapshot(1).Actor.PayloadBytes != 40 {
		t.Fatal(r.Snapshot(1))
	}
	if err = r.Transfer(scope, "input:receiver", resourcebudget.Vector{}); err != nil {
		t.Fatal(err)
	}
}

func TestBuildCapacityTemporaryExternalOwnerCanDrainBeforeRetry(t *testing.T) {
	a, root := buildAdmission(t, 100)
	other, err := NewRequestAdmission(nil, RequestAdmissionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(other.Close)
	if err = other.AttachRootBudget(root, resourcebudget.Scope{ActorID: 1, CallID: "other-build"}); err != nil {
		t.Fatal(err)
	}
	// A real preparation station is observable as a consumer that can release
	// its scratch. Merely retaining cold payload is not evidence of a drain.
	if err = a.ChangeStation(1, 0, 0); err != nil {
		t.Fatal(err)
	}
	scratch, err := a.BeginBuildCapacity("temporary-scratch", 80)
	if err != nil {
		t.Fatal(err)
	}
	if err = other.ClaimBuild("next", 30); !errors.Is(err, resourcebudget.ErrCapacity) || !resourcebudget.CapacityHasDrain(err) {
		t.Fatal(err)
	}
	if other.BuildBytes("next") != 0 || root.Snapshot(1).Actor.PayloadBytes != 80 {
		t.Fatal(root.Snapshot(1))
	}
	changed := other.RootChanged()
	if err = scratch.Close(); err != nil {
		t.Fatal(err)
	}
	if err = a.ChangeStation(-1, 0, 0); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	default:
		t.Fatal("scratch release did not notify waiting preparation")
	}
	if err = other.ClaimBuild("next", 30); err != nil {
		t.Fatal(err)
	}
	if root.Snapshot(1).Actor.PayloadBytes != 30 {
		t.Fatal(root.Snapshot(1))
	}
	if err = other.ReleaseBuild("next"); err != nil {
		t.Fatal(err)
	}
}

func TestBuildCapacityOwnerSharesOptionalPeakAcrossLocalBounds(t *testing.T) {
	a, root := buildAdmission(t, 100)
	reserve := func(n int64) error { return a.ClaimBuildBounded("one-stage", n, 32) }
	release := func(n int64) {
		if err := a.ReleaseBuildBytes("one-stage", n); err != nil {
			t.Fatal(err)
		}
	}
	ctx := WithBuildAllocation(context.Background(), 32, reserve, release)
	first, _ := NewBuildBoundsFromContext(ctx, 1, 1)
	second, _ := NewBuildBoundsFromContext(ctx, 1, 1)
	one, err := first.OwnAllocation(16, 1)
	if err != nil {
		t.Fatal(err)
	}
	two, err := second.OwnAllocation(16, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = second.ReserveAllocation(1, 1); !errors.Is(err, ErrBuildCapacity) {
		t.Fatal(err)
	}
	if a.BuildBytes("one-stage") != 32 || root.Snapshot(1).Actor.PayloadBytes != 32 {
		t.Fatal(root.Snapshot(1))
	}
	one.Release()
	two.Release()
	if a.BuildBytes("one-stage") != 0 || root.Snapshot(1).Actor.PayloadBytes != 0 {
		t.Fatal(root.Snapshot(1))
	}
}

func TestBuildCapacityPartialMovePreservesRemainingSource(t *testing.T) {
	a, root := buildAdmission(t, 100)
	if err := a.ClaimBuild("source", 80); err != nil {
		t.Fatal(err)
	}
	localChanged, rootChanged, version := a.changed, root.Changed(), a.version
	if err := a.MoveBuildBytes("source", "field:new", 30); err != nil {
		t.Fatal(err)
	}
	if a.BuildBytes("source") != 50 || a.BuildBytes("field:new") != 30 || root.Snapshot(1).Actor.PayloadBytes != 80 {
		t.Fatal(root.Snapshot(1))
	}
	if a.version != version+1 || a.changed == localChanged || root.Changed() == rootChanged {
		t.Fatal("equal-total backing transfer did not publish its ownership change")
	}
	if err := a.MoveBuildBytes("source", "field:new", 51); !errors.Is(err, ErrBuildCapacity) {
		t.Fatal(err)
	}
	if a.BuildBytes("source") != 50 || a.BuildBytes("field:new") != 30 {
		t.Fatal("rejected transfer mutated owners")
	}
	if err := a.ReleaseBuild("source"); err != nil {
		t.Fatal(err)
	}
	if root.Snapshot(1).Actor.PayloadBytes != 30 {
		t.Fatal("source release retired live receiver")
	}
	if err := a.ReleaseBuild("field:new"); err != nil {
		t.Fatal(err)
	}
}

func TestBuildCapacityBatchMoveIsAtomicAcrossFields(t *testing.T) {
	a, root := buildAdmission(t, 100)
	if err := a.ClaimBuild("source", 80); err != nil {
		t.Fatal(err)
	}
	if err := a.ClaimBuild("existing", 10); err != nil {
		t.Fatal(err)
	}
	before := a.BuildAllocationSnapshot()
	for _, transfers := range [][]BuildAllocationTransfer{
		{{"new:first", 30}, {"new:second", 51}},
		{{"new:first", 30}, {"source", 1}},
		{{"new:first", 30}, {"", 1}},
		{{"new:first", 30}, {"new:second", -1}},
	} {
		if err := a.MoveBuildAllocations("source", transfers); !errors.Is(err, ErrBuildCapacity) {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, a.BuildAllocationSnapshot()) || root.Snapshot(1).Actor.PayloadBytes != 90 {
			t.Fatal("rejected field batch changed ownership", a.BuildAllocationSnapshot(), root.Snapshot(1))
		}
	}
	if err := a.MoveBuildAllocations("missing", []BuildAllocationTransfer{{"new", 0}}); !errors.Is(err, ErrBuildCapacity) {
		t.Fatal("missing source accepted", err)
	}
	// A real Root identity conflict exercises rollback after the tentative
	// local edits. The original Root identity remains intact throughout.
	a.mu.Lock()
	originalScope := a.rootScope
	a.rootScope.CallID = "conflicting-scope"
	a.mu.Unlock()
	err := a.MoveBuildAllocations("source", []BuildAllocationTransfer{{"new:first", 30}, {"existing", 20}})
	a.mu.Lock()
	a.rootScope = originalScope
	a.mu.Unlock()
	if !errors.Is(err, resourcebudget.ErrIdentity) || !reflect.DeepEqual(before, a.BuildAllocationSnapshot()) || root.Snapshot(1).Actor.PayloadBytes != 90 {
		t.Fatal("root rejection failed to roll back complete field batch", err, a.BuildAllocationSnapshot(), root.Snapshot(1))
	}
	localChanged, rootChanged, version := a.changed, root.Changed(), a.version
	if err = a.MoveBuildAllocations("source", []BuildAllocationTransfer{{"new:first", 10}, {"existing", 20}, {"new:first", 20}}); err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"source": 30, "existing": 30, "new:first": 30}
	if !reflect.DeepEqual(want, a.BuildAllocationSnapshot()) || root.Snapshot(1).Actor.PayloadBytes != 90 {
		t.Fatal("successful batch lost capacity", a.BuildAllocationSnapshot(), root.Snapshot(1))
	}
	if a.version != version+1 || a.changed == localChanged || root.Changed() == rootChanged {
		t.Fatal("equal-total field batch did not publish its ownership change")
	}
	if err = a.ReleaseBuild("source"); err != nil || root.Snapshot(1).Actor.PayloadBytes != 60 {
		t.Fatal("source release refunded live targets", err, root.Snapshot(1))
	}
	for _, owner := range []string{"existing", "new:first"} {
		if err = a.ReleaseBuild(owner); err != nil {
			t.Fatal(err)
		}
	}
	if root.Snapshot(1).Actor.PayloadBytes != 0 {
		t.Fatal(root.Snapshot(1))
	}
}
