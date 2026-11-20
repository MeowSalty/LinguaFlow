package backend

import (
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
	"math"
)

// BuildCapacityLease belongs to explicit live backing, independently of a CPU
// turn. A prefunded build uses Retain to claim its finite plan before CPU and
// local BuildBounds metering without charging that same plan again via Reserve.
type BuildCapacityLease struct {
	admission *RequestAdmission
	owner     string
}

func (a *RequestAdmission) BeginBuildCapacity(owner string, requiredBytes int64) (*BuildCapacityLease, error) {
	if err := a.ResizeBuild(owner, requiredBytes); err != nil {
		return nil, err
	}
	return &BuildCapacityLease{a, owner}, nil
}
func validBuildOwner(owner string) bool { return owner != "" && len(owner) <= 4096 }
func (a *RequestAdmission) ClaimBuild(owner string, neededBytes int64) error {
	return a.ClaimBuildBounded(owner, neededBytes, 0)
}

// ClaimBuildBounded checks the total live backing of one short build owner.
// All helpers in that build share this owner, so constructing another local
// BuildBounds cannot restart the optional allocation policy's peak accounting.
func (a *RequestAdmission) ClaimBuildBounded(owner string, neededBytes, allocationLimit int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !validBuildOwner(owner) || neededBytes < 0 || allocationLimit < 0 {
		return a.rejectLocked(ErrBuildCapacity, "build_claim_invalid", "claim_build", RejectionEvidence{SourceOwner: owner})
	}
	next, err := CheckedAdd(a.buildAllocations[owner], neededBytes)
	if err != nil {
		return err
	}
	if allocationLimit > 0 && next > allocationLimit {
		return a.rejectLocked(&AllocationCapacityError{a.buildAllocations[owner], neededBytes, allocationLimit}, "build_allocation_limit", "claim_build", RejectionEvidence{SourceOwner: owner})
	}
	return a.classifyRootCapacityLocked(a.resizeBuildLocked(owner, next))
}
func (a *RequestAdmission) ResizeBuild(owner string, requiredBytes int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !validBuildOwner(owner) || requiredBytes < 0 || requiredBytes > int64(math.MaxInt) {
		return ErrBuildCapacity
	}
	return a.resizeBuildLocked(owner, requiredBytes)
}
func (a *RequestAdmission) resizeBuildLocked(owner string, bytes int64) error {
	if bytes < 0 || bytes > int64(math.MaxInt) {
		return ErrBuildCapacity
	}
	old := a.buildAllocations[owner]
	if a.closed && bytes > old {
		return ErrAdmissionClosed
	}
	if old == bytes {
		return nil
	}
	if a.buildAllocations == nil {
		a.buildAllocations = make(map[string]int64)
	}
	if bytes == 0 {
		delete(a.buildAllocations, owner)
	} else {
		a.buildAllocations[owner] = bytes
	}
	if err := a.syncRootLocked(0); err != nil {
		if old == 0 {
			delete(a.buildAllocations, owner)
		} else {
			a.buildAllocations[owner] = old
		}
		return err
	}
	a.signalChangedLocked()
	if a.closed && len(a.buildAllocations) == 0 {
		return a.detachRootLocked()
	}
	return nil
}

