package backend

import "context"

// BeginPromotionWithAllocation moves the original candidate payload into its
// pending inventory before the single Root probe. The member stays with raw
// until ApplyPromotion acknowledges the save. No additional payload is made.
func (a *RequestAdmission) BeginPromotionWithAllocation(ctx context.Context, from, to string, size int64, progress bool, allocation *OwnedAllocation) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if allocation == nil {
		return false, ErrBuildCapacity
	}
	allocation.mu.Lock()
	defer allocation.mu.Unlock()
	b := allocation.bounds
	if b == nil {
		return false, ErrBuildCapacity
	}
	b.capacityMu.Lock()
	defer b.capacityMu.Unlock()
	n := allocation.bytes
	if b.retired || b.ctx == nil || !validBuildOwner(from) || !validBuildOwner(to) || from == to || n <= 0 || n != size || n > b.live || n > b.owned {
		return false, ErrBuildCapacity
	}
	admission, owner := BuildOwnership(ctx)
	originalAdmission, originalOwner := BuildOwnership(b.ctx)
	if admission != a || originalAdmission != a || owner != originalOwner || !validBuildOwner(owner) {
		return false, ErrPipelineCapacity
	}
	p := b.meterSource
	policy, _ := ctx.Value(buildAllocationKey{}).(buildAllocationPolicy)
	originalPolicy, _ := b.ctx.Value(buildAllocationKey{}).(buildAllocationPolicy)
	if policy.source != p || originalPolicy.source != p {
		return false, ErrPipelineCapacity
	}
	if p != nil {
		p.mu.Lock()
		defer p.mu.Unlock()
		identity, _ := ctx.Value(providerResponseOwnershipKey{}).(providerResponseOwnership)
		if p.closed || p.sourceAdmission != a || p.ownership.admission != a || p.owner != owner || identity != p.ownership || p.ownership.id != from || p.ownership.stage != RequestStageMain || n > p.live {
			return false, ErrPipelineCapacity
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if a.closed || a.budget == nil {
		return false, ErrAdmissionClosed
	}
	oldSource := a.buildAllocations[owner]
	oldPaid, oldWire, nextPaid, nextWire := int64(0), int64(0), int64(0), int64(0)
	nextSource := oldSource - n
	if p != nil {
		r, exists := a.budget.requests[from]
		if !exists || !r.granted || r.restored || !r.responseOwned {
			return false, ErrPipelineCapacity
		}
		var err error
		oldPaid, err = a.responseCreditLocked(p.ownership)
		if err != nil {
			return false, err
		}
		oldWire, err = a.responseWireCreditLocked(p.ownership)
		if err != nil || oldPaid != p.paid || oldWire != p.wireCredit {
			return false, ErrPipelineCapacity
		}
		nextPaid = max(oldPaid-n, 0)
		nextWire = max(oldWire-max(n-oldPaid, 0), 0)
		oldExtra := max(p.live-oldPaid-oldWire, 0)
		if oldSource < oldExtra {
			return false, ErrBuildCapacity
		}
		nextSource = oldSource - oldExtra + max(p.live-n-nextPaid-nextWire, 0)
	} else if oldSource < n {
		return false, ErrBuildCapacity
	}
	setSource := func(bytes int64) {
		if bytes == 0 {
			delete(a.buildAllocations, owner)
		} else {
			a.buildAllocations[owner] = bytes
		}
	}
	move := func() error {
		if p != nil {
			a.setResponseCreditLocked(p.ownership, nextPaid)
			a.setResponseWireCreditLocked(p.ownership, nextWire)
		}
		setSource(nextSource)
		return nil
	}
	rollback := func() {
		if p != nil {
			a.setResponseCreditLocked(p.ownership, oldPaid)
			a.setResponseWireCreditLocked(p.ownership, oldWire)
		}
		setSource(oldSource)
	}
	granted, err := a.beginPromotionLocked(from, to, size, progress, move, rollback)
	if err != nil || !granted {
		return granted, err
	}
	b.live -= n
	b.owned -= n
	allocation.bounds = nil
	if p != nil {
		p.live -= n
		p.paid = nextPaid
		p.wireCredit = nextWire
	}
	return true, nil
}
