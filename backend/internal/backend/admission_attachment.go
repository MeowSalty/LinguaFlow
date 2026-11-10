package backend

// PrepareInputAttachment is an early K claim in the existing Job ledger. It
// does not grant an HTTP/response owner; PrepareMain replaces its placeholder.
func (a *RequestAdmission) PrepareInputAttachment(id string, round, resource int, bytes int64) (bool, error) {
	if id == "" || bytes < 0 {
		return false, ErrPipelineCapacity
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	if b == nil {
		return false, ErrPipelineCapacity
	}
	if r, ok := b.requests[id]; ok {
		if r.attachment != bytes || r.round != round || r.resource != resource {
			return false, ErrPipelineCapacity
		}
		return true, nil
	}
	if bytes > b.limits.Handoff-b.lane {
		return false, ErrMainBatchTooLarge
	}
	if bytes > b.limits.Handoff-b.lane-b.requestTotal() {
		return false, nil
	}
	b.requests[id] = requestBudget{round: round, resource: resource, known: bytes, attachment: bytes}
	a.notifyLocked()
	return true, nil
}
func (a *RequestAdmission) ReleaseInputAttachment(id string, bytes int64) error {
	return a.RepriceInputAttachment(id, bytes, 0)
}

// RepriceInputAttachment replaces the exact early metadata/blob credit after
// sealing. It never refunds the freeze/wire portion of a prepared request.
func (a *RequestAdmission) RepriceInputAttachment(id string, oldBytes, newBytes int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	if b == nil {
		return ErrPipelineCapacity
	}
	r, ok := b.requests[id]
	if !ok || r.granted || oldBytes < 0 || newBytes < 0 || r.attachment != oldBytes || oldBytes > r.known {
		return ErrPipelineCapacity
	}
	if newBytes > oldBytes && newBytes-oldBytes > b.limits.Handoff-b.lane-b.requestTotal() {
		return ErrPipelineCapacity
	}
	r.known = r.known - oldBytes + newBytes
	if r.freeze > 0 || r.wire > 0 {
		if oldBytes > r.wire {
			return ErrPipelineCapacity
		}
		r.wire = r.wire - oldBytes + newBytes
	}
	r.attachment = newBytes
	b.requests[id] = r
	a.notifyLocked()
	return nil
}
