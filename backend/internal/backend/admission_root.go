package backend

import (
	"errors"
	"fmt"
	"math"

	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

var ErrAggregateCapacity = resourcebudget.ErrCapacity

// SetStorageRepresentationBound lets the persistence adapter supply a proved
// physical representation bound. It changes only reliable storage promises;
// per-Job J/Q and aux input I continue to use canonical encoded payloads.
func (a *RequestAdmission) SetStorageRepresentationBound(bound func(int64) (int64, error)) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.budget == nil || len(a.budget.entries) > 0 || len(a.budget.requests) > 0 {
		return ErrPipelineCapacity
	}
	if bound != nil {
		n, err := bound(a.budget.limits.Item)
		if err != nil {
			return err
		}
		if n < a.budget.limits.Item {
			return ErrPipelineCapacity
		}
	}
	a.storageRepresentationBound = bound
	return nil
}
func (a *RequestAdmission) storageBoundLocked(canonical int64) (int64, error) {
	if canonical < 0 {
		return 0, ErrPipelineCapacity
	}
	if a.storageRepresentationBound == nil {
		return canonical, nil
	}
	n, err := a.storageRepresentationBound(canonical)
	if err != nil {
		return 0, err
	}
	if n < canonical {
		return 0, ErrPipelineCapacity
	}
	return n, nil
}

// RestoreInventory installs header-only totals. Activity subsequently attaches
// individual records by transferring from these totals, never recounting stock.
func (a *RequestAdmission) RestoreInventory(segments, candidate, growth, handoff int64) error {
	if segments < 0 || segments > math.MaxInt || candidate < 0 || growth < 0 || handoff < 0 {
		return ErrPipelineCapacity
	}
	if _, err := checkedSum(candidate, growth, handoff); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	if b == nil || len(b.entries) > 0 || len(b.requests) > 0 || b.inventoryRestored {
		return ErrPipelineCapacity
	}
	b.baseSegments, b.baseCandidates, b.baseGrowth, b.baseHandoff = segments, candidate, growth, handoff
	b.inventoryRestored = true
	return nil
}

