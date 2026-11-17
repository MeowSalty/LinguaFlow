package backend

import "context"

type buildAllocationKey struct{}
type buildOwnershipKey struct{}
type buildOwnership struct {
	admission *RequestAdmission
	owner     string
}

func WithBuildOwnership(ctx context.Context, admission *RequestAdmission, owner string) context.Context {
	return context.WithValue(ctx, buildOwnershipKey{}, buildOwnership{admission, owner})
}
func BuildOwnership(ctx context.Context) (*RequestAdmission, string) {
	value, _ := ctx.Value(buildOwnershipKey{}).(buildOwnership)
	return value.admission, value.owner
}

type buildAllocationPolicy struct {
	limit   int64
	reserve func(int64) error
	release func(int64)
	source  *providerAllocation
}

// WithBuildAllocation connects short-build capacity to the existing Root lease.
// Reserve runs before an allocation and must not wait while holding CPU credit.
// Only explicit and observable payload capacities are supplied to callbacks.
func WithBuildAllocation(ctx context.Context, limit int64, reserve func(int64) error, release func(int64)) context.Context {
	return context.WithValue(ctx, buildAllocationKey{}, buildAllocationPolicy{limit: limit, reserve: reserve, release: release})
}

// WithBuildReservation preserves the independently configured optional limit
// while attaching one concrete backing owner to allocation callbacks.
func WithBuildReservation(ctx context.Context, reserve func(int64) error, release func(int64)) context.Context {
	policy, _ := ctx.Value(buildAllocationKey{}).(buildAllocationPolicy)
	policy.reserve, policy.release = reserve, release
	policy.source = nil
	return context.WithValue(ctx, buildAllocationKey{}, policy)
}
func BuildAllocationLimit(ctx context.Context) int64 {
	policy, _ := ctx.Value(buildAllocationKey{}).(buildAllocationPolicy)
	return policy.limit
}

// HasActiveBuildAllocationSource reports an original, admitted shared source
// that can be used to build and transfer a receipt. It performs no allocation
// accounting; the eventual transfer still checks the receipt and live lane.
func HasActiveBuildAllocationSource(ctx context.Context) bool {
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	policy, _ := ctx.Value(buildAllocationKey{}).(buildAllocationPolicy)
	p, _ := ctx.Value(providerAllocationKey{}).(*providerAllocation)
	// Only providerAllocationContext installs source together with its own
	// callbacks. Both public callback setters invalidate that source marker.
	if p == nil || policy.source != p || policy.reserve == nil || policy.release == nil {
		return false
	}
	a, owner := BuildOwnership(ctx)
	ownership, _ := ctx.Value(providerResponseOwnershipKey{}).(providerResponseOwnership)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.rootReserve == nil || p.rootRelease == nil || a == nil || a != p.sourceAdmission || a != p.ownership.admission || owner != p.owner || !validBuildOwner(owner) || ownership != p.ownership {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return false
	}
	paid, err := a.responseCreditLocked(p.ownership)
	wire, wireErr := a.responseWireCreditLocked(p.ownership)
	return err == nil && wireErr == nil && paid == p.paid && wire == p.wireCredit
}
func NewBuildBoundsFromContext(ctx context.Context, out, in int64) (*BuildBounds, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	policy, _ := ctx.Value(buildAllocationKey{}).(buildAllocationPolicy)
	b, err := NewBuildBounds(out, in, policy.limit)
	if err != nil {
		return nil, err
	}
	b.ctx, b.reserve, b.release, b.meterSource = ctx, policy.reserve, policy.release, policy.source
	return b, nil
}
func (b *BuildBounds) CheckContext() error {
	if b.ctx != nil {
		return b.ctx.Err()
	}
	return nil
}
