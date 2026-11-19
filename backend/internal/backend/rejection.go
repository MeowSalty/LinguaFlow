package backend

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

// RejectionEvidence is a snapshot of the failed decision, not a live owner.
// Callers supply identifiers only: never response text, credentials or payloads.
// Known flags distinguish unavailable evidence from a measured zero.
type RejectionEvidence struct {
	Branch, Operation, Phase, StoreKind                  string
	RequestID, CandidateID, RuntimeID                    string
	MemberKnown                                          bool
	Ordinal, Cursor                                      int
	ScopeKnown                                           bool
	Scope                                                resourcebudget.Scope
	EpochKnown                                           bool
	Epoch                                                int
	SourceOwner, TargetOwner, SourceLedger, TargetLedger string
	CallerOwner, CallerLedger                            string
	ProviderIdentityKnown                                bool
	SourceRequestID, CallerRequestID                     string
	SourceStage, CallerStage                             RequestStage
	MeterID                                              string
	AdmissionStateKnown, AdmissionClosed                 bool
	MeterStateKnown, MeterClosed                         bool
	BoundsStateKnown, BoundsRetired                      bool
	BoundsBytesKnown                                     bool
	BoundsLiveBytes, BoundsOwnedBytes                    int64
	CreditKnown, MeterCreditKnown, LedgerCreditKnown     bool
	LiveBytes, OwnedBytes, MeterPaid, LedgerPaid         int64
	MeterWire, LedgerWire                                int64
	SourceBytesKnown                                     bool
	SourceBytes                                          int64
	InventoryKnown                                       bool
	InventoryMembers, RequestedMembers                   int64
	InventoryBytes, RequestedBytes                       int64
	InventoryGrowth, RequestedGrowth                     int64
	ResponseBytesKnown                                   bool
	FreezeBytes, ExpectedFreezeBytes                     int64
	ResponseBytes, ExpectedResponseBytes                 int64
	BusinessBytes, ExpectedBusinessBytes                 int64
	QuotaKnown                                           bool
	Dimension                                            string
	Used, Requested, Limit                               int64
}

// RejectionError preserves the original cause and its classification.
type RejectionError struct {
	Evidence RejectionEvidence
	cause    error
}

