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
		return false, AnnotateRejection(ErrBuildCapacity, RejectionEvidence{Branch: "promotion_receipt_missing", Operation: "begin_promotion", RequestID: from, CandidateID: to})
	}
	allocation.mu.Lock()
	defer allocation.mu.Unlock()
	b := allocation.bounds
	if b == nil {
		return false, AnnotateRejection(ErrBuildCapacity, RejectionEvidence{Branch: "promotion_receipt_consumed", Operation: "begin_promotion", RequestID: from, CandidateID: to})
	}
	b.capacityMu.Lock()
	defer b.capacityMu.Unlock()
	n := allocation.bytes
	rejectBounds := func(cause error, branch string) error {
		d := b.rejectionEvidenceLocked()
		caller, callerOwner := BuildOwnership(ctx)
		d.CallerOwner, d.CallerLedger, d.TargetLedger = callerOwner, rejectionLedgerID(caller), rejectionLedgerID(a)
		d.Branch, d.Operation, d.RequestID, d.CandidateID = branch, "begin_promotion", from, to
		return AnnotateRejection(cause, d)
	}
	if b.retired || b.ctx == nil || !validBuildOwner(from) || !validBuildOwner(to) || from == to || n <= 0 || n != size || n > b.live || n > b.owned {
		return false, rejectBounds(ErrBuildCapacity, "promotion_receipt_invalid")
	}
	admission, owner := BuildOwnership(ctx)
	originalAdmission, originalOwner := BuildOwnership(b.ctx)
	if admission != a || originalAdmission != a || owner != originalOwner || !validBuildOwner(owner) {
		return false, rejectBounds(ErrPipelineCapacity, "promotion_build_identity_mismatch")
	}
	p := b.meterSource
	policy, _ := ctx.Value(buildAllocationKey{}).(buildAllocationPolicy)
	originalPolicy, _ := b.ctx.Value(buildAllocationKey{}).(buildAllocationPolicy)
	if policy.source != p || originalPolicy.source != p {
		return false, rejectBounds(ErrPipelineCapacity, "promotion_meter_source_mismatch")
	}
	if p != nil {
		p.mu.Lock()
		defer p.mu.Unlock()
		identity, _ := ctx.Value(providerResponseOwnershipKey{}).(providerResponseOwnership)
		if p.closed || p.sourceAdmission != a || p.ownership.admission != a || p.owner != owner || identity != p.ownership || p.ownership.id != from || p.ownership.stage != RequestStageMain || n > p.live {
			d := mergeRejection(p.rejectionEvidenceLocked(), b.rejectionEvidenceLocked())
			d.CallerOwner, d.CallerLedger, d.TargetLedger = owner, rejectionLedgerID(admission), rejectionLedgerID(a)
			d.ProviderIdentityKnown, d.SourceRequestID, d.CallerRequestID, d.SourceStage, d.CallerStage = true, p.ownership.id, identity.id, p.ownership.stage, identity.stage
			d.Branch, d.Operation, d.CandidateID = "promotion_provider_identity_mismatch", "begin_promotion", to
			return false, AnnotateRejection(ErrPipelineCapacity, d)
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	reject := func(cause error, branch string) error {
		d := b.rejectionEvidenceLocked()
		if p != nil {
			d = mergeRejection(p.rejectionEvidenceLocked(), d)
			paid, pe := a.responseCreditLocked(p.ownership)
			wire, we := a.responseWireCreditLocked(p.ownership)
			d.LedgerCreditKnown, d.LedgerPaid, d.LedgerWire = pe == nil && we == nil, paid, wire
		}
		d.RequestID, d.CandidateID, d.SourceBytesKnown, d.SourceBytes = from, to, true, a.buildAllocations[owner]
		return a.rejectLocked(cause, branch, "begin_promotion", d)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if a.closed || a.budget == nil {
		return false, reject(ErrAdmissionClosed, "promotion_admission_closed")
	}
	oldSource := a.buildAllocations[owner]
	oldPaid, oldWire, nextPaid, nextWire := int64(0), int64(0), int64(0), int64(0)
	nextSource := oldSource - n
	if p != nil {
		r, exists := a.budget.requests[from]
		if !exists || !r.granted || r.restored || !r.responseOwned {
			return false, reject(ErrPipelineCapacity, "promotion_live_response_invalid")
		}
		var err error
		oldPaid, err = a.responseCreditLocked(p.ownership)
		if err != nil {
			return false, reject(err, "promotion_response_credit_missing")
		}
		oldWire, err = a.responseWireCreditLocked(p.ownership)
		if err != nil || oldPaid != p.paid || oldWire != p.wireCredit {
			cause := ErrPipelineCapacity
			if err != nil {
				cause = err
			}
			return false, reject(cause, "promotion_response_credit_mismatch")
		}
		nextPaid = max(oldPaid-n, 0)
		nextWire = max(oldWire-max(n-oldPaid, 0), 0)
		oldExtra := max(p.live-oldPaid-oldWire, 0)
		if oldSource < oldExtra {
			return false, reject(ErrBuildCapacity, "promotion_source_credit_insufficient")
		}
		nextSource = oldSource - oldExtra + max(p.live-n-nextPaid-nextWire, 0)
	} else if oldSource < n {
		return false, reject(ErrBuildCapacity, "promotion_source_credit_insufficient")
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
