package backend

import "fmt"

var ErrProgressRequired = fmt.Errorf("pipeline requires its durable progress owner")
var ErrEarlierScope = fmt.Errorf("%w: earlier unfinished scope has priority", ErrProgressRequired)

func (a *RequestAdmission) SignalViewChange() { a.mu.Lock(); defer a.mu.Unlock(); a.notifyLocked() }

// PipelineRuntimeState is fixed-size metadata. Inventory caps are captured once
// per episode and are independent of the presence of a progress attachment.
type PipelineRuntimeState struct {
	Version                               int   `json:"version"`
	Generation                            int64 `json:"generation"`
	EpisodeActive                         bool  `json:"episode_active"`
	N0, B0, J0                            int64
	ProgressID, ResponseID, BusinessOwner string
	ResourceID, RoundIndex                int
}

func (a *RequestAdmission) RestoreRuntimeState(state PipelineRuntimeState) error {
	if state.Version != 1 || state.Generation < 0 || state.N0 < 0 || state.B0 < 0 || state.J0 < 0 || len(state.ProgressID) > 128 || len(state.ResponseID) > 128 || len(state.BusinessOwner) > 128 {
		return ErrPipelineCapacity
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.budget == nil {
		return ErrPipelineCapacity
	}
	if state.Generation < a.budget.state.Generation {
		return ErrPipelineCapacity
	}
	if state.BusinessOwner == "" && a.budget.businessOwner != "" {
		if _, pending := a.budget.requests[a.budget.businessOwner]; pending {
			state.BusinessOwner = a.budget.businessOwner
		}
	}
	a.budget.state = state
	a.budget.progressID = state.ProgressID
	a.budget.businessOwner = state.BusinessOwner
	for id, e := range a.budget.entries {
		e.Progress = id == state.ProgressID
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

func (a *RequestAdmission) AttachProgress(state PipelineRuntimeState) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	if b == nil || state.Version != 1 || state.ProgressID == "" || b.progressID != "" && b.progressID != state.ProgressID {
		return ErrPipelineCapacity
	}
	b.state = state
	b.progressID = state.ProgressID
	b.businessOwner = state.BusinessOwner
	if e, ok := b.entries[state.ProgressID]; ok {
		e.Progress = true
		b.entries[state.ProgressID] = e
	}
	a.notifyLocked()
	return nil
}

func (a *RequestAdmission) MemoryProgress(id, response string, resource, round int) (PipelineRuntimeState, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	if b == nil || id == "" || b.progressID != "" && b.progressID != id {
		return PipelineRuntimeState{}, ErrPipelineCapacity
	}
	s := b.state
	if s.Version == 0 {
		s.Version = 1
	}
	if err := b.captureMemoryRecovery(id, response, &s); err != nil {
		return PipelineRuntimeState{}, err
	}
	s.ProgressID, s.ResponseID, s.ResourceID, s.RoundIndex = id, response, resource, round
	return s, nil
}

func (b *pipelineBudget) progressCandidateLimit() int64 {
	limit := max(b.limits.Candidates, b.limits.Item)
	if b.state.EpisodeActive {
		limit = max(limit, b.state.B0+b.limits.Item)
	}
	return limit
}
func (b *pipelineBudget) progressMemberLimit() int64 {
	limit := int64(b.limits.Segments)
	if b.state.EpisodeActive {
		limit = max(limit, b.state.N0+1)
	}
	return limit
}

func (a *RequestAdmission) RefreshMemoryEpisode() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	if b == nil {
		return ErrPipelineCapacity
	}
	s := b.state
	s.ProgressID = b.progressID
	s.BusinessOwner = b.businessOwner
	if s.ResponseID != "" {
		if _, pending := b.requests[s.ResponseID]; !pending {
			s.ResponseID = ""
		}
	}
	if s.EpisodeActive && s.ProgressID == "" && s.ResponseID == "" && s.BusinessOwner == "" {
		n, bytes, growth := b.candidateTotals()
		if n <= b.limits.Segments-1 && bytes+growth <= max(int64(0), b.limits.Candidates-b.limits.Item) && b.requestTotal() <= b.limits.Handoff-b.lane {
			s.EpisodeActive = false
			s.N0, s.B0, s.J0 = 0, 0, 0
		}
	}
	b.state = s
	return nil
}

func (a *RequestAdmission) MoveProgress(previous string, state PipelineRuntimeState) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	if b == nil || state.Version != 1 || state.ProgressID == "" || b.progressID != "" && b.progressID != previous && b.progressID != state.ProgressID {
		return ErrPipelineCapacity
	}
	for id, e := range b.entries {
		e.Progress = id == state.ProgressID
		b.entries[id] = e
	}
	for id, r := range b.requests {
		r.progress = id == state.ProgressID
		b.requests[id] = r
	}
	b.state = state
	b.progressID = state.ProgressID
	b.businessOwner = state.BusinessOwner
	a.notifyLocked()
	return nil
}
func (a *RequestAdmission) MemoryMoveProgress(previous, business, id, response string, resource, round int) (PipelineRuntimeState, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	if b == nil || b.progressID != "" && b.progressID != previous {
		return PipelineRuntimeState{}, ErrPipelineCapacity
	}
	s := b.state
	if err := b.captureMemoryRecovery(id, response, &s); err != nil {
		return PipelineRuntimeState{}, err
	}
	s.ProgressID, s.ResponseID, s.ResourceID, s.RoundIndex = id, response, resource, round
	if business != "" {
		if b.businessOwner != business {
			return PipelineRuntimeState{}, ErrProgressRequired
		}
		s.BusinessOwner = ""
	}
	return s, nil
}

