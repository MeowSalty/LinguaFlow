package backend

import (
	"context"
	"errors"
	"testing"
)

func preparedSource(t *testing.T, paid bool) (*RequestAdmission, context.Context, *BuildBounds) {
	t.Helper()
	a, err := NewRequestAdmission(nil, RequestAdmissionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	ctx := WithBuildOwnership(context.Background(), a, "source")
	ctx = WithBuildReservation(ctx, func(n int64) error { return a.ClaimBuild("source", n) }, func(n int64) {
		if e := a.ReleaseBuildBytes("source", n); e != nil {
			t.Error(e)
		}
	})
	if paid {
		if err = a.ConfigurePipelineBudget(PipelineBudgetLimits{Segments: 1, Candidates: 1024, Item: 1024, Responses: 1024, Handoff: 1 << 20}); err != nil {
			t.Fatal(err)
		}
		a.mu.Lock()
		a.budget.requests["request"] = requestBudget{granted: true, responseOwned: true, raw: 1024, responseCredit: 1024}
		a.budget.mainOwners[0] = 1
		a.mu.Unlock()
		ctx = WithProviderResponseOwnership(ctx, a, "request", RequestStageMain)
		ctx, err = WithProviderResponseAllocation(ctx, 2048)
		if err != nil {
			t.Fatal(err)
		}
	}
	b, err := NewBuildBoundsFromContext(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.CloseRetired)
	return a, ctx, b
}

func TestPreparedAllocationTransferPreservesConcurrentSource(t *testing.T) {
	for _, paid := range []bool{false, true} {
		name := "ordinary"
		if paid {
			name = "paid"
		}
		t.Run(name, func(t *testing.T) {
			a, ctx, b := preparedSource(t, paid)
			first, e := b.OwnAllocation(128, 1)
			if e != nil {
				t.Fatal(e)
			}
			plan, e := PrepareBuildAllocationTransfer(ctx, first, "dictionary")
			if e != nil {
				t.Fatal(e)
			}
			if a.BuildBytes("dictionary") != 128 {
				t.Fatal("target has no actual backing")
			}
			// A second holder changes the same source after Prepare. Rollback must
			// restore the moved part while keeping this new allocation intact.
			second, e := b.OwnAllocation(37, 1)
			if e != nil {
				t.Fatal(e)
			}
			if e = plan.Rollback(); e != nil {
				t.Fatal(e)
			}
			if b.Snapshot().LiveBytes != 165 || a.BuildBytes("dictionary") != 0 {
				t.Fatal("rollback replaced concurrent source")
			}
			if e = plan.Rollback(); e != nil {
				t.Fatal(e)
			}
			first.Release()
			second.Release()
			if b.Snapshot().LiveBytes != 0 || a.BuildBytes("source") != 0 {
				t.Fatal("source backing did not retire")
			}
		})
	}
}

func TestPreparedAllocationTransferAuthenticatesAndSettles(t *testing.T) {
	a, ctx, b := preparedSource(t, false)
	receipt, e := b.OwnAllocation(64, 1)
	if e != nil {
		t.Fatal(e)
	}
	for _, target := range []string{"", "source"} {
		if _, e = PrepareBuildAllocationTransfer(ctx, receipt, target); !errors.Is(e, ErrBuildCapacity) {
			t.Fatal(e)
		}
	}
	wrong := WithBuildOwnership(ctx, a, "other")
	if _, e = PrepareBuildAllocationTransfer(wrong, receipt, "dictionary"); !errors.Is(e, ErrBuildCapacity) {
		t.Fatal(e)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e = PrepareBuildAllocationTransfer(cancelled, receipt, "dictionary"); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if b.Snapshot().LiveBytes != 64 || a.BuildBytes("dictionary") != 0 {
		t.Fatal("rejection mutated receipt")
	}
	plan, e := PrepareBuildAllocationTransfer(ctx, receipt, "dictionary")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = PrepareBuildAllocationTransfer(ctx, receipt, "other"); !errors.Is(e, ErrBuildCapacity) {
		t.Fatal(e)
	}
	receipt.Release()
	plan.Commit()
	plan.Commit()
	if e = plan.Rollback(); e != nil {
		t.Fatal(e)
	}
	if a.BuildBytes("dictionary") != 64 {
		t.Fatal("settled source release retired published target")
	}
	if e = a.ReleaseBuild("dictionary"); e != nil {
		t.Fatal(e)
	}
}

func TestPreparedAllocationTransferCannotReviveRetiredSource(t *testing.T) {
	a, ctx, b := preparedSource(t, false)
	receipt, e := b.OwnAllocation(64, 1)
	if e != nil {
		t.Fatal(e)
	}
	plan, e := PrepareBuildAllocationTransfer(ctx, receipt, "dictionary")
	if e != nil {
		t.Fatal(e)
	}
	b.CloseRetired()
	if e = plan.Rollback(); e != nil {
		t.Fatal(e)
	}
	receipt.Release()
	if a.BuildBytes("source") != 0 || a.BuildBytes("dictionary") != 0 {
		t.Fatal("retired source was revived")
	}
	if e = plan.Rollback(); e != nil {
		t.Fatal(e)
	}
}
