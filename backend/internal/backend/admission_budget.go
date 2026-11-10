package backend

import (
	"errors"
	"fmt"
	"math"
)

const RequestMetadataBytes int64 = 32 << 10

var ErrPipelineCapacity = errors.New("pipeline capacity conflict")
var ErrMainBatchTooLarge = errors.New("main batch cannot fit response handoff")

func (a *RequestAdmission) capacityLocked(intent RequestAdmissionIntent, exact bool) (int64, string, error) {
	main, aux := a.mainResults[intent.RoundIndex], a.auxResults
	if a.budget != nil {
		main, aux = a.budget.mainOwners[intent.RoundIndex], a.budget.auxOwners
	}
	if intent.Stage == RequestStageMain && main >= a.config.MainConcurrency[intent.RoundIndex] {
		return 0, "main_concurrency", nil
	}
	if intent.Stage == RequestStageAlignment && aux >= a.config.AlignmentConcurrency {
		return 0, "alignment_concurrency", nil
	}
	if a.budget == nil || !exact && intent.RequestID == "" {
		return 0, "", nil
	}
	if intent.Stage == RequestStageMain {
		return a.budget.canGrantMain(intent)
	}
	reason, err := a.budget.canGrantAux(intent)
	return 0, reason, err
}

type PipelineBudgetLimits struct {
	Segments                             int
	Candidates, Item, Responses, Handoff int64
}
type PipelineBudgetEntry struct {
	ResourceID, RoundID, Count     int
	Bytes, Growth                  int64
	Reservation, Durable, Progress bool
	GrowthDurable                  bool
	Replaced                       []string
}
type PipelineBudgetSnapshot struct {
	OwnedPayloads                                                                             OwnedPayloadSnapshot
	RequestedMain, EffectiveMain                                                              map[int]int
	MainCapacityReason                                                                        map[int]string
	RequestedAux, EffectiveAux                                                                int
	AuxCapacityReason                                                                         string
	Episode                                                                                   PipelineRuntimeState
	MainOwnerPeak, AuxOwnerPeak                                                               int
	Segments                                                                                  int
	CandidateBytes, GrowthBytes, HandoffBytes, PreparedBytes, AuxInputBytes, AuxResponseBytes int64
	ProgressID                                                                                string
	ProgressLane                                                                              int64
	MainOwners                                                                                map[int]int
	AuxOwners                                                                                 int
	Revision                                                                                  uint64
}
type requestBudget struct {
	round, resource                    int
	known, freeze, wire, raw, business int64
	granted, progress                  bool
	pinned                             bool
	corrected                          int64
	attachment                         int64
}
type pipelineBudget struct {
	owned                       ownedPayloadState
	mainOwnerPeak, auxOwnerPeak int
	limits                      PipelineBudgetLimits
	entries                     map[string]PipelineBudgetEntry
	requests                    map[string]requestBudget
	mainOwners                  map[int]int
	auxOwners                   int
	auxInput, auxResponse       int64
	progressID                  string
	lane                        int64
	businessOwner               string
	state                       PipelineRuntimeState
}

func checkedSum(values ...int64) (int64, error) {
	var total int64
	for _, v := range values {
		if v < 0 || v > math.MaxInt64-total {
			return 0, ErrPipelineCapacity
		}
		total += v
	}
	return total, nil
}

func (a *RequestAdmission) ConfigurePipelineBudget(l PipelineBudgetLimits) error {
	if l.Segments < 1 || l.Candidates < 1 || l.Item < 1 || l.Responses < 1 || l.Handoff < 1 {
		return ErrPipelineCapacity
	}
	lane, err := checkedSum(l.Item, l.Responses, RequestMetadataBytes)
	if err != nil || lane > l.Handoff {
		return fmt.Errorf("%w: response handoff cannot fit its progress lane", ErrPipelineCapacity)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.budget != nil {
		return fmt.Errorf("pipeline budget already configured")
	}
	a.budget = &pipelineBudget{limits: l, entries: map[string]PipelineBudgetEntry{}, requests: map[string]requestBudget{}, mainOwners: map[int]int{}, lane: lane}
	return nil
}

func (a *RequestAdmission) SetResponseLimits(response, handoff int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	if b == nil || len(b.entries) > 0 || len(b.requests) > 0 {
		return ErrPipelineCapacity
	}
	lane, err := checkedSum(b.limits.Item, response, RequestMetadataBytes)
	if err != nil || response < 1 || handoff < lane {
		return ErrPipelineCapacity
	}
	b.limits.Responses, b.limits.Handoff, b.lane = response, handoff, lane
	return nil
}

func (a *RequestAdmission) StageCapacity(stage RequestStage, round int) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if stage == RequestStageMain {
		return a.config.MainConcurrency[round]
	}
	return a.config.AlignmentConcurrency
}