// TransferBuild moves still-live capacity to its next explicit owner and
// returns only the proven dead remainder. A canonical candidate debit does not
// silently cover typed backing; callers release the target when its users end.
func (a *RequestAdmission) TransferBuild(owner string, retainedBytes int64, targetOwner string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !validBuildOwner(owner) || !validBuildOwner(targetOwner) || retainedBytes < 0 || retainedBytes > a.buildAllocations[owner] {
		return a.rejectLocked(ErrBuildCapacity, "build_transfer_source_invalid", "transfer_build", RejectionEvidence{SourceOwner: owner, TargetOwner: targetOwner, SourceBytesKnown: true, SourceBytes: a.buildAllocations[owner], InventoryKnown: true, RequestedBytes: retainedBytes})
	}
	if owner == targetOwner {
		return a.resizeBuildLocked(owner, retainedBytes)
	}
	old, oldTarget := a.buildAllocations[owner], a.buildAllocations[targetOwner]
	next, err := CheckedAdd(oldTarget, retainedBytes)
	if err != nil {
		return err
	}
	if next > int64(math.MaxInt) {
		return ErrBuildCapacity
	}
	delete(a.buildAllocations, owner)
	if next > 0 {
		a.buildAllocations[targetOwner] = next
	}
	if err = a.syncRootLocked(0); err != nil {
		if old > 0 {
			a.buildAllocations[owner] = old
		}
		if oldTarget == 0 {
			delete(a.buildAllocations, targetOwner)
		} else {
			a.buildAllocations[targetOwner] = oldTarget
		}
		return err
	}
	a.signalChangedLocked()
	if a.closed && len(a.buildAllocations) == 0 {
		return a.detachRootLocked()
	}
	return nil
}
func (a *RequestAdmission) ReleaseBuild(owner string) error { return a.ResizeBuild(owner, 0) }

// MoveBuildBytes transfers part of a live owner without retiring its remaining
// backing. The receiver identity must be new when replacing installed fields.
func (a *RequestAdmission) MoveBuildBytes(owner, target string, bytes int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.moveBuildBytesLocked(owner, target, bytes)
}

func (a *RequestAdmission) moveBuildBytesLocked(owner, target string, bytes int64) error {
	if !validBuildOwner(owner) || !validBuildOwner(target) || owner == target || bytes < 0 || bytes > a.buildAllocations[owner] {
		return a.rejectLocked(ErrBuildCapacity, "build_move_source_invalid", "move_build_bytes", RejectionEvidence{SourceOwner: owner, TargetOwner: target, SourceBytesKnown: true, SourceBytes: a.buildAllocations[owner], InventoryKnown: true, RequestedBytes: bytes})
	}
	oldSource, oldTarget := a.buildAllocations[owner], a.buildAllocations[target]
	next, err := CheckedAdd(oldTarget, bytes)
	if err != nil || next > int64(math.MaxInt) {
		return ErrBuildCapacity
	}
	if bytes == 0 {
		return nil
	}
	if oldSource == bytes {
		delete(a.buildAllocations, owner)
	} else {
		a.buildAllocations[owner] = oldSource - bytes
	}
	a.buildAllocations[target] = next
	if err = a.syncRootLocked(0); err != nil {
		a.buildAllocations[owner] = oldSource
		if oldTarget == 0 {
			delete(a.buildAllocations, target)
		} else {
			a.buildAllocations[target] = oldTarget
		}
		return err
	}
	a.signalChangedLocked()
	return nil
}

type BuildAllocationTransfer struct {
	Owner string
	Bytes int64
}

// MoveBuildAllocations atomically distributes live backing from one existing
// source. Repeated target identities are combined, including existing target
// capacity. No owner changes until all identities and arithmetic are valid.
func (a *RequestAdmission) MoveBuildAllocations(source string, transfers []BuildAllocationTransfer) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	oldSource, exists := a.buildAllocations[source]
	if !validBuildOwner(source) || !exists {
		return ErrBuildCapacity
	}
	oldTargets := make(map[string]int64, len(transfers))
	nextTargets := make(map[string]int64, len(transfers))
	total := int64(0)
	for _, transfer := range transfers {
		if !validBuildOwner(transfer.Owner) || transfer.Owner == source || transfer.Bytes < 0 {
			return ErrBuildCapacity
		}
		var err error
		total, err = CheckedAdd(total, transfer.Bytes)
		if err != nil || total > oldSource {
			return ErrBuildCapacity
		}
		next, seen := nextTargets[transfer.Owner]
		if !seen {
			next = a.buildAllocations[transfer.Owner]
			oldTargets[transfer.Owner] = next
		}
		next, err = CheckedAdd(next, transfer.Bytes)
		if err != nil || next > int64(math.MaxInt) {
			return ErrBuildCapacity
		}
		nextTargets[transfer.Owner] = next
	}
	if total == 0 {
		return nil
	}
	if total == oldSource {
		delete(a.buildAllocations, source)
	} else {
		a.buildAllocations[source] = oldSource - total
	}
	for owner, bytes := range nextTargets {
		if bytes > 0 {
			a.buildAllocations[owner] = bytes
		}
	}
	if err := a.syncRootLocked(0); err != nil {
		a.buildAllocations[source] = oldSource
		for owner, bytes := range oldTargets {
			if bytes == 0 {
				delete(a.buildAllocations, owner)
			} else {
				a.buildAllocations[owner] = bytes
			}
		}
		return err
	}
	a.signalChangedLocked()
	return nil
}