func (e *RejectionError) Unwrap() error { return e.cause }
func (e *RejectionError) Error() string {
	d := e.Evidence
	s := fmt.Sprintf("%v: rejection branch=%s op=%s phase=%s store=%s request=%s candidate=%s runtime=%s source_owner=%s target_owner=%s source_ledger=%s target_ledger=%s meter=%s", e.cause, d.Branch, d.Operation, d.Phase, d.StoreKind, d.RequestID, d.CandidateID, d.RuntimeID, d.SourceOwner, d.TargetOwner, d.SourceLedger, d.TargetLedger, d.MeterID)
	if d.CallerOwner != "" || d.CallerLedger != "" {
		s += fmt.Sprintf(" caller_owner=%s caller_ledger=%s", d.CallerOwner, d.CallerLedger)
	}
	if d.ProviderIdentityKnown {
		s += fmt.Sprintf(" source_request=%s caller_request=%s source_stage=%s caller_stage=%s", d.SourceRequestID, d.CallerRequestID, d.SourceStage, d.CallerStage)
	}
	if d.MemberKnown {
		s += fmt.Sprintf(" ordinal=%d cursor=%d", d.Ordinal, d.Cursor)
	}
	if d.ScopeKnown {
		s += fmt.Sprintf(" actor=%d job=%d call=%s", d.Scope.ActorID, d.Scope.JobID, d.Scope.CallID)
	}
	if d.EpochKnown {
		s += fmt.Sprintf(" epoch=%d", d.Epoch)
	}
	if d.AdmissionStateKnown {
		s += fmt.Sprintf(" admission_closed=%t", d.AdmissionClosed)
	}
	if d.MeterStateKnown {
		s += fmt.Sprintf(" meter_closed=%t", d.MeterClosed)
	}
	if d.BoundsStateKnown {
		s += fmt.Sprintf(" bounds_retired=%t", d.BoundsRetired)
	}
	if d.BoundsBytesKnown {
		s += fmt.Sprintf(" bounds_live=%d bounds_owned=%d", d.BoundsLiveBytes, d.BoundsOwnedBytes)
	}
	if d.MeterCreditKnown || d.CreditKnown {
		s += fmt.Sprintf(" live=%d owned=%d meter_paid=%d meter_wire=%d", d.LiveBytes, d.OwnedBytes, d.MeterPaid, d.MeterWire)
	}
	if d.LedgerCreditKnown || d.CreditKnown {
		s += fmt.Sprintf(" ledger_paid=%d ledger_wire=%d", d.LedgerPaid, d.LedgerWire)
	}
	if d.SourceBytesKnown {
		s += fmt.Sprintf(" source_bytes=%d", d.SourceBytes)
	}
	if d.InventoryKnown {
		s += fmt.Sprintf(" inventory_members=%d requested_members=%d inventory_bytes=%d requested_bytes=%d inventory_growth=%d requested_growth=%d", d.InventoryMembers, d.RequestedMembers, d.InventoryBytes, d.RequestedBytes, d.InventoryGrowth, d.RequestedGrowth)
	}
	if d.ResponseBytesKnown {
		s += fmt.Sprintf(" freeze_bytes=%d expected_freeze=%d response_bytes=%d expected_response=%d business_bytes=%d expected_business=%d", d.FreezeBytes, d.ExpectedFreezeBytes, d.ResponseBytes, d.ExpectedResponseBytes, d.BusinessBytes, d.ExpectedBusinessBytes)
	}
	if d.QuotaKnown {
		s += fmt.Sprintf(" dimension=%s used=%d requested=%d limit=%d", d.Dimension, d.Used, d.Requested, d.Limit)
	}
	return s
}

// AnnotateRejection fills missing caller context without replacing evidence from
// the actual rejecting branch. It performs no logging, locking or ledger probes.
// Thus a coordinator may record the error after all relevant locks are released.
func AnnotateRejection(err error, detail RejectionEvidence) error {
	if err == nil {
		return nil
	}
	var prior *RejectionError
	if errors.As(err, &prior) {
		detail = mergeRejection(prior.Evidence, detail)
	}
	var allocation *AllocationCapacityError
	var quota *resourcebudget.CapacityError
	if errors.As(err, &allocation) {
		detail.QuotaKnown, detail.Dimension = true, "build_allocation_bytes"
		detail.Used, detail.Requested, detail.Limit = allocation.Used, allocation.Requested, allocation.Limit
	} else if errors.As(err, &quota) && quota.Dimension != "" {
		detail.QuotaKnown, detail.Dimension = true, quota.Dimension
		detail.Used, detail.Requested, detail.Limit = quota.Used, quota.Requested, quota.Limit
	}
	// Clone bounded strings so an error cannot keep a large caller backing alive.
	for _, value := range []*string{&detail.Branch, &detail.Operation, &detail.Phase, &detail.StoreKind, &detail.RequestID, &detail.CandidateID, &detail.RuntimeID, &detail.Scope.CallID, &detail.SourceOwner, &detail.TargetOwner, &detail.SourceLedger, &detail.TargetLedger, &detail.CallerOwner, &detail.CallerLedger, &detail.SourceRequestID, &detail.CallerRequestID, &detail.MeterID, &detail.Dimension} {
		*value = strings.Clone((*value)[:min(len(*value), 256)])
	}
	return &RejectionError{Evidence: detail, cause: err}
}