func (a *RequestAdmission) ReserveCandidateGrowth(id string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	e, ok := b.entries[id]
	if !ok || e.Count != 1 || e.Reservation {
		return false, ErrPipelineCapacity
	}
	if e.Growth == b.limits.Item-e.Bytes {
		return true, nil
	}
	ordinary := int64(0)
	for _, v := range b.entries {
		if !v.Progress {
			ordinary += v.Bytes + v.Growth
		}
	}
	delta := b.limits.Item - e.Bytes - e.Growth
	if e.Progress || delta <= max(int64(0), b.limits.Candidates-b.limits.Item)-ordinary {
		e.Growth = b.limits.Item - e.Bytes
		b.entries[id] = e
		a.notifyLocked()
		return true, nil
	}
	if b.progressID != "" && b.progressID != id {
		return false, nil
	}
	if b.progressID == "" {
		return false, ErrProgressRequired
	}
	e.Progress = true
	e.Growth = b.limits.Item - e.Bytes
	b.progressID = id
	b.entries[id] = e
	a.notifyLocked()
	return true, nil
}

func (b *pipelineBudget) candidateTotals() (count int, bytes, growth int64) {
	for _, e := range b.entries {
		count += e.Count
		bytes += e.Bytes
		growth += e.Growth
	}
	return
}
func (b *pipelineBudget) requestTotal() int64 {
	var total int64
	for _, r := range b.requests {
		if !r.progress {
			total += r.known
			if r.granted {
				total += r.raw + RequestMetadataBytes
				if r.pinned {
					total += r.corrected
				}
			}
		}
	}
	return total
}
func (b *pipelineBudget) businessMaximum(extra int64) int64 {
	for _, r := range b.requests {
		if r.granted && !r.pinned && r.business > extra {
			extra = r.business
		}
	}
	return extra
}

func (a *RequestAdmission) PipelineSnapshot() PipelineBudgetSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	if b == nil {
		return PipelineBudgetSnapshot{}
	}
	count, bytes, growth := b.candidateTotals()
	out := PipelineBudgetSnapshot{Segments: count, CandidateBytes: bytes, GrowthBytes: growth, HandoffBytes: b.requestTotal() + b.lane, AuxInputBytes: b.auxInput, AuxResponseBytes: b.auxResponse, ProgressID: b.progressID, ProgressLane: b.lane, MainOwners: map[int]int{}, AuxOwners: b.auxOwners, Revision: a.version}
	for _, r := range b.requests {
		if !r.granted {
			out.PreparedBytes += r.known
		}
	}
	for round, n := range b.mainOwners {
		out.MainOwners[round] = n
	}
	out.Episode = b.state
	out.MainOwnerPeak = b.mainOwnerPeak
	out.AuxOwnerPeak = b.auxOwnerPeak
	out.OwnedPayloads = a.ownedPayloadSnapshotLocked()
	out.RequestedMain = map[int]int{}
	out.EffectiveMain = map[int]int{}
	out.MainCapacityReason = map[int]string{}
	known := map[int]int64{}
	pinned := false
	for _, request := range b.requests {
		if request.pinned {
			pinned = true
		}
		u, err := checkedSum(request.known, b.limits.Responses, RequestMetadataBytes)
		if err == nil && (known[request.round] == 0 || u < known[request.round]) {
			known[request.round] = u
		}
	}
	remaining := max(int64(0), b.limits.Handoff-b.lane-b.requestTotal())
	for round, u := range known {
		requested := a.config.MainConcurrency[round]
		effective := b.mainOwners[round]
		reason := "response_handoff"
		if pinned {
			reason = "future_pins"
		} else if u > 0 {
			available := remaining / u
			effective += int(min(int64(max(0, requested-effective)), available))
		}
		if effective >= requested {
			effective = requested
			reason = "configured_concurrency"
		}
		out.RequestedMain[round] = requested
		out.EffectiveMain[round] = effective
		out.MainCapacityReason[round] = reason
	}
	out.RequestedAux = a.config.AlignmentConcurrency
	ordinary := int64(0)
	for _, entry := range b.entries {
		if !entry.Progress {
			ordinary += entry.Bytes + entry.Growth
		}
	}
	free := max(int64(0), b.limits.Candidates-b.limits.Item-ordinary)
	out.EffectiveAux = b.auxOwners + int(min(int64(max(0, out.RequestedAux-b.auxOwners)), free/b.limits.Item))
	out.AuxCapacityReason = "candidate_growth"
	if out.EffectiveAux >= out.RequestedAux {
		out.EffectiveAux = out.RequestedAux
		out.AuxCapacityReason = "configured_concurrency"
	}
	return out
}