// MoveBuildTo transfers the same live backing into a pre-existing external
// lease. Root and scope must match; no release/reclaim gap or second charge is
// introduced. The receiver updates its local usage after this callback succeeds.
func (a *RequestAdmission) MoveBuildTo(owner string, root *resourcebudget.RootBudget, scope resourcebudget.Scope, targetIdentity string, retainedBytes int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !validBuildOwner(owner) || targetIdentity == "" || retainedBytes < 0 || retainedBytes > a.buildAllocations[owner] {
		return ErrBuildCapacity
	}
	if root != a.root || root != nil && scope != a.rootScope {
		return resourcebudget.ErrIdentity
	}
	if retainedBytes == 0 {
		return nil
	}
	if root != nil {
		if err := root.Move(scope, a.rootID, targetIdentity, resourcebudget.Vector{PayloadBytes: retainedBytes}); err != nil {
			return err
		}
	}
	remaining := a.buildAllocations[owner] - retainedBytes
	if remaining == 0 {
		delete(a.buildAllocations, owner)
	} else {
		a.buildAllocations[owner] = remaining
	}
	a.notifyLocked()
	if a.closed && len(a.buildAllocations) == 0 {
		return a.detachRootLocked()
	}
	return nil
}
func (a *RequestAdmission) ReleaseBuildBytes(owner string, bytes int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !validBuildOwner(owner) || bytes < 0 || bytes > a.buildAllocations[owner] {
		return ErrBuildCapacity
	}
	return a.resizeBuildLocked(owner, a.buildAllocations[owner]-bytes)
}
func (a *RequestAdmission) BuildBytes(owner string) int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.buildAllocations[owner]
}

// BuildAllocationSnapshot copies live build ownership for lifecycle diagnostics.
// It does not grant, transfer, or release capacity.
func (a *RequestAdmission) BuildAllocationSnapshot() map[string]int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[string]int64, len(a.buildAllocations))
	for owner, bytes := range a.buildAllocations {
		out[owner] = bytes
	}
	return out
}

// BuildLimit is the existing aggregate policy's absolute single-owner ceiling,
// never a concurrency-divided quota. Zero means both aggregate limits absent.
func (a *RequestAdmission) BuildLimit() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.root == nil {
		return 0
	}
	policy := a.root.Snapshot(a.rootScope.ActorID).Policy
	actor, instance := policy.Actor.MaxPayloadBytes, policy.Instance.MaxPayloadBytes
	if actor == 0 {
		return instance
	}
	if instance == 0 {
		return actor
	}
	return min(actor, instance)
}
func (l *BuildCapacityLease) Bytes() int64 { return l.admission.BuildBytes(l.owner) }
func (l *BuildCapacityLease) Reserve(bytes int64) error {
	return l.admission.ClaimBuild(l.owner, bytes)
}
func (l *BuildCapacityLease) Retain(bytes int64) error {
	return l.admission.ResizeBuild(l.owner, bytes)
}
func (l *BuildCapacityLease) Release(bytes int64) error {
	return l.admission.ReleaseBuildBytes(l.owner, bytes)
}
func (l *BuildCapacityLease) Transfer(bytes int64, target string) error {
	return l.admission.TransferBuild(l.owner, bytes, target)
}
func (l *BuildCapacityLease) Close() error { return l.admission.ReleaseBuild(l.owner) }