func mergeRejection(d, extra RejectionEvidence) RejectionEvidence {
	for _, pair := range [][2]*string{{&d.Branch, &extra.Branch}, {&d.Operation, &extra.Operation}, {&d.Phase, &extra.Phase}, {&d.StoreKind, &extra.StoreKind}, {&d.RequestID, &extra.RequestID}, {&d.CandidateID, &extra.CandidateID}, {&d.RuntimeID, &extra.RuntimeID}, {&d.SourceOwner, &extra.SourceOwner}, {&d.TargetOwner, &extra.TargetOwner}, {&d.SourceLedger, &extra.SourceLedger}, {&d.TargetLedger, &extra.TargetLedger}, {&d.CallerOwner, &extra.CallerOwner}, {&d.CallerLedger, &extra.CallerLedger}, {&d.MeterID, &extra.MeterID}} {
		if *pair[0] == "" {
			*pair[0] = *pair[1]
		}
	}
	if !d.ProviderIdentityKnown && extra.ProviderIdentityKnown {
		d.ProviderIdentityKnown, d.SourceRequestID, d.CallerRequestID, d.SourceStage, d.CallerStage = true, extra.SourceRequestID, extra.CallerRequestID, extra.SourceStage, extra.CallerStage
	}
	if !d.MemberKnown && extra.MemberKnown {
		d.MemberKnown, d.Ordinal, d.Cursor = true, extra.Ordinal, extra.Cursor
	}
	if !d.ScopeKnown && extra.ScopeKnown {
		d.ScopeKnown, d.Scope = true, extra.Scope
	}
	if !d.EpochKnown && extra.EpochKnown {
		d.EpochKnown, d.Epoch = true, extra.Epoch
	}
	if !d.AdmissionStateKnown && extra.AdmissionStateKnown {
		d.AdmissionStateKnown, d.AdmissionClosed = true, extra.AdmissionClosed
	}
	if !d.MeterStateKnown && extra.MeterStateKnown {
		d.MeterStateKnown, d.MeterClosed = true, extra.MeterClosed
	}
	if !d.BoundsStateKnown && extra.BoundsStateKnown {
		d.BoundsStateKnown, d.BoundsRetired = true, extra.BoundsRetired
	}
	if !d.BoundsBytesKnown && extra.BoundsBytesKnown {
		d.BoundsBytesKnown, d.BoundsLiveBytes, d.BoundsOwnedBytes = true, extra.BoundsLiveBytes, extra.BoundsOwnedBytes
	}
	if !d.CreditKnown && extra.CreditKnown {
		d.CreditKnown, d.LiveBytes, d.OwnedBytes, d.MeterPaid, d.LedgerPaid, d.MeterWire, d.LedgerWire = true, extra.LiveBytes, extra.OwnedBytes, extra.MeterPaid, extra.LedgerPaid, extra.MeterWire, extra.LedgerWire
	}
	if !d.MeterCreditKnown && extra.MeterCreditKnown {
		d.MeterCreditKnown, d.LiveBytes, d.OwnedBytes, d.MeterPaid, d.MeterWire = true, extra.LiveBytes, extra.OwnedBytes, extra.MeterPaid, extra.MeterWire
	}
	if !d.LedgerCreditKnown && extra.LedgerCreditKnown {
		d.LedgerCreditKnown, d.LedgerPaid, d.LedgerWire = true, extra.LedgerPaid, extra.LedgerWire
	}
	if !d.SourceBytesKnown && extra.SourceBytesKnown {
		d.SourceBytesKnown, d.SourceBytes = true, extra.SourceBytes
	}
	if !d.InventoryKnown && extra.InventoryKnown {
		d.InventoryKnown, d.InventoryMembers, d.RequestedMembers, d.InventoryBytes, d.RequestedBytes, d.InventoryGrowth, d.RequestedGrowth = true, extra.InventoryMembers, extra.RequestedMembers, extra.InventoryBytes, extra.RequestedBytes, extra.InventoryGrowth, extra.RequestedGrowth
	}
	if !d.ResponseBytesKnown && extra.ResponseBytesKnown {
		d.ResponseBytesKnown, d.FreezeBytes, d.ExpectedFreezeBytes, d.ResponseBytes, d.ExpectedResponseBytes, d.BusinessBytes, d.ExpectedBusinessBytes = true, extra.FreezeBytes, extra.ExpectedFreezeBytes, extra.ResponseBytes, extra.ExpectedResponseBytes, extra.BusinessBytes, extra.ExpectedBusinessBytes
	}
	return d
}