func (a *RequestAdmission) BudgetEntries() map[string]PipelineBudgetEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := map[string]PipelineBudgetEntry{}
	if a.budget != nil {
		for k, v := range a.budget.entries {
			out[k] = v
		}
	}
	return out
}

func (a *RequestAdmission) ReserveMembers(key string, count, resource, round int, progress bool) (bool, error) {
	return a.ReserveMembersReplacing(key, count, resource, round, progress, nil)
}
func (a *RequestAdmission) ReserveMembersReplacing(key string, count, resource, round int, progress bool, replacements []string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	if b == nil {
		return false, ErrPipelineCapacity
	}
	if old, ok := b.entries[key]; ok {
		if old.Reservation && old.Count == count {
			return true, nil
		}
		return false, ErrPipelineCapacity
	}
	if key == "" || count < 1 || count > b.limits.Segments {
		return false, ErrPipelineCapacity
	}
	if len(replacements) > count {
		return false, ErrPipelineCapacity
	}
	replaced := make([]string, 0, len(replacements))
	seen := map[string]bool{}
	for _, id := range replacements {
		if seen[id] {
			return false, ErrPipelineCapacity
		}
		seen[id] = true
		if e, ok := b.entries[id]; ok {
			if !e.Reservation || !e.Durable || e.Count != 1 || e.ResourceID != resource || e.RoundID != round {
				return false, ErrPipelineCapacity
			}
			replaced = append(replaced, id)
		}
	}
	n, _, _ := b.candidateTotals()
	n -= len(replaced)
	limit := b.limits.Segments - 1
	if progress {
		if count != 1 || b.progressID != "" && b.progressID != key && !seen[b.progressID] {
			return false, nil
		}
		limit = int(b.progressMemberLimit())
	}
	if count > limit-n {
		return false, nil
	}
	for _, id := range replaced {
		delete(b.entries, id)
	}
	b.entries[key] = PipelineBudgetEntry{ResourceID: resource, RoundID: round, Count: count, Reservation: true, Progress: progress, Replaced: replaced}
	if progress {
		b.progressID = key
	}
	a.notifyLocked()
	return true, nil
}

func (a *RequestAdmission) RenameMembers(from, to string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	e, ok := b.entries[from]
	if !ok || !e.Reservation {
		return ErrPipelineCapacity
	}
	if from == to {
		e.Durable = true
		e.Replaced = nil
		b.entries[to] = e
		a.notifyLocked()
		return nil
	}
	if _, exists := b.entries[to]; exists {
		return ErrPipelineCapacity
	}
	delete(b.entries, from)
	e.Durable = true
	e.Replaced = nil
	b.entries[to] = e
	if b.progressID == from {
		b.progressID = to
	}
	a.notifyLocked()
	return nil
}

func (a *RequestAdmission) RestoreBudgetEntry(key string, e PipelineBudgetEntry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	if b == nil || key == "" || e.Count < 1 || e.Bytes < 0 || e.Bytes > b.limits.Item && !e.Reservation {
		return ErrPipelineCapacity
	}
	if old, ok := b.entries[key]; ok {
		if old.Count != e.Count || old.Bytes != e.Bytes {
			return ErrPipelineCapacity
		}
		return nil
	}
	if b.progressID == key {
		e.Progress = true
	}
	b.entries[key] = e
	if e.Progress {
		if b.progressID != "" && b.progressID != key {
			return ErrPipelineCapacity
		}
		b.progressID = key
	}
	a.notifyLocked()
	return nil
}

