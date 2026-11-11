package backend

import "sync"

// One aux owner reads its candidates serially. The maximum physical DB frame
// and canonical codec arena are promised at dispatch, independently of C/G/R.
// Their transient overlap must not make an already-paid response claim more P.
type candidateReadCredit struct {
	maximum    int64
	candidates []string
	inUse      bool
}

// SetCandidateReadAllocationBound supplies the persistence adapter's proved
// physical-frame/codec allocation bound. It is distinct from logical storage:
// this payload credit exists only until the aux response owner finishes. The
// supplied bound must be monotone and cover every representation up to its
// argument; an unsupported or oversized representation must return an error.
func (a *RequestAdmission) SetCandidateReadAllocationBound(bound func(int64) (int64, error)) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.budget == nil || len(a.budget.entries) > 0 || len(a.budget.requests) > 0 || len(a.candidateReads) > 0 {
		return ErrPipelineCapacity
	}
	if bound != nil {
		physical, err := a.storageBoundLocked(a.budget.limits.Item)
		if err != nil {
			return err
		}
		maximum, err := bound(physical)
		if err != nil {
			return err
		}
		if maximum < physical {
			return ErrBuildCapacity
		}
	}
	a.candidateReadAllocationBound = bound
	return nil
}

// BorrowCandidateRead admits a bounded DB read within an active aux owner's
// prepaid payload. The adapter releases after DB and canonicalization copies
// have ended; CPU ownership separately covers only the short canonical build.
// Without such an owner the caller must acquire its own finite input lease.
func (a *RequestAdmission) BorrowCandidateRead(candidateID string, allocationBytes int64) (release func(), owned bool, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, credit := range a.candidateReads {
		for _, id := range credit.candidates {
			if id != candidateID {
				continue
			}
			if allocationBytes < 0 || allocationBytes > credit.maximum || credit.inUse {
				return nil, true, ErrBuildCapacity
			}
			credit.inUse = true
			var once sync.Once
			return func() {
				once.Do(func() {
					a.mu.Lock()
					credit.inUse = false
					a.mu.Unlock()
				})
			}, true, nil
		}
	}
	return nil, false, nil
}