// ResetInventoryAfterJoin replaces transient activity with the authoritative
// bounded SQL totals after every HTTP, builder and reliable writer has joined.
// The caller must retain ownership on a read error rather than guess a refund.
func (a *RequestAdmission) ResetInventoryAfterJoin(segments, candidate, growth, handoff int64, metadata ...int64) error {
	if segments < 0 || segments > math.MaxInt || candidate < 0 || growth < 0 || handoff < 0 {
		return ErrPipelineCapacity
	}
	if _, err := checkedSum(candidate, growth, handoff); err != nil {
		return err
	}
	if len(metadata) > 1 || len(metadata) == 1 && metadata[0] < 0 {
		return ErrPipelineCapacity
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	if b == nil {
		return ErrPipelineCapacity
	}
	old := *b
	oldReads := a.candidateReads
	oldMetadata, oldMetadataBase := a.requestMetadata, a.rootMetadataBase
	metadataBase := a.rootMetadataBase
	if len(metadata) > 0 {
		metadataBase = metadata[0]
	} else {
		for _, credit := range oldMetadata {
			retained := credit.actual
			if !credit.settled {
				retained = credit.maximum
			}
			var err error
			metadataBase, err = checkedSum(metadataBase, retained)
			if err != nil {
				return err
			}
		}
	}
	if _, err := checkedSum(candidate, growth, handoff, metadataBase); err != nil {
		return err
	}
	a.candidateReads = nil
	a.requestMetadata = nil
	a.rootMetadataBase = metadataBase
	p, w, q := a.prepareOwners, a.buildWorkers, a.descriptors
	b.baseSegments, b.baseCandidates, b.baseGrowth, b.baseHandoff = segments, candidate, growth, handoff
	b.entries = map[string]PipelineBudgetEntry{}
	b.requests = map[string]requestBudget{}
	b.mainOwners = map[int]int{}
	b.auxOwners = 0
	b.auxInput = 0
	b.auxResponse = 0
	b.auxPrepared = nil
	b.rootStandby = 0
	a.prepareOwners, a.buildWorkers, a.descriptors = 0, 0, 0
	if err := a.syncRootLocked(0); err != nil {
		*b = old
		a.candidateReads = oldReads
		a.requestMetadata, a.rootMetadataBase = oldMetadata, oldMetadataBase
		a.prepareOwners, a.buildWorkers, a.descriptors = p, w, q
		return err
	}
	return nil
}

func (a *RequestAdmission) AttachRootBudget(root *resourcebudget.RootBudget, scope resourcebudget.Scope) error {
	if root == nil || scope.ActorID <= 0 {
		return resourcebudget.ErrInvalid
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.root != nil {
		return resourcebudget.ErrIdentity
	}
	id := fmt.Sprintf("job:%d:runtime", scope.JobID)
	if scope.JobID == 0 {
		id = "call:" + scope.CallID + ":runtime"
	}
	v, err := a.rootVectorLocked()
	if err != nil {
		return err
	}
	if v != (resourcebudget.Vector{}) {
		if err = root.Attach(scope, id, v); err != nil {
			return err
		}
	}
	a.root, a.rootScope, a.rootID = root, scope, id
	return nil
}

func (a *RequestAdmission) rootVectorLocked() (resourcebudget.Vector, error) {
	v := resourcebudget.Vector{PrepareOwners: a.prepareOwners, BuildWorkers: a.buildWorkers, QueuedDescriptors: a.descriptors}
	if b := a.budget; b != nil {
		n, bytes, growth := b.candidateTotals()
		v.UnconfirmedMembers = int64(n)
		payload, err := checkedSum(bytes, growth, b.requestTotal(), b.rootStandby, b.auxInput, b.auxResponse, int64(b.auxOwners)*RequestMetadataBytes)
		if err != nil {
			return v, err
		}
		v.PayloadBytes = payload
		v.PayloadBytes, err = checkedSum(v.PayloadBytes, a.rootMetadataBase)
		if err != nil {
			return v, err
		}
		for _, metadata := range a.requestMetadata {
			v.PayloadBytes, err = checkedSum(v.PayloadBytes, metadata.maximum)
			if err != nil {
				return v, err
			}
		}
		for _, read := range a.candidateReads {
			v.PayloadBytes, err = checkedSum(v.PayloadBytes, read.maximum)
			if err != nil {
				return v, err
			}
		}
		for _, n := range b.auxPrepared {
			v.PayloadBytes, err = checkedSum(v.PayloadBytes, n)
			if err != nil {
				return v, err
			}
		}
		v.AuxResponseOwners = int64(b.auxOwners)
		for _, count := range b.mainOwners {
			v.MainResponseOwners += int64(count)
		}
	}
	return v, nil
}

// reserveRootDispatchLocked runs before RPM is consumed, while the current
// limiter registry and the ready worker are held by the joint grant.
func (a *RequestAdmission) reserveRootDispatchLocked(intent RequestAdmissionIntent, lane int64) error {
	if a.root == nil {
		return nil
	}
	b := a.budget
	if b == nil {
		return ErrPipelineCapacity
	}
	oldR := b.requests[intent.RequestID]
	oldLane, oldStandby := b.lane, b.rootStandby
	oldAux, oldInput, oldResponse := b.auxOwners, b.auxInput, b.auxResponse
	oldAuxPrepared, auxWasPrepared := b.auxPrepared[intent.RequestID]
	oldCount := b.mainOwners[intent.RoundIndex]
	oldEntries := make(map[string]PipelineBudgetEntry, len(intent.CandidateIDs))
	if e, ok := b.entries[intent.RequestID]; ok {
		oldEntries[intent.RequestID] = e
		for _, id := range e.Replaced {
			oldEntries[id] = b.entries[id]
		}
	}
	for _, id := range intent.CandidateIDs {
		oldEntries[id] = b.entries[id]
	}
	oldMainPeak, oldAuxPeak := b.mainOwnerPeak, b.auxOwnerPeak
	defer func() {
		b.requests[intent.RequestID] = oldR
		b.lane, b.rootStandby = oldLane, oldStandby
		b.auxOwners, b.auxInput, b.auxResponse = oldAux, oldInput, oldResponse
		b.mainOwners[intent.RoundIndex] = oldCount
		b.mainOwnerPeak, b.auxOwnerPeak = oldMainPeak, oldAuxPeak
		for id, e := range oldEntries {
			b.entries[id] = e
		}
		if auxWasPrepared {
			b.auxPrepared[intent.RequestID] = oldAuxPrepared
		}
		if intent.Stage == RequestStageAlignment {
			delete(b.requests, intent.RequestID)
		}
	}()
	b.applyGrant(intent, lane)
	storage := int64(0)
	if intent.Stage == RequestStageMain {
		if !oldR.full && !oldR.direct {
			b.rootStandby = lane
		}
		if oldR.full {
			b.rootStandby = 0
		}
		if a.rootScope.JobID > 0 {
			members := int64(0)
			if e, ok := oldEntries[intent.RequestID]; ok {
				members = int64(max(e.Count, e.PlannedCount))
			}
			payloadBound, e := a.storageBoundLocked(b.limits.Item)
			if e != nil {
				return e
			}
			perMember, e := checkedSum(payloadBound, 2048)
			if e != nil {
				return e
			}
			candidateBound, e := CheckedProduct(members, perMember)
			if e != nil {
				return e
			}
			// Freeze contains each member's bounded baseline and fixed receipt/index
			// metadata. These columns may be stored alongside the Q-sized candidate.
			storage, e = checkedSum(b.limits.Responses, oldR.business, candidateBound, oldR.freeze, RequestMetadataBytes)
			if e != nil {
				return e
			}
		}
	} else if a.rootScope.JobID > 0 {
		for _, id := range intent.CandidateIDs {
			entry := oldEntries[id]
			payloadBound, e := a.storageBoundLocked(b.limits.Item)
			if e != nil {
				return e
			}
			growth := max(int64(0), payloadBound-entry.Bytes)
			storage, e = checkedSum(storage, growth, 2048)
			if e != nil {
				return e
			}
		}
		var e error
		storage, e = checkedSum(storage, b.limits.Responses, RequestMetadataBytes)
		if e != nil {
			return e
		}
	}
	if a.rootScope.JobID > 0 {
		if metadata := a.requestMetadata[intent.RequestID]; metadata != nil {
			var e error
			storage, e = checkedSum(storage, metadata.maximum-metadata.actual)
			if e != nil {
				return e
			}
		}
	}
	var read *candidateReadCredit
	if intent.Stage == RequestStageAlignment && a.candidateReadAllocationBound != nil {
		physical, e := a.storageBoundLocked(b.limits.Item)
		if e != nil {
			return e
		}
		maximum, e := a.candidateReadAllocationBound(physical)
		if e != nil {
			return e
		}
		if maximum < physical {
			return ErrBuildCapacity
		}
		read = &candidateReadCredit{maximum: maximum, candidates: append([]string(nil), intent.CandidateIDs...)}
		if a.candidateReads == nil {
			a.candidateReads = map[string]*candidateReadCredit{}
		}
		if _, exists := a.candidateReads[intent.RequestID]; exists {
			return ErrPipelineCapacity
		}
		a.candidateReads[intent.RequestID] = read
	}
	err := a.syncRootLocked(storage)
	if err != nil && read != nil {
		delete(a.candidateReads, intent.RequestID)
	}
	if err == nil && storage > 0 {
		if a.storageCredits == nil {
			a.storageCredits = map[string]int64{}
		}
		a.storageCredits[intent.RequestID] = storage
	}
	if err == nil {
		if metadata := a.requestMetadata[intent.RequestID]; metadata != nil {
			metadata.dispatched = true
		}
	}
	b.requests[intent.RequestID] = oldR
	b.lane, b.rootStandby = oldLane, oldStandby
	b.auxOwners, b.auxInput, b.auxResponse = oldAux, oldInput, oldResponse
	if auxWasPrepared {
		b.auxPrepared[intent.RequestID] = oldAuxPrepared
	}
	b.mainOwners[intent.RoundIndex] = oldCount
	for id, e := range oldEntries {
		b.entries[id] = e
	}
	if intent.Stage == RequestStageAlignment {
		delete(b.requests, intent.RequestID)
	}
	return err
}

func (a *RequestAdmission) syncRootLocked(storageDelta int64) error {
	if a.root == nil {
		return nil
	}
	v, err := a.rootVectorLocked()
	if err != nil {
		return err
	}
	if _, ok := a.root.Usage(a.rootScope, a.rootID); !ok {
		v.LogicalStorageBytes = storageDelta
		if v == (resourcebudget.Vector{}) {
			return nil
		}
		return a.root.Claim(a.rootScope, a.rootID, v)
	}
	return a.root.TransferRuntime(a.rootScope, a.rootID, v, storageDelta)
}

// ConsumeStorage atomically transfers one active dispatch receipt into the
// writer's durable identity. No later absolute runtime update can revive it.
func (a *RequestAdmission) ConsumeStorage(requestID string, bytes int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.root == nil || a.rootScope.JobID == 0 || requestID == "" || bytes < 0 || bytes > a.storageCredits[requestID] {
		return ErrPipelineCapacity
	}
	if bytes == 0 {
		return nil
	}
	if err := a.root.Move(a.rootScope, a.rootID, fmt.Sprintf("job:%d:storage", a.rootScope.JobID), resourcebudget.Vector{LogicalStorageBytes: bytes}); err != nil {
		return err
	}
	a.storageCredits[requestID] -= bytes
	return nil
}

func (a *RequestAdmission) StorageCredit(requestID string) (int64, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	n, ok := a.storageCredits[requestID]
	return n, ok
}
func (a *RequestAdmission) releaseStorageLocked(requestID string) {
	if remaining := a.storageCredits[requestID]; remaining > 0 {
		_ = a.syncRootLocked(-remaining)
	}
	delete(a.storageCredits, requestID)
}

// RefundStorage returns a transaction's proven rollback bytes to its still
// active receipt. Unknown commits must be resolved before this operation.
func (a *RequestAdmission) RefundStorage(requestID string, bytes int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	current, ok := a.storageCredits[requestID]
	if !ok || a.root == nil || bytes < 0 {
		return ErrPipelineCapacity
	}
	next, err := checkedSum(current, bytes)
	if err != nil {
		return err
	}
	if bytes > 0 {
		if err = a.root.Move(a.rootScope, fmt.Sprintf("job:%d:storage", a.rootScope.JobID), a.rootID, resourcebudget.Vector{LogicalStorageBytes: bytes}); err != nil {
			return err
		}
	}
	a.storageCredits[requestID] = next
	return nil
}

// ChangeStation is nonblocking. Preparation and build callbacks cannot hold a
// worker while waiting for another root, network or database capacity.
func (a *RequestAdmission) ChangeStation(prepare, build, descriptors int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	p, w, q := a.prepareOwners, a.buildWorkers, a.descriptors
	add := func(current, delta int64) (int64, error) {
		if delta > 0 && delta > math.MaxInt64-current || delta < -current {
			return 0, ErrPipelineCapacity
		}
		return current + delta, nil
	}
	np, e := add(p, prepare)
	if e != nil {
		return e
	}
	nw, e := add(w, build)
	if e != nil {
		return e
	}
	nq, e := add(q, descriptors)
	if e != nil {
		return e
	}
	a.prepareOwners, a.buildWorkers, a.descriptors = np, nw, nq
	if a.prepareOwners < 0 || a.buildWorkers < 0 || a.descriptors < 0 {
		a.prepareOwners, a.buildWorkers, a.descriptors = p, w, q
		return ErrPipelineCapacity
	}
	if err := a.syncRootLocked(0); err != nil {
		a.prepareOwners, a.buildWorkers, a.descriptors = p, w, q
		return err
	}
	return nil
}
func (a *RequestAdmission) RootChanged() <-chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.root == nil {
		return nil
	}
	return a.root.Changed()
}
func aggregateCapacity(err error) bool { return errors.Is(err, resourcebudget.ErrCapacity) }

func (a *RequestAdmission) classifyRootCapacityLocked(err error) error {
	if !aggregateCapacity(err) || resourcebudget.CapacityHasDrain(err) {
		return err
	}
	if b := a.budget; b != nil {
		if b.auxOwners > 0 {
			return err
		}
		for _, n := range b.mainOwners {
			if n > 0 {
				return err
			}
		}
	}
	return ErrNoCapacityDrain
}

func (a *RequestAdmission) CanDrainAggregate() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if b := a.budget; b != nil {
		if b.auxOwners > 0 {
			return true
		}
		for _, n := range b.mainOwners {
			if n > 0 {
				return true
			}
		}
	}
	return a.root != nil && a.root.CanDrainOutside(a.rootScope)
}