// BeginPromotion reserves actual candidate bytes while the member still belongs
// to raw. ApplyPromotion transfers its S credit only after the transaction ACK.
func (a *RequestAdmission) BeginPromotion(from, to string, size int64, progress bool) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	e, ok := b.entries[from]
	if !ok || !e.Reservation || e.Count < 1 || size < 0 || size > b.limits.Item {
		return false, ErrPipelineCapacity
	}
	if pending, ok := b.entries[to]; ok {
		return pending.Count == 0 && pending.Bytes == size, nil
	}
	_, bytes, growth := b.candidateTotals()
	ordinary := int64(0)
	for _, entry := range b.entries {
		if !entry.Progress {
			ordinary += entry.Bytes + entry.Growth
		}
	}
	if !progress && size > max(int64(0), b.limits.Candidates-b.limits.Item)-ordinary {
		if b.progressID != "" && b.progressID != from && b.progressID != to {
			return false, nil
		}
		if b.progressID == "" {
			return false, ErrProgressRequired
		}
		progress = true
	}
	if progress {
		if b.progressID != "" && b.progressID != from && b.progressID != to {
			return false, nil
		}
		if size > b.progressCandidateLimit()-bytes-growth {
			return false, nil
		}
		b.progressID = to
	}
	b.entries[to] = PipelineBudgetEntry{ResourceID: e.ResourceID, RoundID: e.RoundID, Bytes: size, Progress: progress}
	a.notifyLocked()
	return true, nil
}

func (a *RequestAdmission) ApplyPromotion(from, to string, expected ...int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	raw, ok := b.entries[from]
	candidate, exists := b.entries[to]
	if !ok || !exists || !raw.Reservation || raw.Count < 1 || candidate.Count != 0 {
		return ErrPipelineCapacity
	}
	if len(expected) > 0 && (expected[0] < 0 || expected[0] > b.limits.Item || expected[0] != candidate.Bytes) {
		return ErrPipelineCapacity
	}
	raw.Count--
	if raw.Count == 0 {
		delete(b.entries, from)
	} else {
		b.entries[from] = raw
	}
	candidate.Count = 1
	candidate.Durable = true
	b.entries[to] = candidate
	a.notifyLocked()
	return nil
}
func (a *RequestAdmission) AbortPromotion(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.budget != nil {
		if e, ok := a.budget.entries[id]; ok && e.Count == 0 {
			delete(a.budget.entries, id)
			if a.budget.progressID == id {
				a.budget.progressID = ""
			}
			a.notifyLocked()
		}
	}
}
func (a *RequestAdmission) DropBudgetMember(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	e, ok := b.entries[id]
	if !ok || !e.Reservation || e.Count < 1 {
		return ErrPipelineCapacity
	}
	e.Count--
	if e.Count == 0 {
		delete(b.entries, id)
	} else {
		b.entries[id] = e
	}
	if e.Count == 0 && b.progressID == id {
		b.progressID = ""
	}
	a.notifyLocked()
	return nil
}
func (a *RequestAdmission) ReleaseBudgetEntry(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.budget == nil {
		return
	}
	if e, ok := a.budget.entries[id]; ok && e.Reservation && !e.Durable {
		for _, old := range e.Replaced {
			a.budget.entries[old] = PipelineBudgetEntry{ResourceID: e.ResourceID, RoundID: e.RoundID, Count: 1, Reservation: true, Durable: true}
		}
	}
	delete(a.budget.entries, id)
	if a.budget.progressID == id {
		a.budget.progressID = ""
	}
	a.notifyLocked()
}

func (a *RequestAdmission) ResizeBudgetEntry(id string, size int64, shrink bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	e, ok := b.entries[id]
	if !ok || size < 0 || size > b.limits.Item {
		return ErrPipelineCapacity
	}
	if size > e.Bytes+e.Growth {
		return ErrPipelineCapacity
	}
	old := e.Bytes
	e.Bytes = size
	if e.Growth > 0 {
		e.Growth = max(int64(0), b.limits.Item-size)
	}
	if shrink && size < old && e.Growth == 0 {
		e.Growth = 0
	}
	b.entries[id] = e
	a.notifyLocked()
	return nil
}