// RejectionEvidenceFromContext takes a rejection-only snapshot in meter ->
// admission lock order. Do not call it while holding either lock. It never grants
// credit, and returning the evidence retains no context, meter or runtime.
func RejectionEvidenceFromContext(ctx context.Context) RejectionEvidence {
	if ctx == nil {
		return RejectionEvidence{}
	}
	p, _ := ctx.Value(providerAllocationKey{}).(*providerAllocation)
	if p != nil {
		p.mu.Lock()
		defer p.mu.Unlock()
		d := p.rejectionEvidenceLocked()
		caller, callerOwner := BuildOwnership(ctx)
		identity, _ := ctx.Value(providerResponseOwnershipKey{}).(providerResponseOwnership)
		d.CallerOwner, d.CallerLedger = callerOwner, rejectionLedgerID(caller)
		d.ProviderIdentityKnown, d.SourceRequestID, d.CallerRequestID, d.SourceStage, d.CallerStage = true, p.ownership.id, identity.id, p.ownership.stage, identity.stage
		if a := p.ownership.admission; a != nil {
			a.mu.Lock()
			defer a.mu.Unlock()
			d = a.rejectionEvidenceLocked(d)
			paid, e := a.responseCreditLocked(p.ownership)
			wire, we := a.responseWireCreditLocked(p.ownership)
			if e == nil && we == nil {
				d.LedgerCreditKnown, d.LedgerPaid, d.LedgerWire = true, paid, wire
			}
		}
		return d
	}
	a, owner := BuildOwnership(ctx)
	d := RejectionEvidence{SourceOwner: owner}
	if a != nil {
		a.mu.Lock()
		defer a.mu.Unlock()
		d = a.rejectionEvidenceLocked(d)
		d.SourceLedger, d.SourceBytesKnown, d.SourceBytes = fmt.Sprintf("%p", a), true, a.buildAllocations[owner]
	}
	return d
}

func (p *providerAllocation) rejectionEvidenceLocked() RejectionEvidence {
	return RejectionEvidence{RequestID: p.ownership.id, SourceOwner: p.owner, SourceLedger: fmt.Sprintf("%p", p.sourceAdmission), MeterID: fmt.Sprintf("%p", p), MeterStateKnown: true, MeterClosed: p.closed, MeterCreditKnown: true, LiveBytes: p.live, MeterPaid: p.paid, MeterWire: p.wireCredit}
}

func (a *RequestAdmission) rejectionEvidenceLocked(d RejectionEvidence) RejectionEvidence {
	d.TargetLedger, d.RuntimeID, d.ScopeKnown, d.Scope = fmt.Sprintf("%p", a), a.rootID, a.root != nil, a.rootScope
	d.AdmissionStateKnown, d.AdmissionClosed = true, a.closed
	return d
}

func (a *RequestAdmission) rejectLocked(err error, branch, operation string, d RejectionEvidence) error {
	d.Branch, d.Operation = branch, operation
	return AnnotateRejection(err, a.rejectionEvidenceLocked(d))
}

func (b *BuildBounds) rejectionEvidenceLocked() RejectionEvidence {
	d := RejectionEvidence{BoundsStateKnown: true, BoundsRetired: b.retired, BoundsBytesKnown: true, BoundsLiveBytes: b.live, BoundsOwnedBytes: b.owned}
	if b.ctx != nil {
		a, owner := BuildOwnership(b.ctx)
		d.SourceOwner, d.SourceLedger = owner, fmt.Sprintf("%p", a)
	}
	return d
}

func (b *BuildBounds) rejectLocked(err error, branch, operation string, d RejectionEvidence) error {
	d = mergeRejection(d, b.rejectionEvidenceLocked())
	d.Branch, d.Operation = branch, operation
	return AnnotateRejection(err, d)
}

func rejectionLedgerID(a *RequestAdmission) string {
	if a == nil {
		return ""
	}
	return fmt.Sprintf("%p", a)
}