func (a *RequestAdmission) PersistedRuntimeState() PipelineRuntimeState {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.budget == nil {
		return PipelineRuntimeState{Version: 1}
	}
	return a.budget.state
}

func (b *pipelineBudget) captureMemoryRecovery(id, response string, s *PipelineRuntimeState) error {
	if s.EpisodeActive {
		return nil
	}
	n, bytes, j := int64(0), int64(0), int64(0)
	for _, e := range b.entries {
		if e.Durable {
			var err error
			n, err = checkedSum(n, int64(e.Count))
			if err != nil {
				return err
			}
			bytes, err = checkedSum(bytes, e.Bytes, e.Growth)
			if err != nil {
				return err
			}
		}
	}
	for _, r := range b.requests {
		if r.granted {
			var err error
			j, err = checkedSum(j, r.known, r.raw, r.corrected, RequestMetadataBytes)
			if err != nil {
				return err
			}
		}
	}
	ordinaryN, ordinaryB, ordinaryJ := n, bytes, j
	if e, ok := b.entries[id]; ok && e.Durable && !e.Reservation {
		ordinaryN = max(int64(0), ordinaryN-int64(e.Count))
		ordinaryB = max(int64(0), ordinaryB-(e.Bytes+e.Growth))
	}
	if s.ProgressID == id && response != "" {
		if r, ok := b.requests[response]; ok && r.granted {
			owned, err := checkedSum(r.known, r.raw, r.corrected, RequestMetadataBytes)
			if err != nil {
				return err
			}
			ordinaryN = max(int64(0), ordinaryN-1)
			ordinaryJ = max(int64(0), ordinaryJ-owned)
		}
	}
	if ordinaryN <= int64(max(0, b.limits.Segments-1)) && ordinaryB <= max(int64(0), b.limits.Candidates-b.limits.Item) && ordinaryJ <= b.limits.Handoff-b.limits.Item-b.limits.Responses-RequestMetadataBytes {
		return nil
	}
	if s.Generation == int64(^uint64(0)>>1) {
		return ErrPipelineCapacity
	}
	s.EpisodeActive = true
	s.Generation++
	s.N0, s.B0, s.J0 = n, bytes, j
	return nil
}
