package backend

// OwnedObserver records actual payload backing lengths/capacities. It is
// diagnostic data on the existing Job ledger, never a grant or another budget.
type OwnedObserver interface {
	ObserveOwned(category, key string, bytes int64)
}
type OwnedPayloadSnapshot struct {
	Current, Peak    map[string]int64
	Total, TotalPeak int64
}
type ownedPayloadState struct {
	objects          map[string]int64
	categories       map[string]string
	current, peak    map[string]int64
	total, totalPeak int64
}

func (a *RequestAdmission) ObserveOwned(category, key string, bytes int64) {
	if bytes < 0 || len(key) > 256 {
		return
	}
	switch category {
	case "freeze", "raw", "wire", "candidate", "aux", "attachment", "codec", "db_copy":
	default:
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.budget == nil {
		return
	}
	s := &a.budget.owned
	if s.objects == nil {
		s.objects = map[string]int64{}
		s.categories = map[string]string{}
		s.current = map[string]int64{}
		s.peak = map[string]int64{}
	}
	object := category + ":" + key
	old := s.objects[object]
	s.current[category] -= old
	s.total -= old
	if bytes == 0 {
		delete(s.objects, object)
		delete(s.categories, object)
	} else {
		s.objects[object] = bytes
		s.categories[object] = category
		s.current[category] += bytes
		s.total += bytes
	}
	s.peak[category] = max(s.peak[category], s.current[category])
	s.totalPeak = max(s.totalPeak, s.total)
}
func (a *RequestAdmission) OwnedPayloadSnapshot() OwnedPayloadSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ownedPayloadSnapshotLocked()
}
func (a *RequestAdmission) ownedPayloadSnapshotLocked() OwnedPayloadSnapshot {
	out := OwnedPayloadSnapshot{Current: map[string]int64{}, Peak: map[string]int64{}}
	if a.budget == nil {
		return out
	}
	s := a.budget.owned
	for k, v := range s.current {
		out.Current[k] = v
	}
	for k, v := range s.peak {
		out.Peak[k] = v
	}
	out.Total, out.TotalPeak = s.total, s.totalPeak
	return out
}
