package backend

import (
	"errors"
	"fmt"
	"math"
)

const RequestMetadataBytes int64 = 32 << 10

var ErrPipelineCapacity = errors.New("pipeline capacity conflict")

type permanentBudgetError string

func (e permanentBudgetError) Error() string   { return string(e) }
func (e permanentBudgetError) Permanent() bool { return true }

var ErrMainBatchTooLarge error = permanentBudgetError("main batch cannot fit response handoff")
var ErrNoCapacityDrain error = permanentBudgetError("execution resource budget has no runnable consumer")

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
	if a.budget != nil && intent.Stage == RequestStageMain && a.mainPriority && (intent.ResourceID != a.priorityResource || intent.RoundIndex != a.priorityRound) {
		return 0, "fair_main_turn", nil
	}
	if a.budget == nil || !exact && intent.RequestID == "" {
		return 0, "", nil
	}
	if intent.Stage == RequestStageMain {
		lane, reason, err := a.budget.canGrantMain(intent)
		if reason != "" && err == nil && !a.mainPriority {
			a.mainPriority = true
			a.priorityResource, a.priorityRound = intent.ResourceID, intent.RoundIndex
		}
		return lane, reason, err
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
	PlannedCount                   int
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
	Lifecycle                                                                                 PipelineRuntimeState
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
	full                               bool
	direct                             bool
	responseOwned                      bool
	businessAttached                   bool
	restored                           bool
	corrected                          int64
	attachment                         int64
}
type pipelineBudget struct {
	baseSegments                            int64
	baseCandidates, baseGrowth, baseHandoff int64
	inventoryRestored                       bool
	owned                                   ownedPayloadState
	mainOwnerPeak, auxOwnerPeak             int
	limits                                  PipelineBudgetLimits
	entries                                 map[string]PipelineBudgetEntry
	requests                                map[string]requestBudget
	mainOwners                              map[int]int
	auxOwners                               int
	auxInput, auxResponse                   int64
	auxPrepared                             map[string]int64
	progressID                              string
	lane                                    int64
	businessOwner                           string
	rootStandby                             int64
	state                                   PipelineRuntimeState
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

func CheckedProduct(left, right int64) (int64, error) {
	if left < 0 || right < 0 || left != 0 && right > math.MaxInt64/left {
		return 0, ErrPipelineCapacity
	}
	return left * right, nil
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
	previous, previousProgress := e, b.progressID
	commit := func() (bool, error) {
		b.entries[id] = e
		if err := a.syncRootLocked(0); err != nil {
			b.entries[id] = previous
			b.progressID = previousProgress
			if aggregateCapacity(err) {
				return false, nil
			}
			return false, err
		}
		a.notifyLocked()
		return true, nil
	}
	ordinary := b.baseCandidates + b.baseGrowth
	for _, v := range b.entries {
		if !v.Progress {
			ordinary += v.Bytes + v.Growth
		}
	}
	delta := b.limits.Item - e.Bytes - e.Growth
	_, candidateBytes, growthBytes := b.candidateTotals()
	if delta > 0 && delta > max(b.limits.Candidates, b.limits.Item)-candidateBytes-growthBytes {
		return false, nil
	}
	if e.Progress || delta <= max(int64(0), b.limits.Candidates-b.limits.Item)-ordinary {
		e.Growth = b.limits.Item - e.Bytes
		return commit()
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
	return commit()
}

func (b *pipelineBudget) candidateTotals() (count int, bytes, growth int64) {
	count, bytes, growth = int(b.baseSegments), b.baseCandidates, b.baseGrowth
	for _, e := range b.entries {
		count += e.Count
		bytes += e.Bytes
		growth += e.Growth
	}
	return
}
func (b *pipelineBudget) requestTotal() int64 {
	total := b.baseHandoff
	for _, r := range b.requests {
		total += r.known
		if r.granted {
			total += r.raw + RequestMetadataBytes
			total += r.corrected
			if r.full && !r.businessAttached {
				total += r.business
			}
		}
	}
	return total
}
func (b *pipelineBudget) businessMaximum(extra int64) int64 {
	for _, r := range b.requests {
		if r.granted && !r.pinned && !r.businessAttached && r.business > extra {
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
	out := PipelineBudgetSnapshot{Segments: count, CandidateBytes: bytes, GrowthBytes: growth, HandoffBytes: b.requestTotal() + b.rootStandby, AuxInputBytes: b.auxInput, AuxResponseBytes: b.auxResponse, ProgressID: b.progressID, ProgressLane: b.rootStandby, MainOwners: map[int]int{}, AuxOwners: b.auxOwners, Revision: a.version}
	for _, r := range b.requests {
		if !r.granted {
			out.PreparedBytes += r.known
		}
	}
	for round, n := range b.mainOwners {
		out.MainOwners[round] = n
	}
	out.Lifecycle = b.state
	out.MainOwnerPeak = b.mainOwnerPeak
	out.AuxOwnerPeak = b.auxOwnerPeak
	out.OwnedPayloads = a.ownedPayloadSnapshotLocked()
	out.RequestedMain = map[int]int{}
	out.EffectiveMain = map[int]int{}
	out.MainCapacityReason = map[int]string{}
	known := map[int]int64{}
	memberSize := map[int]int{}
	pinned := false
	for id, request := range b.requests {
		if entry, ok := b.entries[id]; ok {
			n := max(entry.Count, entry.PlannedCount)
			if n > 0 && (memberSize[request.round] == 0 || n < memberSize[request.round]) {
				memberSize[request.round] = n
			}
		}
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
			if n := memberSize[round]; n > 0 {
				byS := int64(max(0, b.limits.Segments-1-count) / n)
				if byS < available {
					available = byS
					reason = "member_capacity"
				}
			}
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
	ordinary := b.baseCandidates + b.baseGrowth
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

// PrepareMembers retains a bounded descriptor only. S is transferred together
// with the response and RPM grant, after the complete input is ready.
func (a *RequestAdmission) PrepareMembers(key string, count, resource, round int, replacements []string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	if b == nil || key == "" || count < 1 || count > b.limits.Segments {
		return false, ErrPipelineCapacity
	}
	if old, ok := b.entries[key]; ok {
		return old.PlannedCount == count, nil
	}
	if len(replacements) > count {
		return false, ErrPipelineCapacity
	}
	seen := map[string]bool{}
	for _, id := range replacements {
		if id == "" || seen[id] {
			return false, ErrPipelineCapacity
		}
		seen[id] = true
		if e, ok := b.entries[id]; ok && (!e.Reservation || !e.Durable || e.Count != 1 || e.ResourceID != resource || e.RoundID != round) {
			return false, ErrPipelineCapacity
		}
	}
	metadataAdded := false
	if a.root != nil && a.rootScope.JobID > 0 {
		var e error
		metadataAdded, e = a.prepareRootMetadataLocked(key, count)
		if e != nil {
			return false, e
		}
		if e = a.syncRootLocked(0); e != nil {
			if metadataAdded {
				delete(a.requestMetadata, key)
			}
			return false, a.classifyRootCapacityLocked(e)
		}
	}
	b.entries[key] = PipelineBudgetEntry{ResourceID: resource, RoundID: round, PlannedCount: count, Reservation: true, Replaced: append([]string(nil), replacements...)}
	a.notifyLocked()
	return true, nil
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
	if count == b.limits.Segments || progress {
		limit = b.limits.Segments
	}
	if count > limit-n {
		return false, nil
	}
	previous := make(map[string]PipelineBudgetEntry, len(replaced))
	for _, id := range replaced {
		previous[id] = b.entries[id]
		delete(b.entries, id)
	}
	b.entries[key] = PipelineBudgetEntry{ResourceID: resource, RoundID: round, Count: count, Reservation: true, Progress: progress, Replaced: replaced}
	if err := a.syncRootLocked(0); err != nil {
		delete(b.entries, key)
		for _, id := range replaced {
			b.entries[id] = previous[id]
		}
		if aggregateCapacity(err) {
			return false, nil
		}
		return false, err
	}
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
	if e.Progress && b.progressID != "" && b.progressID != key {
		return ErrPipelineCapacity
	}
	if b.inventoryRestored {
		if int64(e.Count) > b.baseSegments || e.Bytes > b.baseCandidates || e.Growth > b.baseGrowth {
			return ErrPipelineCapacity
		}
		b.baseSegments -= int64(e.Count)
		b.baseCandidates -= e.Bytes
		b.baseGrowth -= e.Growth
	}
	b.entries[key] = e
	if e.Progress {
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
	oldProgress := b.progressID
	e, ok := b.entries[from]
	if !ok || !e.Reservation || e.Count < 1 || size < 0 || size > b.limits.Item {
		return false, ErrPipelineCapacity
	}
	if pending, ok := b.entries[to]; ok {
		return pending.Count == 0 && pending.Bytes == size, nil
	}
	_, bytes, growth := b.candidateTotals()
	ordinary := b.baseCandidates + b.baseGrowth
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
	if rootErr := a.syncRootLocked(0); rootErr != nil {
		delete(b.entries, to)
		b.progressID = oldProgress
		if aggregateCapacity(rootErr) {
			return false, nil
		}
		return false, rootErr
	}
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
	if metadata := a.requestMetadata[id]; metadata != nil && !metadata.dispatched {
		a.releaseRootMetadataLocked(id)
	}
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
func (a *RequestAdmission) PrepareMain(id string, round, resource int, freeze, wire, business int64, progress bool, members ...int) (bool, error) {
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
	fullRequired, e := checkedSum(known, b.limits.Responses, RequestMetadataBytes, business)
	if e != nil {
		return false, e
	}
	if fullRequired > b.limits.Handoff {
		return false, ErrMainBatchTooLarge
	}
	alone, e := checkedSum(known, b.limits.Responses, RequestMetadataBytes, ownLane)
	if e != nil {
		return false, e
	}
	full := alone > b.limits.Handoff
	if entry, ok := b.entries[id]; ok && max(entry.Count, entry.PlannedCount) == b.limits.Segments {
		full = true
	}
	if entry, ok := b.entries[id]; ok && entry.PlannedCount > 0 {
		n, _, _ := b.candidateTotals()
		for _, id := range entry.Replaced {
			if e, ok := b.entries[id]; ok {
				n -= e.Count
			}
		}
		if entry.PlannedCount > b.limits.Segments-1-n && entry.PlannedCount <= b.limits.Segments-n {
			full = true
		}
	}
	previous := int64(0)
	if attachment > 0 {
		previous = old.known
	}
	if known > b.limits.Handoff-(b.requestTotal()-previous) {
		return false, nil
	}
	b.requests[id] = requestBudget{round: round, resource: resource, known: known, freeze: freeze, wire: wire + attachment, business: business, progress: progress, full: full, attachment: attachment}
	r := b.requests[id]
	r.direct = freeze == 0 && business == 0
	b.requests[id] = r
	metadataAdded := false
	if a.root != nil && a.rootScope.JobID > 0 {
		memberCount := 0
		if entry, ok := b.entries[id]; ok {
			memberCount = max(entry.Count, entry.PlannedCount)
		}
		if len(members) > 1 {
			if exists {
				b.requests[id] = old
			} else {
				delete(b.requests, id)
			}
			return false, ErrPipelineCapacity
		}
		if len(members) > 0 {
			memberCount = members[0]
		}
		if memberCount != 0 {
			var e error
			metadataAdded, e = a.prepareRootMetadataLocked(id, memberCount)
			if e != nil {
				if exists {
					b.requests[id] = old
				} else {
					delete(b.requests, id)
				}
				return false, e
			}
		}
	}
	if rootErr := a.syncRootLocked(0); rootErr != nil {
		if metadataAdded {
			delete(a.requestMetadata, id)
		}
		if exists {
			b.requests[id] = old
		} else {
			delete(b.requests, id)
		}
		return false, a.classifyRootCapacityLocked(rootErr)
	}
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
	if err != nil {
		return 0, "", ErrPipelineCapacity
	}
	if r.direct {
		lane = 0
	}
	if entry, ok := b.entries[intent.RequestID]; ok && entry.PlannedCount > 0 {
		n, _, _ := b.candidateTotals()
		for _, id := range entry.Replaced {
			if e, ok := b.entries[id]; ok {
				n -= e.Count
			}
		}
		limit := b.limits.Segments - 1
		if r.full {
			limit = b.limits.Segments
		}
		if entry.PlannedCount > limit-n {
			return 0, "member_capacity", nil
		}
	}
	for id, owned := range b.requests {
		if id != intent.RequestID && owned.full && owned.granted {
			return 0, "full_owner", nil
		}
	}
	if r.full {
		for id, owned := range b.requests {
			if id != intent.RequestID && owned.responseOwned && !owned.pinned {
				return 0, "full_owner_drain", nil
			}
		}
		lane = r.business
	}
	required, err := checkedSum(b.requestTotal(), b.limits.Responses, RequestMetadataBytes, lane)
	if err != nil {
		return 0, "", err
	}
	if required > b.limits.Handoff {
		return 0, "response_handoff", nil
	}
	if r.full {
		lane = 0
	}
	return lane, "", nil
}

func (b *pipelineBudget) canGrantAux(intent RequestAdmissionIntent) (string, error) {
	if _, err := checkedSum(b.auxResponse, b.limits.Responses); err != nil {
		return "", err
	}
	if _, err := checkedSum(b.auxInput, intent.InputBytes); err != nil {
		return "", err
	}
	if intent.InputBytes < 0 || intent.InputBytes > max(b.limits.Candidates, b.limits.Item) {
		return "", ErrPipelineCapacity
	}
	prepared := int64(0)
	for id, n := range b.auxPrepared {
		if id != intent.RequestID {
			prepared += n
		}
	}
	if intent.InputBytes > max(b.limits.Candidates, b.limits.Item)-b.auxInput-prepared {
		return "aux_input", nil
	}
	ordinary := b.baseCandidates + b.baseGrowth
	for _, e := range b.entries {
		if !e.Progress {
			ordinary += e.Bytes + e.Growth
		}
	}
	additional := int64(0)
	allAdditional := int64(0)
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
		allAdditional += b.limits.Item - e.Bytes - e.Growth
		if e.Progress {
			if len(intent.CandidateIDs) != 1 {
				return "", ErrPipelineCapacity
			}
			continue
		}
		additional += b.limits.Item - e.Bytes - e.Growth
	}
	_, candidateBytes, growthBytes := b.candidateTotals()
	if allAdditional > 0 && allAdditional > max(b.limits.Candidates, b.limits.Item)-candidateBytes-growthBytes {
		return "candidate_growth", nil
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
		if entry, ok := b.entries[intent.RequestID]; ok && entry.PlannedCount > 0 {
			for _, id := range entry.Replaced {
				delete(b.entries, id)
			}
			entry.Count = entry.PlannedCount
			entry.PlannedCount = 0
			b.entries[intent.RequestID] = entry
		}
		r := b.requests[intent.RequestID]
		r.granted = true
		r.responseOwned = true
		r.raw = b.limits.Responses
		b.requests[intent.RequestID] = r
		b.mainOwners[intent.RoundIndex]++
		total := 0
		for _, n := range b.mainOwners {
			total += n
		}
		b.mainOwnerPeak = max(b.mainOwnerPeak, total)
		b.lane = lane
		if !r.full && !r.direct {
			b.rootStandby = lane
		}
		if r.full {
			b.rootStandby = 0
		}
	} else {
		b.auxOwners++
		delete(b.auxPrepared, intent.RequestID)
		b.auxOwnerPeak = max(b.auxOwnerPeak, b.auxOwners)
		b.auxInput += intent.InputBytes
		b.auxResponse += b.limits.Responses
		ordinary := b.baseCandidates + b.baseGrowth
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
			a.releaseRootMetadataLocked(id)
			a.notifyLocked()
		}
		delete(a.budget.auxPrepared, id)
		if metadata := a.requestMetadata[id]; metadata != nil && !metadata.dispatched {
			a.releaseRootMetadataLocked(id)
			a.notifyLocked()
		}
	}
}

func (a *RequestAdmission) PrepareAux(id string, bytes int64, members ...int) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := a.budget
	if b == nil || id == "" || bytes < 0 || bytes > max(b.limits.Candidates, b.limits.Item) {
		return false, ErrPipelineCapacity
	}
	if n, ok := b.auxPrepared[id]; ok {
		return n == bytes, nil
	}
	total := b.auxInput
	for _, n := range b.auxPrepared {
		total += n
	}
	if bytes > max(b.limits.Candidates, b.limits.Item)-total {
		return false, nil
	}
	if b.auxPrepared == nil {
		b.auxPrepared = map[string]int64{}
	}
	b.auxPrepared[id] = bytes
	metadataAdded := false
	if a.root != nil && a.rootScope.JobID > 0 && len(members) > 0 {
		var e error
		metadataAdded, e = a.prepareRootMetadataLocked(id, members[0])
		if e != nil {
			delete(b.auxPrepared, id)
			return false, e
		}
	}
	if err := a.syncRootLocked(0); err != nil {
		if metadataAdded {
			delete(a.requestMetadata, id)
		}
		delete(b.auxPrepared, id)
		if aggregateCapacity(err) {
			return false, nil
		}
		return false, err
	}
	a.notifyLocked()
	return true, nil
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

// ReserveFinalResultCredit measures the final QA/commit envelope before its
// opaque backing allocation. Ready recovery can drain with delta zero instead
// of precharging Q; added diagnostics reserve only their actual positive delta.
func (a *RequestAdmission) ReserveFinalResultCredit(id string, size int64) error {
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
	growth := max(int64(0), size-e.Bytes)
	if growth <= e.Growth {
		return nil
	}
	delta := growth - e.Growth
	_, bytes, totalGrowth := b.candidateTotals()
	if delta > max(b.limits.Candidates, b.limits.Item)-bytes-totalGrowth {
		return ErrPipelineCapacity
	}
	ordinary := b.baseCandidates + b.baseGrowth
	for _, v := range b.entries {
		if !v.Progress {
			ordinary += v.Bytes + v.Growth
		}
	}
	if !e.Progress && delta > max(int64(0), b.limits.Candidates-b.limits.Item)-ordinary {
		return ErrProgressRequired
	}
	old := e
	e.Growth = growth
	b.entries[id] = e
	if err := a.syncRootLocked(0); err != nil {
		b.entries[id] = old
		return err
	}
	a.notifyLocked()
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
	if b.inventoryRestored {
		owned, e := checkedSum(freeze, raw, RequestMetadataBytes)
		if e != nil || owned > b.baseHandoff {
			return ErrPipelineCapacity
		}
		b.baseHandoff -= owned
	}
	lane, err := checkedSum(b.limits.Item, b.limits.Responses, RequestMetadataBytes, b.businessMaximum(business))
	if err != nil || freeze > b.limits.Handoff {
		return ErrPipelineCapacity
	}
	b.requests[id] = requestBudget{round: round, resource: resource, known: freeze, freeze: freeze, raw: raw, business: business, granted: true, restored: b.inventoryRestored}
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
	a.releaseStorageLocked(intent.RequestID)
	a.releaseRootMetadataLocked(intent.RequestID)
	if intent.Stage == RequestStageMain {
		if r, ok := b.requests[intent.RequestID]; ok {
			if r.responseOwned {
				b.mainOwners[r.round]--
			}
			delete(b.requests, intent.RequestID)
		}
		if b.businessOwner == intent.RequestID {
			b.businessOwner = ""
		}
		lane, _ := checkedSum(b.limits.Item, b.limits.Responses, RequestMetadataBytes, b.businessMaximum(0))
		b.lane = lane
		if len(b.requests) == 0 {
			b.rootStandby = 0
		}
	} else {
		delete(a.candidateReads, intent.RequestID)
		b.auxOwners--
		b.auxInput -= intent.InputBytes
		b.auxResponse -= b.limits.Responses
	}
}

func (a *RequestAdmission) ClaimResponseOwner(id string) (bool, error) {
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
	if r.responseOwned {
		return true, nil
	}
	if b.mainOwners[r.round] >= a.config.MainConcurrency[r.round] {
		return false, nil
	}
	r.responseOwned = true
	b.requests[id] = r
	b.mainOwners[r.round]++
	if err := a.syncRootLocked(0); err != nil {
		r.responseOwned = false
		b.requests[id] = r
		b.mainOwners[r.round]--
		if aggregateCapacity(err) {
			return false, nil
		}
		return false, err
	}
	a.notifyLocked()
	return true, nil
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
	delta := b.limits.Item - e.Bytes - e.Growth
	if b.inventoryRestored {
		if delta > b.baseGrowth {
			return ErrPipelineCapacity
		}
		b.baseGrowth -= delta
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
	old := r
	base := b.baseHandoff
	if r.restored && !r.businessAttached {
		if bytes > b.baseHandoff {
			return ErrPipelineCapacity
		}
		b.baseHandoff -= bytes
	}
	r.corrected = bytes
	r.businessAttached = true
	b.requests[id] = r
	if err := a.syncRootLocked(0); err != nil {
		b.requests[id] = old
		b.baseHandoff = base
		return err
	}
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
