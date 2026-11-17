package service

import (
	"context"
	"unsafe"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

// measureExecutionAllocation funds the measurement's real reflected slots.
// A failed probe unwinds those slots and its owner before outside drain waits.
func measureExecutionAllocation(ctx context.Context, value any, measure func(context.Context, any) (int64, error)) (int64, error) {
	header, err := backend.AllocationBytes(1, int64(unsafe.Sizeof(ExecutionInputLease{})))
	if err != nil {
		return 0, err
	}
	policy, err := backend.NewBuildBoundsFromContext(ctx, 0, 0)
	if err != nil {
		return 0, err
	}
	for {
		state := executionInputState(ctx)
		var changed <-chan struct{}
		if state.root != nil {
			changed = state.root.Changed()
		}
		lease, err := newExecutionInputLease(ctx, header, false)
		var n int64
		if err == nil {
			measurementContext := backend.WithBuildAllocation(ctx, policy.Snapshot().AllocationLimit, lease.RetainPayload, func(n int64) { _ = lease.ReleasePayload(n) })
			n, err = measure(measurementContext, value)
			lease.Close()
		}
		if !resourcebudget.CapacityHasDrain(err) || changed == nil {
			return n, err
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-changed:
		}
	}
}