// PrepareMain stores only known K. A cooled backend owns neither response nor
// network capacity until the same admission grant succeeds.
func (a *RequestAdmission) PrepareMain(id string, round, resource int, freeze, wire, business int64, progress bool) (bool, error) {
	known, err := checkedSum(freeze, wire)
	if err != nil {
		return false, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	if b == nil {
		return false, ErrPipelineCapacity
	}
	old, exists := b.requests[id]
	attachment := int64(0)
	if exists && old.attachment > 0 {
		attachment = old.attachment
		known, err = checkedSum(known, attachment)
		if err != nil {
			return false, err
		}
	}
	if exists && !(old.attachment > 0 && old.freeze == 0 && old.wire == 0 && !old.granted) {
		if old.known != known || old.round != round {
			return false, ErrPipelineCapacity
		}
		return true, nil
	}
	ownLane, e := checkedSum(b.limits.Item, b.limits.Responses, RequestMetadataBytes, business)
	if e != nil {
		return false, e
	}
	if ownLane > b.limits.Handoff {
		return false, ErrMainBatchTooLarge
	}
	alone, e := checkedSum(known, b.limits.Responses, RequestMetadataBytes, ownLane)
	if e != nil {
		return false, e
	}
	if !progress && alone > b.limits.Handoff {
		return false, ErrMainBatchTooLarge
	}
	// Future saved business can make the previous lane exceed a lowered J.
	// Its live inventory is classified by the one progress owner, not by
	// rejecting a finite fresh singleton before that owner can be attached.
	if !progress && known > b.limits.Handoff-b.lane {
		return false, nil
	}
	previous := int64(0)
	if attachment > 0 {
		previous = old.known
	}
	if !progress && known > b.limits.Handoff-b.lane-(b.requestTotal()-previous) {
		return false, nil
	}
	if progress && known > b.limits.Item {
		return false, ErrPipelineCapacity
	}
	b.requests[id] = requestBudget{round: round, resource: resource, known: known, freeze: freeze, wire: wire + attachment, business: business, progress: progress, attachment: attachment}
	a.notifyLocked()
	return true, nil
}

func (b *pipelineBudget) canGrantMain(intent RequestAdmissionIntent) (int64, string, error) {
	r, ok := b.requests[intent.RequestID]
	if !ok {
		return 0, "", ErrPipelineCapacity
	}
	if r.granted {
		return 0, "", ErrPipelineCapacity
	}
	lane, err := checkedSum(b.limits.Item, b.limits.Responses, RequestMetadataBytes, b.businessMaximum(r.business))
	if err != nil || lane > b.limits.Handoff {
		return 0, "", ErrPipelineCapacity
	}
	if r.progress {
		total := int64(0)
		for _, owned := range b.requests {
			credit, e := checkedSum(owned.known)
			if e != nil {
				return 0, "", e
			}
			if owned.pinned {
				credit, e = checkedSum(credit, owned.corrected)
				if e != nil {
					return 0, "", e
				}
			}
			if owned.granted {
				credit, e = checkedSum(credit, owned.raw, RequestMetadataBytes)
				if e != nil {
					return 0, "", e
				}
			}
			total, e = checkedSum(total, credit)
			if e != nil {
				return 0, "", e
			}
		}
		required, e := checkedSum(total, b.limits.Responses, RequestMetadataBytes)
		if e != nil {
			return 0, "", e
		}
		ceiling, e := checkedSum(max(b.limits.Handoff, b.state.J0), lane)
		if e != nil {
			return 0, "", e
		}
		if required > ceiling {
			return 0, "response_handoff", nil
		}
	}
	if !r.progress {
		for _, owned := range b.requests {
			if owned.pinned {
				return 0, "pinned_response_inventory", nil
			}
		}
		required, err := checkedSum(b.requestTotal(), b.limits.Responses, RequestMetadataBytes, lane)
		if err != nil {
			return 0, "", err
		}
		if required > b.limits.Handoff {
			return 0, "response_handoff", nil
		}
	}
	return lane, "", nil
}

func (b *pipelineBudget) canGrantAux(intent RequestAdmissionIntent) (string, error) {
	if intent.InputBytes < 0 || intent.InputBytes > max(b.limits.Candidates, b.limits.Item) {
		return "", ErrPipelineCapacity
	}
	if intent.InputBytes > max(b.limits.Candidates, b.limits.Item)-b.auxInput {
		return "aux_input", nil
	}
	ordinary := int64(0)
	for _, e := range b.entries {
		if !e.Progress {
			ordinary += e.Bytes + e.Growth
		}
	}
	additional := int64(0)
	seen := map[string]bool{}
	for _, id := range intent.CandidateIDs {
		if seen[id] {
			return "", ErrPipelineCapacity
		}
		seen[id] = true
		e, ok := b.entries[id]
		if !ok || e.Count != 1 || e.Reservation {
			return "", ErrPipelineCapacity
		}
		if e.Progress {
			if len(intent.CandidateIDs) != 1 {
				return "", ErrPipelineCapacity
			}
			continue
		}
		additional += b.limits.Item - e.Bytes - e.Growth
	}
	if additional > max(int64(0), b.limits.Candidates-b.limits.Item)-ordinary {
		if len(intent.CandidateIDs) == 1 && b.progressID == intent.CandidateIDs[0] {
			return "", nil
		}
		if len(intent.CandidateIDs) == 1 && b.progressID == "" {
			return "", ErrProgressRequired
		}
		return "candidate_growth", nil
	}
	return "", nil
}

func (b *pipelineBudget) applyGrant(intent RequestAdmissionIntent, lane int64) {
	if intent.Stage == RequestStageMain {
		r := b.requests[intent.RequestID]
		r.granted = true
		r.raw = b.limits.Responses
		b.requests[intent.RequestID] = r
		b.mainOwners[intent.RoundIndex]++
		total := 0
		for _, n := range b.mainOwners {
			total += n
		}
		b.mainOwnerPeak = max(b.mainOwnerPeak, total)
		b.lane = lane
	} else {
		b.auxOwners++
		b.auxOwnerPeak = max(b.auxOwnerPeak, b.auxOwners)
		b.auxInput += intent.InputBytes
		b.auxResponse += b.limits.Responses
		ordinary := int64(0)
		for _, e := range b.entries {
			if !e.Progress {
				ordinary += e.Bytes + e.Growth
			}
		}
		additional := int64(0)
		for _, id := range intent.CandidateIDs {
			e := b.entries[id]
			if !e.Progress {
				additional += b.limits.Item - e.Bytes - e.Growth
			}
		}
		borrow := additional > max(int64(0), b.limits.Candidates-b.limits.Item)-ordinary
		for _, id := range intent.CandidateIDs {
			e := b.entries[id]
			e.Growth = b.limits.Item - e.Bytes
			e.GrowthDurable = true
			if borrow {
				e.Progress = true
				b.progressID = id
			}
			b.entries[id] = e
		}
	}
}

func (a *RequestAdmission) ApplyResponseHandoff(id string, raw int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	r, ok := b.requests[id]
	if !ok || !r.granted || raw < 0 || raw > b.limits.Responses {
		return ErrPipelineCapacity
	}
	r.known = r.freeze
	r.wire = 0
	r.raw = raw
	b.requests[id] = r
	a.notifyLocked()
	return nil
}
func (a *RequestAdmission) ReleasePrepared(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.budget != nil {
		if r, ok := a.budget.requests[id]; ok && !r.granted {
			delete(a.budget.requests, id)
			a.notifyLocked()
		}
	}
}

// ApplyFinalResultCredit replaces a joined local construction allowance with
// the actual opaque commit buffer. Durable aux/progress Q remains reserved.
func (a *RequestAdmission) ApplyFinalResultCredit(id string, size int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	if b == nil {
		return ErrPipelineCapacity
	}
	e, ok := b.entries[id]
	if !ok || e.Reservation || e.Count != 1 || size < 0 || size > b.limits.Item {
		return ErrPipelineCapacity
	}
	if e.Progress || e.GrowthDurable || b.state.ProgressID == id {
		return nil
	}
	growth := max(int64(0), size-e.Bytes)
	if growth > e.Growth {
		return ErrPipelineCapacity
	}
	if growth != e.Growth {
		e.Growth = growth
		b.entries[id] = e
		a.notifyLocked()
	}
	return nil
}

func (a *RequestAdmission) RestoreResponseOwner(id string, round, resource int, freeze, raw, business int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	if b == nil || id == "" || freeze < 0 || raw < 0 || raw > b.limits.Responses || business < 0 {
		return ErrPipelineCapacity
	}
	if _, ok := a.config.MainConcurrency[round]; !ok {
		return ErrPipelineCapacity
	}
	if old, ok := b.requests[id]; ok {
		if old.freeze != freeze || old.raw != raw || old.business != business {
			return ErrPipelineCapacity
		}
		return nil
	}
	lane, err := checkedSum(b.limits.Item, b.limits.Responses, RequestMetadataBytes, b.businessMaximum(business))
	if err != nil || freeze > b.limits.Handoff {
		return ErrPipelineCapacity
	}
	b.requests[id] = requestBudget{round: round, resource: resource, known: freeze, freeze: freeze, raw: raw, business: business, granted: true}
	b.mainOwners[round]++
	b.lane = lane
	a.notifyLocked()
	return nil
}
func (a *RequestAdmission) ReleaseDurableResponse(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.budget == nil {
		return
	}
	r, ok := a.budget.requests[id]
	if !ok {
		return
	}
	a.releaseRequestBudgetLocked(RequestAdmissionIntent{RequestID: id, RoundIndex: r.round, Stage: RequestStageMain})
	a.notifyLocked()
}

func (a *RequestAdmission) TryBusinessOwner(id string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	if b == nil {
		return false, ErrPipelineCapacity
	}
	r, ok := b.requests[id]
	if !ok || !r.granted {
		return false, ErrPipelineCapacity
	}
	if b.businessOwner != "" && b.businessOwner != id {
		return false, nil
	}
	b.businessOwner = id
	return true, nil
}
func (a *RequestAdmission) releaseRequestBudgetLocked(intent RequestAdmissionIntent) {
	b := a.budget
	if b == nil {
		return
	}
	if intent.Stage == RequestStageMain {
		if r, ok := b.requests[intent.RequestID]; ok {
			if r.granted {
				b.mainOwners[r.round]--
			}
			delete(b.requests, intent.RequestID)
		}
		if b.businessOwner == intent.RequestID {
			b.businessOwner = ""
		}
		lane, _ := checkedSum(b.limits.Item, b.limits.Responses, RequestMetadataBytes, b.businessMaximum(0))
		b.lane = lane
	} else {
		b.auxOwners--
		b.auxInput -= intent.InputBytes
		b.auxResponse -= b.limits.Responses
	}
}

// ActiveMainCapacity counts each active round once, including its pending raw
// owners. A resource in an already active round cannot increase the denominator.
func (a *RequestAdmission) ActiveMainCapacity(extraRound int, activeRounds []int) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	rounds := map[int]bool{extraRound: true}
	for _, round := range activeRounds {
		rounds[round] = true
	}
	if a.budget != nil {
		for round, count := range a.budget.mainOwners {
			if count > 0 {
				rounds[round] = true
			}
		}
	}
	sum := int64(0)
	for round := range rounds {
		v, ok := a.config.MainConcurrency[round]
		if !ok || v < 1 || int64(v) > math.MaxInt64-sum {
			return 0, ErrPipelineCapacity
		}
		sum += int64(v)
	}
	if sum > int64(math.MaxInt) {
		return 0, ErrPipelineCapacity
	}
	return int(sum), nil
}

