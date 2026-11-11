package backend

import "fmt"

var ErrProgressRequired = fmt.Errorf("pipeline requires its durable progress owner")
var ErrEarlierScope = fmt.Errorf("%w: earlier unfinished scope has priority", ErrProgressRequired)

// PipelineRuntimeState persists lifecycle identities only. It cannot authorize
// additional inventory, memory or concurrency during recovery.
type PipelineRuntimeState struct {
	Version                               int `json:"version"`
	ProgressID, ResponseID, BusinessOwner string
	ResourceID, RoundIndex                int
}

func (a *RequestAdmission) SignalViewChange() { a.mu.Lock(); defer a.mu.Unlock(); a.notifyLocked() }
func (a *RequestAdmission) RestoreRuntimeState(s PipelineRuntimeState) error {
	if s.Version != 1 || len(s.ProgressID) > 128 || len(s.ResponseID) > 128 || len(s.BusinessOwner) > 128 {
		return ErrPipelineCapacity
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.budget == nil {
		return ErrPipelineCapacity
	}
	if s.BusinessOwner == "" && a.budget.businessOwner != "" {
		if _, ok := a.budget.requests[a.budget.businessOwner]; ok {
			s.BusinessOwner = a.budget.businessOwner
		}
	}
	a.budget.state = s
	a.budget.progressID = s.ProgressID
	a.budget.businessOwner = s.BusinessOwner
	for id, e := range a.budget.entries {
		e.Progress = id == s.ProgressID
		a.budget.entries[id] = e
	}
	a.notifyLocked()
	return nil
}
func (a *RequestAdmission) RuntimeState() PipelineRuntimeState {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.budget == nil {
		return PipelineRuntimeState{Version: 1}
	}
	s := a.budget.state
	s.ProgressID = a.budget.progressID
	s.BusinessOwner = a.budget.businessOwner
	return s
}
func (a *RequestAdmission) PersistedRuntimeState() PipelineRuntimeState {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.budget == nil {
		return PipelineRuntimeState{Version: 1}
	}
	return a.budget.state
}
func (a *RequestAdmission) AttachProgress(s PipelineRuntimeState) error {
	return a.RestoreRuntimeState(s)
}
func (a *RequestAdmission) MemoryProgress(id, response string, resource, round int) (PipelineRuntimeState, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	if b == nil || id == "" || b.progressID != "" && b.progressID != id {
		return PipelineRuntimeState{}, ErrProgressRequired
	}
	s := b.state
	s.Version = 1
	s.ProgressID, s.ResponseID, s.ResourceID, s.RoundIndex = id, response, resource, round
	return s, nil
}
func (b *pipelineBudget) progressCandidateLimit() int64 {
	return max(b.limits.Candidates, b.limits.Item)
}
func (b *pipelineBudget) progressMemberLimit() int64 { return int64(b.limits.Segments) }
func (a *RequestAdmission) RefreshMemoryEpisode() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.budget == nil {
		return ErrPipelineCapacity
	}
	s := a.budget.state
	s.Version = 1
	s.ProgressID = a.budget.progressID
	s.BusinessOwner = a.budget.businessOwner
	if s.ResponseID != "" {
		if _, ok := a.budget.requests[s.ResponseID]; !ok {
			s.ResponseID = ""
		}
	}
	a.budget.state = s
	return nil
}
func (a *RequestAdmission) MoveProgress(previous string, s PipelineRuntimeState) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	if b == nil || s.Version != 1 || s.ProgressID == "" || b.progressID != "" && b.progressID != previous && b.progressID != s.ProgressID {
		return ErrProgressRequired
	}
	for id, e := range b.entries {
		e.Progress = id == s.ProgressID
		b.entries[id] = e
	}
	b.state = s
	b.progressID = s.ProgressID
	b.businessOwner = s.BusinessOwner
	a.notifyLocked()
	return nil
}
func (a *RequestAdmission) MemoryMoveProgress(previous, business, id, response string, resource, round int) (PipelineRuntimeState, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	if b == nil || b.progressID != "" && b.progressID != previous {
		return PipelineRuntimeState{}, ErrProgressRequired
	}
	s := b.state
	s.Version = 1
	s.ProgressID, s.ResponseID, s.ResourceID, s.RoundIndex = id, response, resource, round
	if business != "" {
		if b.businessOwner != business {
			return PipelineRuntimeState{}, ErrProgressRequired
		}
		s.BusinessOwner = ""
	}
	return s, nil
}
