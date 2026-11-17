package backend

import "sync"

// One active aux owner reads its candidates serially. remaining is existing
// response protocol credit, not a future DB frame or codec allocation estimate.
type candidateReadCredit struct {
	remaining                                   int64
	inputRemaining, wireOriginal, wireRemaining int64
	wireBound                                   bool
	candidates                                  []string
	inUse                                       bool
}

// BorrowCandidateRead identifies an active aux lane for a serial DB read. The
// adapter meters its actual buffers through the same response context; any
// observed capacity above remaining uses Root, never an invented R read limit.
// Without such an owner the caller must acquire its own finite input lease.
func (a *RequestAdmission) BorrowCandidateRead(candidateID string, allocationBytes int64) (release func(), owned bool, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, credit := range a.candidateReads {
		for _, id := range credit.candidates {
			if id != candidateID {
				continue
			}
			if allocationBytes < 0 || credit.inUse {
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
