package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
)

func TestAllocationPromotionContextCannotManufactureResponseCredit(t *testing.T) {
	r := testExecutionRuntime(t, 1)
	defer r.Close()
	ctx := r.BuildContext(context.Background(), "promotion:restored")
	got, err := r.mainResponseBuildContext(ctx, "restored")
	a, owner := backend.BuildOwnership(ctx)
	gotAdmission, gotOwner := backend.BuildOwnership(got)
	if err != nil || gotAdmission != a || gotOwner != owner || persistence.Deadline(got).IsZero() || backend.HasActiveBuildAllocationSource(got) {
		t.Fatalf("restored inventory acquired a source: %v", err)
	}
	standalone, err := backend.WithProviderResponseAllocation(context.Background(), r.responseLimit())
	if err != nil {
		t.Fatal(err)
	}
	r.retainMainOwner("inactive", nil, nil, standalone)
	for _, deadline := range []bool{false, true} {
		name := "cancelled"
		cancelCtx, cancel := context.WithCancel(ctx)
		want := context.Canceled
		if deadline {
			cancel()
			name = "deadline"
			cancelCtx, cancel = context.WithDeadline(ctx, time.Unix(0, 0))
			want = context.DeadlineExceeded
		} else {
			cancel()
		}
		t.Run(name, func(t *testing.T) {
			before := backend.ProviderResponseSnapshot(standalone)
			got, err := r.mainResponseBuildContext(cancelCtx, "inactive")
			if got != nil || !errors.Is(err, want) {
				t.Fatalf("call cause replaced: got=%v err=%v want=%v", got, err, want)
			}
			if after := backend.ProviderResponseSnapshot(standalone); after != before {
				t.Fatalf("cancelled binding changed original meter: before=%+v after=%+v", before, after)
			}
		})
		cancel()
	}
	if _, err = r.mainResponseBuildContext(ctx, "inactive"); !errors.Is(err, backend.ErrPipelineCapacity) {
		t.Fatalf("unowned meter admitted: %v", err)
	}
	if err = backend.CloseProviderResponseAllocation(standalone); err != nil {
		t.Fatal(err)
	}
	if _, err = r.mainResponseBuildContext(ctx, "inactive"); !errors.Is(err, backend.ErrPipelineCapacity) {
		t.Fatalf("closed meter admitted: %v", err)
	}
	if err = r.releaseMainOwner("inactive"); err != nil {
		t.Fatal(err)
	}

	t.Run("cross_request", func(t *testing.T) {
		for _, id := range []string{"original", "other"} {
			prepared, err := r.Admission.PrepareMain(id, 0, 1, 2, 0, 0, false)
			if err != nil || !prepared {
				t.Fatalf("prepare %s: %t %v", id, prepared, err)
			}
			permit, _, err := r.Admission.TryAdmit(context.Background(), backend.RequestAdmissionIntent{RequestID: id, RoundIndex: 0, Stage: backend.RequestStageMain}, nil)
			if err != nil || permit == nil {
				t.Fatalf("admit %s: %v", id, err)
			}
			defer permit.Release()
		}
		original := backend.WithProviderResponseOwnership(r.BuildContext(context.Background(), "response:original"), r.Admission, "original", backend.RequestStageMain)
		original, err := backend.WithProviderResponseAllocation(original, r.responseLimit())
		if err != nil {
			t.Fatal(err)
		}
		allocation, err := backend.OwnProviderResponseAllocation(original, 8, 1)
		if err != nil {
			t.Fatal(err)
		}
		backing := make([]byte, 8)
		r.retainMainOwner("original", nil, nil, original)
		r.retainMainOwner("other", nil, nil, original)
		before := backend.ProviderResponseSnapshot(original)
		wrong, err := r.mainResponseBuildContext(ctx, "other")
		if wrong != nil || !errors.Is(err, backend.ErrPipelineCapacity) {
			t.Fatalf("cross-request source admitted: %v", err)
		}
		if after := backend.ProviderResponseSnapshot(original); after != before {
			t.Fatalf("cross-request changed source meter: before=%+v after=%+v", before, after)
		}
		valid, err := r.mainResponseBuildContext(ctx, "original")
		if err != nil || !backend.HasActiveBuildAllocationSource(valid) || backend.ProviderResponseSnapshot(valid) != before {
			t.Fatalf("original source lost after rejection: %v", err)
		}
		clear(backing)
		backing = nil
		allocation.Release()
		r.ownerMu.Lock()
		delete(r.mainOwners, "other")
		r.ownerMu.Unlock()
		if err = r.releaseMainOwner("original"); err != nil {
			t.Fatal(err)
		}
	})
}
