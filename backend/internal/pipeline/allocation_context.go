package pipeline

import (
	"context"
	"errors"
	"fmt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

type buildOwnerKey struct{}

// BuildContext binds capacity to a lifecycle owner rather than a CPU permit.
// A reserve is an atomic nonblocking Root probe; any retry happens after Run
// returns its CPU station. Callers explicitly transfer or release that owner.
func (r *ExecutionRuntime) BuildContext(ctx context.Context, owner string) context.Context {
	if r == nil || r.Admission == nil {
		return ctx
	}
	ctx = context.WithValue(ctx, buildOwnerKey{}, owner)
	ctx = backend.WithBuildOwnership(ctx, r.Admission, owner)
	return backend.WithBuildReservation(ctx, func(bytes int64) error {
		if limit := r.Admission.BuildLimit(); limit > 0 {
			used := r.Admission.BuildBytes(owner)
			needed, err := backend.CheckedAdd(used, bytes)
			if err != nil {
				return err
			}
			if needed > limit {
				return fmt.Errorf("build owner=%s: %w", owner, &backend.AllocationCapacityError{Used: used, Requested: bytes, Limit: limit})
			}
		}
		// Capture before the atomic probe. An external consumer may release
		// between the failed probe and its delivery to the coordinator.
		changed := r.Admission.RootChanged()
		err := r.Admission.ClaimBuildBounded(owner, bytes, backend.BuildAllocationLimit(ctx))
		if errors.Is(err, backend.ErrAggregateCapacity) {
			return &WorkDeferred{Wait: backend.AdmissionWait{Reason: "aggregate_build_bytes", CapacityChanged: changed}, Cause: err}
		}
		return err
	}, func(bytes int64) {
		_ = r.Admission.ReleaseBuildBytes(owner, bytes)
	})
}
