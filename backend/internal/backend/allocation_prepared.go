package backend

import (
	"context"
	"sync"
)

// PreparedAllocationTransfer keeps a publication's actual ownership reversible.
// It creates no credit. Commit ends the source reference; Rollback restores the
// original receipt before its holder retires the unpublished bytes.
type PreparedAllocationTransfer struct {
	mu                sync.Mutex
	allocation        *OwnedAllocation
	bounds            *BuildBounds
	source, target    string
	admission         *RequestAdmission
	meter             *providerAllocation
	bytes, paid, wire int64
	settled           bool
}

func PrepareBuildAllocationTransfer(ctx context.Context, allocation *OwnedAllocation, target string) (*PreparedAllocationTransfer, error) {
	if ctx == nil {
		return nil, ErrBuildCapacity
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if allocation == nil {
		return nil, ErrBuildCapacity
	}
	allocation.mu.Lock()
	defer allocation.mu.Unlock()
	b := allocation.bounds
	if b == nil {
		return nil, ErrBuildCapacity
	}
	b.capacityMu.Lock()
	defer b.capacityMu.Unlock()
	a, source := BuildOwnership(b.ctx)
	current, currentSource := BuildOwnership(ctx)
	if b.retired || a == nil || current != a || currentSource != source || !validBuildOwner(target) || target == source || allocation.bytes > b.live || allocation.bytes > b.owned {
		return nil, ErrBuildCapacity
	}
	plan := &PreparedAllocationTransfer{allocation: allocation, bounds: b, admission: a, source: source, target: target, bytes: allocation.bytes, meter: b.meterSource}
	p := b.meterSource
	if p != nil && p.ownership.admission != nil {
		currentMeter, _ := ctx.Value(providerAllocationKey{}).(*providerAllocation)
		policy, _ := ctx.Value(buildAllocationKey{}).(buildAllocationPolicy)
		ownership, _ := ctx.Value(providerResponseOwnershipKey{}).(providerResponseOwnership)
		if currentMeter != p || policy.source != p || ownership != p.ownership {
			return nil, ErrBuildCapacity
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.closed || p.sourceAdmission != a || p.owner != source || p.live < plan.bytes {
			return nil, ErrPipelineCapacity
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.closed {
			return nil, ErrAdmissionClosed
		}
		plan.paid = min(plan.bytes, p.paid)
		plan.wire = min(plan.bytes-plan.paid, p.wireCredit)
		if err := a.transferPaidResponseLocked(p, plan.bytes, target, nil, a.rootScope, ""); err != nil {
			return nil, err
		}
		p.live -= plan.bytes
		p.paid -= plan.paid
		p.wireCredit -= plan.wire
	} else {
		if p != nil {
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.closed || p.paid != 0 || p.live < plan.bytes {
				return nil, ErrBuildCapacity
			}
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.closed {
			return nil, ErrAdmissionClosed
		}
		if err := a.moveBuildBytesLocked(source, target, plan.bytes); err != nil {
			return nil, err
		}
		if p != nil {
			p.live -= plan.bytes
		}
	}
	b.live -= plan.bytes
	b.owned -= plan.bytes
	allocation.bounds = nil
	return plan, nil
}

func (p *PreparedAllocationTransfer) Commit() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.settled {
		return
	}
	p.settled = true
	p.allocation = nil
	p.bounds = nil
	p.meter = nil
	p.admission = nil
}
func (p *PreparedAllocationTransfer) Rollback() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.settled {
		return nil
	}
	allocation, b := p.allocation, p.bounds
	allocation.mu.Lock()
	defer allocation.mu.Unlock()
	b.capacityMu.Lock()
	defer b.capacityMu.Unlock()
	if allocation.bounds != nil {
		return ErrBuildCapacity
	}
	a, m := p.admission, p.meter
	a.mu.Lock()
	closed := a.closed
	a.mu.Unlock()
	if b.retired || closed {
		err := a.ReleaseBuildBytes(p.target, p.bytes)
		if err != nil {
			return err
		}
		p.settled = true
		p.bounds = nil
		p.allocation = nil
		p.admission = nil
		p.meter = nil
		return nil
	}
	if m != nil && m.ownership.admission != nil {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			err := a.ReleaseBuildBytes(p.target, p.bytes)
			if err != nil {
				return err
			}
			p.settled = true
			p.bounds = nil
			p.allocation = nil
			p.admission = nil
			p.meter = nil
			return nil
		}
		defer m.mu.Unlock()
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.closed {
			old := a.buildAllocations[p.target]
			if old < p.bytes {
				return ErrBuildCapacity
			}
			if old == p.bytes {
				delete(a.buildAllocations, p.target)
			} else {
				a.buildAllocations[p.target] = old - p.bytes
			}
			if err := a.syncRootLocked(0); err != nil {
				a.buildAllocations[p.target] = old
				return err
			}
			p.settled = true
			p.bounds = nil
			p.allocation = nil
			p.admission = nil
			p.meter = nil
			return nil
		}
		if a.budget == nil || a.buildAllocations[p.target] < p.bytes {
			return ErrPipelineCapacity
		}
		paid, e := a.responseCreditLocked(m.ownership)
		if e != nil {
			return e
		}
		wire, e := a.responseWireCreditLocked(m.ownership)
		if e != nil {
			return e
		}
		if paid != m.paid || wire != m.wireCredit {
			return ErrPipelineCapacity
		}
		oldTarget, oldSource := a.buildAllocations[p.target], a.buildAllocations[p.source]
		oldExtra := max(m.live-paid-wire, 0)
		nextExtra := max(m.live+p.bytes-paid-p.paid-wire-p.wire, 0)
		nextSource := oldSource - oldExtra + nextExtra
		if nextSource < 0 {
			return ErrBuildCapacity
		}
		a.setResponseCreditLocked(m.ownership, paid+p.paid)
		a.setResponseWireCreditLocked(m.ownership, wire+p.wire)
		a.buildAllocations[p.target] = oldTarget - p.bytes
		a.buildAllocations[p.source] = nextSource
		if e = a.syncRootLocked(0); e != nil {
			a.setResponseCreditLocked(m.ownership, paid)
			a.setResponseWireCreditLocked(m.ownership, wire)
			a.buildAllocations[p.target] = oldTarget
			a.buildAllocations[p.source] = oldSource
			return e
		}
		if a.buildAllocations[p.target] == 0 {
			delete(a.buildAllocations, p.target)
		}
		if a.buildAllocations[p.source] == 0 {
			delete(a.buildAllocations, p.source)
		}
		m.live += p.bytes
		m.paid += p.paid
		m.wireCredit += p.wire
		a.notifyLocked()
	} else {
		if m != nil {
			m.mu.Lock()
			defer m.mu.Unlock()
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.closed || m != nil && m.closed {
			old := a.buildAllocations[p.target]
			if old < p.bytes {
				return ErrBuildCapacity
			}
			if e := a.resizeBuildLocked(p.target, old-p.bytes); e != nil {
				return e
			}
			p.settled = true
			p.bounds = nil
			p.allocation = nil
			p.admission = nil
			p.meter = nil
			return nil
		}
		if e := a.moveBuildBytesLocked(p.target, p.source, p.bytes); e != nil {
			return e
		}
		if m != nil {
			m.live += p.bytes
		}
	}
	b.live += p.bytes
	b.owned += p.bytes
	allocation.bounds = b
	p.settled = true
	p.bounds = nil
	p.allocation = nil
	p.admission = nil
	p.meter = nil
	return nil
}