func (a *RequestAdmission) RestoreGrowth(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	if b == nil {
		return ErrPipelineCapacity
	}
	e, ok := b.entries[id]
	if !ok || e.Reservation || e.Count != 1 || e.Bytes < 0 || e.Bytes > b.limits.Item {
		return ErrPipelineCapacity
	}
	e.Growth = b.limits.Item - e.Bytes
	e.GrowthDurable = true
	b.entries[id] = e
	a.notifyLocked()
	return nil
}

// ReleaseLocalGrowth follows the scope join: no builder can still use its
// temporary local-finalization credit. A dispatched aux member retains Q.
func (a *RequestAdmission) ReleaseLocalGrowth(ids []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.budget == nil {
		return
	}
	changed := false
	for _, id := range ids {
		e, ok := a.budget.entries[id]
		if ok && !e.Progress && a.budget.state.ProgressID != id && !e.GrowthDurable && e.Growth > 0 {
			e.Growth = 0
			a.budget.entries[id] = e
			changed = true
		}
	}
	if changed {
		a.notifyLocked()
	}
}

func (a *RequestAdmission) ResponseOwnerIDs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	if a.budget != nil {
		for id, r := range a.budget.requests {
			if r.granted {
				out = append(out, id)
			}
		}
	}
	return out
}