// DetachRoot retains durable S/P stock after the execution has joined. Storage
// consumed by the writer belongs to its separate identity; unused reservations
// are returned here, not persisted as fictitious bytes.
func (a *RequestAdmission) detachRootLocked() error {
	if a.root == nil {
		return nil
	}
	if b := a.budget; b != nil {
		if a.rootScope.JobID == 0 {
			// Synchronous calls have no durable SQL backing. Their scratch raw
			// and candidate storage ends with this joined runtime, independently
			// of any returned Document/input lease owned by the service.
			b.baseSegments, b.baseCandidates, b.baseGrowth, b.baseHandoff = 0, 0, 0, 0
			b.entries = map[string]PipelineBudgetEntry{}
			b.requests = map[string]requestBudget{}
		}
		for id, e := range b.entries {
			if e.PlannedCount > 0 || !e.Durable {
				delete(b.entries, id)
				continue
			}
			if !e.GrowthDurable {
				e.Growth = 0
				b.entries[id] = e
			}
		}
		for id, r := range b.requests {
			if !r.granted {
				delete(b.requests, id)
			} else {
				r.known = r.freeze
				r.wire = 0
				r.full = false
				b.requests[id] = r
			}
		}
		b.mainOwners = map[int]int{}
		b.auxOwners = 0
		b.auxInput, b.auxResponse = 0, 0
		b.auxPrepared = nil
		b.rootStandby = 0
	}
	a.prepareOwners, a.buildWorkers, a.descriptors = 0, 0, 0
	a.candidateReads = nil
	for id := range a.requestMetadata {
		a.releaseRootMetadataLocked(id)
	}
	if a.rootScope.JobID == 0 {
		a.rootMetadataBase = 0
	}
	v, _ := a.root.Usage(a.rootScope, a.rootID)
	a.storageCredits = nil
	if err := a.syncRootLocked(-v.LogicalStorageBytes); err != nil {
		return err
	}
	if _, ok := a.root.Usage(a.rootScope, a.rootID); ok {
		return a.root.Detach(a.rootScope, a.rootID)
	}
	return nil
}
