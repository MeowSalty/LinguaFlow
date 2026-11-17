package backend

import (
	"context"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

type providerResponseOwnershipKey struct{}
type providerResponseOwnership struct {
	admission *RequestAdmission
	id        string
	stage     RequestStage
}

// WithProviderResponseOwnership identifies the existing response protocol lane.
// It adds no allocation credit and does not change the R I/O limit.
func WithProviderResponseOwnership(ctx context.Context, admission *RequestAdmission, id string, stage RequestStage) context.Context {
	return context.WithValue(ctx, providerResponseOwnershipKey{}, providerResponseOwnership{admission, id, stage})
}

// TransferProviderResponseToBuild moves actual live backing and the credit which
// already covers it. The source handle is invalidated only after atomic success.
func TransferProviderResponseToBuild(ctx context.Context, allocation *OwnedAllocation, target string) error {
	if p := allocationMeter(allocation); p == nil || p.ownership.admission == nil {
		return TransferBuildAllocationToBuild(ctx, allocation, target)
	}
	return transferProviderResponse(ctx, allocation, target, nil, resourcebudget.Scope{}, "")
}

func TransferBuildAllocationToBuild(ctx context.Context, allocation *OwnedAllocation, target string) error {
	if p := allocationMeter(allocation); p != nil && p.ownership.admission != nil {
		return transferProviderResponse(ctx, allocation, target, nil, resourcebudget.Scope{}, "")
	}
	a, owner := BuildOwnership(ctx)
	if a == nil || allocation == nil {
		return ErrBuildCapacity
	}
	allocation.mu.Lock()
	defer allocation.mu.Unlock()
	b := allocation.bounds
	if b == nil {
		return ErrBuildCapacity
	}
	b.capacityMu.Lock()
	defer b.capacityMu.Unlock()
	if b.retired || b.ctx == nil {
		return ErrBuildCapacity
	}
	if source, sourceOwner := BuildOwnership(b.ctx); source != a || sourceOwner != owner {
		return ErrBuildCapacity
	}
	if p := b.meterSource; p != nil {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.paid != 0 || allocation.bytes > p.live {
			return ErrBuildCapacity
		}
	}
	if err := a.MoveBuildBytes(owner, target, allocation.bytes); err != nil {
		return err
	}
	if p := b.meterSource; p != nil {
		p.live -= allocation.bytes
	}
	b.live -= allocation.bytes
	b.owned -= allocation.bytes
	allocation.bounds = nil
	return nil
}

// ApplyProviderResponseHandoff installs the actual raw result while retaining
// the live session's remaining R through promotion and final consumption.
// Restored inventory has only its actual raw credit; it never regains this R.
func ApplyProviderResponseHandoff(ctx context.Context, id string, raw int64) error {
	p, ok := ctx.Value(providerAllocationKey{}).(*providerAllocation)
	if !ok || p.ownership.id != id || p.ownership.stage != RequestStageMain {
		return ErrPipelineCapacity
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	a := p.ownership.admission
	if a == nil {
		return ErrPipelineCapacity
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.budget == nil || !validBuildOwner(p.owner) {
		return ErrPipelineCapacity
	}
	old, ok := a.budget.requests[id]
	if !ok || !old.granted || raw < 0 || raw > a.budget.limits.Responses || old.responseCredit != p.paid || old.providerWireRemaining != p.wireCredit {
		return ErrPipelineCapacity
	}
	next := old
	next.known, next.wire, next.raw = next.freeze, 0, raw
	next.providerWireRemaining = 0
	oldExtra := max(p.live-p.paid-p.wireCredit, 0)
	newExtra := max(p.live-next.responseCredit, 0)
	oldSource := a.buildAllocations[p.owner]
	if oldSource < oldExtra {
		return ErrBuildCapacity
	}
	source, err := CheckedAdd(oldSource-oldExtra, newExtra)
	if err != nil {
		return err
	}
	a.budget.requests[id] = next
	if source == 0 {
		delete(a.buildAllocations, p.owner)
	} else {
		if a.buildAllocations == nil {
			a.buildAllocations = map[string]int64{}
		}
		a.buildAllocations[p.owner] = source
	}
	if err = a.syncRootLocked(0); err != nil {
		a.budget.requests[id] = old
		if oldSource == 0 {
			delete(a.buildAllocations, p.owner)
		} else {
			a.buildAllocations[p.owner] = oldSource
		}
		return err
	}
	p.paid = next.responseCredit
	p.wireCredit = 0
	a.notifyLocked()
	return nil
}
func TransferProviderResponseToLease(ctx context.Context, allocation *OwnedAllocation, root *resourcebudget.RootBudget, scope resourcebudget.Scope, identity string) error {
	return transferProviderResponse(ctx, allocation, "", root, scope, identity)
}

func transferProviderResponse(ctx context.Context, allocation *OwnedAllocation, target string, root *resourcebudget.RootBudget, scope resourcebudget.Scope, identity string) error {
	p, ok := ctx.Value(providerAllocationKey{}).(*providerAllocation)
	if !ok || allocation == nil {
		return ErrBuildCapacity
	}
	allocation.mu.Lock()
	defer allocation.mu.Unlock()
	b := allocation.bounds
	if b == nil {
		return ErrBuildCapacity
	}
	b.capacityMu.Lock()
	defer b.capacityMu.Unlock()
	if b.retired || b.meterSource != p || allocation.bytes > b.live || allocation.bytes > b.owned {
		return ErrBuildCapacity
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	n := allocation.bytes
	if n < 0 || n > p.live {
		return ErrBuildCapacity
	}
	a := p.ownership.admission
	if a == nil || !validBuildOwner(p.owner) {
		return ErrBuildCapacity
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.transferPaidResponseLocked(p, n, target, root, scope, identity); err != nil {
		return err
	}
	b.live -= n
	b.owned -= n
	allocation.bounds = nil
	p.live -= n
	wireSpent := max(n-p.paid, 0)
	p.paid = max(p.paid-n, 0)
	p.wireCredit = max(p.wireCredit-wireSpent, 0)
	return nil
}

func (a *RequestAdmission) responseCreditLocked(ownership providerResponseOwnership) (int64, error) {
	if a.budget == nil || ownership.id == "" {
		return 0, ErrPipelineCapacity
	}
	if ownership.stage == RequestStageMain {
		r, ok := a.budget.requests[ownership.id]
		if !ok || !r.granted {
			return 0, ErrPipelineCapacity
		}
		return r.responseCredit, nil
	}
	if ownership.stage == RequestStageAlignment {
		credit := a.candidateReads[ownership.id]
		if credit == nil {
			return 0, ErrPipelineCapacity
		}
		return credit.remaining, nil
	}
	return 0, ErrPipelineCapacity
}
func (a *RequestAdmission) setResponseCreditLocked(ownership providerResponseOwnership, next int64) {
	if ownership.stage == RequestStageMain {
		r := a.budget.requests[ownership.id]
		r.responseCredit = next
		a.budget.requests[ownership.id] = r
	} else {
		credit := a.candidateReads[ownership.id]
		a.budget.auxResponse += next - credit.remaining
		credit.remaining = next
	}
}

// The explicit RequestCredits.InputBytes is the measured encoded body K.
// Pipeline pays K+System+User, so only this body subcredit may be reused here.
func (a *RequestAdmission) bindProviderWireCreditLocked(o providerResponseOwnership, wire int64) error {
	if wire < 0 || a.budget == nil {
		return ErrPipelineCapacity
	}
	if o.stage == RequestStageMain {
		r, ok := a.budget.requests[o.id]
		if !ok || !r.granted {
			return ErrPipelineCapacity
		}
		if r.providerWireBound {
			if wire != r.providerWireOriginal {
				return ErrPipelineCapacity
			}
			return nil
		}
		if wire > r.wire || wire > r.known-r.freeze {
			return ErrPipelineCapacity
		}
		r.providerWireOriginal, r.providerWireRemaining, r.providerWireBound = wire, wire, true
		a.budget.requests[o.id] = r
	} else {
		c := a.candidateReads[o.id]
		if c == nil {
			return ErrPipelineCapacity
		}
		if c.wireBound {
			if wire != c.wireOriginal {
				return ErrPipelineCapacity
			}
			return nil
		}
		if wire > c.inputRemaining {
			return ErrPipelineCapacity
		}
		c.wireOriginal, c.wireRemaining, c.wireBound = wire, wire, true
	}
	return nil
}
func (a *RequestAdmission) responseWireCreditLocked(o providerResponseOwnership) (int64, error) {
	if a.budget == nil {
		return 0, ErrPipelineCapacity
	}
	if o.stage == RequestStageMain {
		r, ok := a.budget.requests[o.id]
		if !ok || !r.granted || r.providerWireRemaining < 0 || r.providerWireRemaining > r.providerWireOriginal || r.providerWireRemaining > r.known-r.freeze {
			return 0, ErrPipelineCapacity
		}
		return r.providerWireRemaining, nil
	}
	c := a.candidateReads[o.id]
	if c == nil || c.wireRemaining < 0 || c.wireRemaining > c.wireOriginal || c.wireRemaining > c.inputRemaining {
		return 0, ErrPipelineCapacity
	}
	return c.wireRemaining, nil
}
func (a *RequestAdmission) setResponseWireCreditLocked(o providerResponseOwnership, next int64) {
	if o.stage == RequestStageMain {
		r := a.budget.requests[o.id]
		r.known += next - r.providerWireRemaining
		r.providerWireRemaining = next
		a.budget.requests[o.id] = r
	} else {
		c := a.candidateReads[o.id]
		delta := next - c.wireRemaining
		c.inputRemaining += delta
		a.budget.auxInput += delta
		c.wireRemaining = next
	}
}
func (a *RequestAdmission) transferPaidResponseLocked(p *providerAllocation, n int64, target string, root *resourcebudget.RootBudget, scope resourcebudget.Scope, identity string) error {
	paid, err := a.responseCreditLocked(p.ownership)
	wire, wireErr := a.responseWireCreditLocked(p.ownership)
	if err != nil || wireErr != nil || paid != p.paid || wire != p.wireCredit {
		return ErrPipelineCapacity
	}
	if target == "" {
		if identity == "" || identity == a.rootID || root != a.root || root != nil && scope != a.rootScope {
			return resourcebudget.ErrIdentity
		}
	} else if !validBuildOwner(target) || target == p.owner {
		return ErrBuildCapacity
	}
	oldExtra := max(p.live-paid-wire, 0)
	nextPaid := max(paid-n, 0)
	nextWire := max(wire-max(n-paid, 0), 0)
	nextExtra := max(p.live-n-nextPaid-nextWire, 0)
	oldSource := a.buildAllocations[p.owner]
	if oldSource < oldExtra {
		return ErrBuildCapacity
	}
	oldTarget := a.buildAllocations[target]
	nextTarget, err := CheckedAdd(oldTarget, n)
	if err != nil {
		return err
	}
	a.setResponseCreditLocked(p.ownership, nextPaid)
	a.setResponseWireCreditLocked(p.ownership, nextWire)
	nextSource := oldSource - oldExtra + nextExtra
	if nextSource == 0 {
		delete(a.buildAllocations, p.owner)
	} else {
		a.buildAllocations[p.owner] = nextSource
	}
	rollback := func() {
		a.setResponseCreditLocked(p.ownership, paid)
		a.setResponseWireCreditLocked(p.ownership, wire)
		if oldSource == 0 {
			delete(a.buildAllocations, p.owner)
		} else {
			a.buildAllocations[p.owner] = oldSource
		}
		if target != "" {
			if oldTarget == 0 {
				delete(a.buildAllocations, target)
			} else {
				a.buildAllocations[target] = oldTarget
			}
		}
	}
	if target != "" {
		if a.buildAllocations == nil {
			a.buildAllocations = map[string]int64{}
		}
		a.buildAllocations[target] = nextTarget
		if err = a.syncRootLocked(0); err != nil {
			rollback()
			return err
		}
	} else if root != nil {
		if err = root.Move(scope, a.rootID, identity, resourcebudget.Vector{PayloadBytes: n}); err != nil {
			rollback()
			return err
		}
	}
	a.notifyLocked()
	return nil
}

// TransferBuildAllocationToLease also supports ordinary explicitly-owned builds.
// owned=false leaves the handle untouched so a caller without a source can use
// its own accurately sized receiver lease.
func TransferBuildAllocationToLease(ctx context.Context, allocation *OwnedAllocation, root *resourcebudget.RootBudget, scope resourcebudget.Scope, identity string) (owned bool, err error) {
	if p := allocationMeter(allocation); p != nil && p.ownership.admission != nil {
		return true, TransferProviderResponseToLease(ctx, allocation, root, scope, identity)
	}
	a, owner := BuildOwnership(ctx)
	if a == nil {
		return false, nil
	}
	if allocation == nil {
		return true, ErrBuildCapacity
	}
	allocation.mu.Lock()
	defer allocation.mu.Unlock()
	b := allocation.bounds
	if b == nil {
		return true, ErrBuildCapacity
	}
	b.capacityMu.Lock()
	defer b.capacityMu.Unlock()
	if b.retired || b.ctx == nil {
		return true, ErrBuildCapacity
	}
	if source, sourceOwner := BuildOwnership(b.ctx); source != a || sourceOwner != owner {
		return true, ErrBuildCapacity
	}
	if p := b.meterSource; p != nil {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.paid != 0 || allocation.bytes > p.live {
			return true, ErrBuildCapacity
		}
	}
	if err = a.MoveBuildTo(owner, root, scope, identity, allocation.bytes); err != nil {
		return true, err
	}
	if p := b.meterSource; p != nil {
		p.live -= allocation.bytes
	}
	b.live -= allocation.bytes
	b.owned -= allocation.bytes
	allocation.bounds = nil
	return true, nil
}

func allocationMeter(allocation *OwnedAllocation) *providerAllocation {
	if allocation == nil {
		return nil
	}
	allocation.mu.Lock()
	defer allocation.mu.Unlock()
	if allocation.bounds == nil {
		return nil
	}
	allocation.bounds.capacityMu.Lock()
	defer allocation.bounds.capacityMu.Unlock()
	return allocation.bounds.meterSource
}