// PinResponses follows an exhaustive header/consumer proof of inactive future
// inventory. Its saved business stays owned, outside the current exclusive lane.
func (a *RequestAdmission) PinResponses(ids []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	if b == nil {
		return ErrPipelineCapacity
	}
	changed := false
	for _, id := range ids {
		r, ok := b.requests[id]
		if ok && r.granted && !r.pinned {
			r.pinned = true
			b.requests[id] = r
			changed = true
		}
	}
	if changed {
		lane, e := checkedSum(b.limits.Item, b.limits.Responses, RequestMetadataBytes, b.businessMaximum(0))
		if e != nil {
			return e
		}
		b.lane = lane
		a.notifyLocked()
	}
	return nil
}
func (a *RequestAdmission) RestoreResponseBusiness(id string, bytes int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	if b == nil || bytes < 0 {
		return ErrPipelineCapacity
	}
	r, ok := b.requests[id]
	if !ok || bytes > r.business {
		return ErrPipelineCapacity
	}
	r.corrected = bytes
	b.requests[id] = r
	a.notifyLocked()
	return nil
}
func (a *RequestAdmission) UnpinResponse(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	if b == nil {
		return ErrPipelineCapacity
	}
	r, ok := b.requests[id]
	if !ok {
		return ErrPipelineCapacity
	}
	if r.pinned {
		r.pinned = false
		b.requests[id] = r
		lane, e := checkedSum(b.limits.Item, b.limits.Responses, RequestMetadataBytes, b.businessMaximum(0))
		if e != nil {
			return e
		}
		b.lane = lane
		a.notifyLocked()
	}
	return nil
}

func (a *RequestAdmission) ShrinkMembers(id string, count int) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	if b == nil {
		return ErrPipelineCapacity
	}
	e, ok := b.entries[id]
	if !ok || !e.Reservation || e.Durable || count < 1 || count > e.Count {
		return ErrPipelineCapacity
	}
	e.Count = count
	b.entries[id] = e
	a.notifyLocked()
	return nil
}

func (a *RequestAdmission) SplitOwnedMembers(from string, ids []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	if b == nil {
		return ErrPipelineCapacity
	}
	e, ok := b.entries[from]
	if !ok || !e.Reservation || len(ids) > e.Count {
		return ErrPipelineCapacity
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			return ErrPipelineCapacity
		}
		seen[id] = true
		if _, exists := b.entries[id]; exists && id != from {
			return ErrPipelineCapacity
		}
	}
	delete(b.entries, from)
	for _, id := range ids {
		b.entries[id] = PipelineBudgetEntry{ResourceID: e.ResourceID, RoundID: e.RoundID, Count: 1, Reservation: true, Durable: true, Progress: e.Progress && len(ids) == 1}
	}
	if b.progressID == from {
		b.progressID = ""
		if len(ids) == 1 {
			b.progressID = ids[0]
		}
	}
	a.notifyLocked()
	return nil
}

func (a *RequestAdmission) RetireToMember(from, to string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	if b == nil || to == "" {
		return ErrPipelineCapacity
	}
	e, ok := b.entries[from]
	if !ok || e.Reservation || e.Count != 1 {
		return ErrPipelineCapacity
	}
	if _, ok := b.entries[to]; ok {
		return ErrPipelineCapacity
	}
	delete(b.entries, from)
	b.entries[to] = PipelineBudgetEntry{ResourceID: e.ResourceID, RoundID: e.RoundID, Count: 1, Reservation: true, Durable: true, Progress: e.Progress}
	if b.progressID == from {
		b.progressID = to
	}
	a.notifyLocked()
	return nil
}

func (a *RequestAdmission) CanTransferBusiness(previous, next string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	if b == nil || b.businessOwner != previous {
		return false, ErrPipelineCapacity
	}
	old, ok := b.requests[previous]
	if !ok || !old.granted {
		return false, ErrPipelineCapacity
	}
	current, ok := b.requests[next]
	if !ok || !current.granted {
		return false, ErrPipelineCapacity
	}
	pinned := old.pinned
	old.pinned = true
	b.requests[previous] = old
	lane, err := checkedSum(b.limits.Item, b.limits.Responses, RequestMetadataBytes, b.businessMaximum(current.business))
	total := b.requestTotal()
	old.pinned = pinned
	b.requests[previous] = old
	if err != nil {
		return false, err
	}
	required, err := checkedSum(total, lane)
	if err != nil {
		return false, err
	}
	if required <= b.limits.Handoff {
		return true, nil
	}
	if current.progress && b.state.EpisodeActive {
		ceiling, e := checkedSum(max(b.limits.Handoff, b.state.J0), lane)
		if e != nil {
			return false, e
		}
		return required <= ceiling, nil
	}
	return false, nil
}
func (a *RequestAdmission) ApplyBusinessTransfer(previous, next string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	if b == nil || b.businessOwner != previous {
		return ErrPipelineCapacity
	}
	old, ok := b.requests[previous]
	if !ok {
		return ErrPipelineCapacity
	}
	current, ok := b.requests[next]
	if !ok {
		return ErrPipelineCapacity
	}
	old.pinned = true
	current.pinned = false
	b.requests[previous], b.requests[next] = old, current
	b.businessOwner = next
	lane, e := checkedSum(b.limits.Item, b.limits.Responses, RequestMetadataBytes, b.businessMaximum(0))
	if e != nil {
		return e
	}
	b.lane = lane
	a.notifyLocked()
	return nil
}
