package pipeline

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/model"
)

// ErrCandidateStale lets storage report a failed content CAS without exposing
// database types. Other members of the same received batch can still be saved.
var ErrCandidateStale = errors.New("candidate baseline changed")

type WorkCursor = model.RoundWorkCursor
type CursorTransition struct {
	Index  int
	Cursor WorkCursor
}

type RoundRecovery struct {
	Sealed    bool
	Members   []int
	Completed []int
	Cursors   map[int]WorkCursor
}

type RequestIntent struct {
	FrozenContext
	InputMembers   []InputMemberFrame
	RoundIndex     int
	ResourceID     int
	ID             string
	Stage          backend.RequestStage
	BackendID      int
	Indices        []int
	CandidateID    string
	Pool           int
	Attempt        int
	LogicalAttempt int
	NetworkAttempt int
	Phase          string
	InputDigest    string
	Members        []RequestMember
	SnapshotDigest string
	BusinessBound  int64
}

// RequestMember freezes one candidate's identity and pre-dispatch attempt
// cursor. A batch shares a request, never its members' attempt counters.
type RequestMember struct {
	Bytes            int64
	Index            int
	WorkID           string
	CandidateID      string
	CandidateVersion int64
	Pool             int
	LogicalAttempt   int
	NetworkAttempt   int
}

type RequestRecord struct {
	State      string
	UsageKnown bool
	Usage      backend.Usage
	Duration   time.Duration
	Error      string
}

type CommitOutcome string

const (
	CommitAccepted     CommitOutcome = "committed"
	CommitExisting     CommitOutcome = "already_committed"
	CommitNoop         CommitOutcome = "confirmed_noop"
	CommitStale        CommitOutcome = "stale"
	CommitRetryStorage CommitOutcome = "retryable_storage"
	CommitRejected     CommitOutcome = "rejected"
	CommitFatal        CommitOutcome = "fatal"
)

// RoundStore is the persistence boundary consumed by the staged executor.
// The database adapter owns identity mapping and all transactional acceptance.
type RoundStore interface {
	MemberResponseStore
	CanonicalRoundStore
	CandidatePayloadStore
	Committer
	Load(context.Context) (RoundRecovery, error)
	Seal(context.Context, []int) (RoundRecovery, error)
	Candidates(context.Context, int, int) ([]*Candidate, int, error)
	Save(context.Context, *Candidate) error
	Retire(context.Context, *Candidate, WorkCursor) error
	Cursors(context.Context, []CursorTransition) error
	Reserve(context.Context, RequestIntent) error
	Record(context.Context, string, RequestRecord) error
	ResponseHeaders(context.Context, int, int) ([]MainResponseHeader, int, error)
	PromoteMember(context.Context, MemberKey, *Candidate, WorkCursor, string) error
}

// Committer confirms one candidate atomically. Infrastructure owns version
// checks, accepted content, terminal candidate state, checkpoints and progress.
type Committer interface {
	Commit(context.Context, *Candidate, TranslatedSegment) (CommitOutcome, error)
}

// RequestAborter refunds a reservation only when admission proves that the
// current process never dispatched it. Recovered unknown requests cannot use it.
type RequestAborter interface {
	AbortRequest(context.Context, string) error
}

// MemoryRoundStore gives synchronous entrypoints exactly the same candidate and
// confirmation contract without manufacturing Job checkpoints or durable recovery.
type memoryCandidateOwner struct {
	Admission *backend.RequestAdmission
	Window    *CandidateWindow
}
type MemoryRoundStore struct {
	mu                  sync.Mutex
	state               RoundRecovery
	candidates          map[string][]byte
	headers             map[string]CandidateHeader
	confirmed           map[string]bool
	apply               func(context.Context, BatchResult) error
	identity            string
	requests            map[string]*memoryResponse
	nextRequest         int
	closed              bool
	candidateOwners     map[string]map[memoryCandidateOwner]struct{}
	promotionCandidates map[string]*Candidate
	promotionActive     map[string]bool
}

// MemoryStore also identifies adapters embedding this store, so their audit
// backing follows the same lifetime as the underlying in-memory store.
func (s *MemoryRoundStore) MemoryStore() *MemoryRoundStore { return s }

// Close is called after all store users and response borrowers have joined.
// Runtime.Close alone cannot refund these retained audit records.
func (s *MemoryRoundStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.state = RoundRecovery{}
	s.apply = nil
	var result error
	// This store has no durable candidate destination after Close. Every user
	// has joined; retire each actual payload before its matching inventory.
	s.candidates, s.headers, s.confirmed, s.promotionCandidates = nil, nil, nil, nil
	s.promotionActive = nil
	for id, runtimes := range s.candidateOwners {
		for runtime := range runtimes {
			runtime.Admission.ReleaseMemoryPromotion(id)
			runtime.Window.Release(id)
			result = errors.Join(result, runtime.Admission.ReleaseBuild("candidate:"+id))
			result = errors.Join(result, runtime.Admission.ReleaseBuild(s.promotionOwner(id)))
		}
	}
	s.candidateOwners = nil
	// Keep each record's owned identity and audit backing until the table is
	// gone. The record pointer avoids allocating another table during Close.
	var retired *memoryResponse
	for _, record := range s.requests {
		record.retiredNext = retired
		retired = record
	}
	s.requests = nil
	for retired != nil {
		record := retired
		retired = record.retiredNext
		admission, owner := record.historyAdmission, record.historyOwner
		for _, backings := range []map[int]*memberBacking{record.memberBackings, record.outputBackings, record.correctionBackings, record.stagedCorrectionBackings, record.archiveStaging} {
			for _, backing := range backings {
				result = errors.Join(result, backing.drop())
			}
		}
		record.capture.drop()
		record.publicBacking.drop()
		*record = memoryResponse{}
		record = nil
		if admission != nil {
			result = errors.Join(result, admission.ReleaseBuild(owner))
		}
	}
	return result
}

func NewMemoryRoundStore(apply func(context.Context, BatchResult) error) *MemoryRoundStore {
	return &MemoryRoundStore{state: RoundRecovery{Cursors: map[int]WorkCursor{}}, candidates: map[string][]byte{}, headers: map[string]CandidateHeader{}, confirmed: map[string]bool{}, requests: map[string]*memoryResponse{}, apply: apply, identity: NewWorkID()}
}

func (s *MemoryRoundStore) WorkIdentity(index int) string {
	return fmt.Sprintf("call:%s/segment:%d", s.identity, index)
}
func (s *MemoryRoundStore) Load(context.Context) (RoundRecovery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return copyRoundRecovery(s.state), nil
}
func (s *MemoryRoundStore) Seal(_ context.Context, ids []int) (RoundRecovery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.state.Sealed {
		s.state.Members = append([]int(nil), ids...)
		s.state.Sealed = true
	}
	return copyRoundRecovery(s.state), nil
}
func copyRoundRecovery(in RoundRecovery) RoundRecovery {
	out := RoundRecovery{Sealed: in.Sealed, Members: append([]int(nil), in.Members...), Completed: append([]int(nil), in.Completed...), Cursors: make(map[int]WorkCursor, len(in.Cursors))}
	for i, c := range in.Cursors {
		out.Cursors[i] = c
	}
	return out
}
func (s *MemoryRoundStore) Candidates(_ context.Context, after, limit int) ([]*Candidate, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var page []*Candidate
	for _, payload := range s.candidates {
		c, err := DecodeCandidate(payload)
		if err != nil {
			return nil, after, err
		}
		if c.Index+1 > after {
			c.StoredBytes = int64(len(payload))
			page = append(page, c)
		}
	}
	sort.Slice(page, func(i, j int) bool { return page[i].Index < page[j].Index })
	if len(page) > limit {
		page = page[:limit]
	}
	if len(page) > 0 {
		after = page[len(page)-1].Index + 1
	}
	return page, after, nil
}
func (s *MemoryRoundStore) Save(ctx context.Context, c *Candidate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	payload, err := c.Encode()
	if err != nil {
		return err
	}
	s.candidates[c.ID] = payload
	s.retainCandidateRuntime(ctx, c.ID)
	c.StoredBytes = int64(len(payload))
	s.headers[c.ID] = HeaderForCandidate(c)
	s.state.Cursors[c.Index] = WorkCursor{Pool: c.PoolIndex, Attempt: c.MainAttempt, LogicalAttempt: c.LogicalAttempt, NetworkAttempt: c.NetworkAttempt, NextAttemptAt: c.NextAttemptAt, State: "candidate"}
	return nil
}

func (s *MemoryRoundStore) retainCandidateRuntime(ctx context.Context, id string) {
	if runtime := ExecutionRuntimeFromContext(ctx); runtime != nil {
		if s.candidateOwners == nil {
			s.candidateOwners = make(map[string]map[memoryCandidateOwner]struct{})
		}
		if s.candidateOwners[id] == nil {
			s.candidateOwners[id] = make(map[memoryCandidateOwner]struct{})
		}
		s.candidateOwners[id][memoryCandidateOwner{runtime.Admission, runtime.Window}] = struct{}{}
	}
}

func (s *MemoryRoundStore) promotionOwner(id string) string {
	return "memory-candidate:" + s.identity + ":" + id
}

// The save callback has entered. Retain the original compact candidate through
// uncertain acknowledgement, including every accurately owned header field.
func (s *MemoryRoundStore) retainPromotionAllocation(ctx context.Context, candidate *Candidate, b *backend.BuildBounds) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return backend.ErrAdmissionClosed
	}
	runtime := ExecutionRuntimeFromContext(ctx)
	if runtime == nil {
		return backend.ErrPipelineCapacity
	}
	id := candidate.ID
	old := s.promotionCandidates[id]
	if old != nil {
		if old == candidate {
			return nil
		}
		// Failed staged runs stop their admission. A resumed response uses a
		// fresh runtime; same-runtime reliable retries keep the original object.
		if _, sameRuntime := s.candidateOwners[id][memoryCandidateOwner{runtime.Admission, runtime.Window}]; sameRuntime {
			return backend.ErrPipelineCapacity
		}
		// A finished failed attempt may be rebuilt from its retained response.
		// A saved row or an active borrower rules out replacing its backing.
		response := s.requests[candidate.ParentRequestID]
		if s.promotionActive[id] || s.candidates[id] != nil || s.confirmed[id] || response == nil || response.response.Cursor >= response.response.MemberCount || response.inputs[response.response.Cursor].CandidateID != id || old.ParentRequestID != candidate.ParentRequestID || old.WorkID != candidate.WorkID || old.Version != candidate.Version {
			return backend.ErrPipelineCapacity
		}
	}
	retained, err := retainedBacking(ctx, b, reflect.ValueOf(candidate), 0)
	if err != nil {
		return err
	}
	retained -= int64(cap(candidate.PreparedPayload))
	if retained < 0 {
		return backend.ErrBuildCapacity
	}
	allocation, err := b.RetainedAllocation(retained)
	if err != nil {
		return err
	}
	defer func() { _ = allocation.ReturnToBuild() }()
	if err = runtime.Admission.RetainMemoryPromotion(id); err != nil {
		return err
	}
	if err = backend.TransferBuildAllocationToBuild(ctx, allocation, s.promotionOwner(id)); err != nil {
		runtime.Admission.ReleaseMemoryPromotion(id)
		return err
	}
	// From this point the store really retains every transferred object, even
	// if dispatch closes concurrently or the outer save never returns an ACK.
	if s.promotionCandidates == nil {
		s.promotionCandidates = make(map[string]*Candidate)
	}
	s.promotionCandidates[id] = candidate
	if s.promotionActive == nil {
		s.promotionActive = make(map[string]bool)
	}
	s.promotionActive[id] = true
	s.retainCandidateRuntime(ctx, id)
	if old != nil {
		// The replacement owns its receipts before the prior failed attempt
		// loses its last payload/header reference and original credits.
		*old = Candidate{}
		old = nil
		for previous := range s.candidateOwners[id] {
			if previous == (memoryCandidateOwner{runtime.Admission, runtime.Window}) {
				continue
			}
			previous.Admission.ReleaseMemoryPromotion(id)
			previous.Window.Release(id)
			delete(s.candidateOwners[id], previous)
			if err = previous.Admission.ReleaseBuild(s.promotionOwner(id)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *MemoryRoundStore) finishPromotionAttempt(candidate *Candidate) {
	if candidate == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.promotionCandidates[candidate.ID] == candidate {
		s.promotionActive[candidate.ID] = false
	}
}

func (s *MemoryRoundStore) dropPromotionPayload(id string) {
	if candidate := s.promotionCandidates[id]; candidate != nil {
		candidate.PreparedPayload = nil
	}
	for runtime := range s.candidateOwners[id] {
		runtime.Admission.ReleaseMemoryPromotion(id)
	}
}
func (s *MemoryRoundStore) Cursors(_ context.Context, transitions []CursorTransition) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range transitions {
		s.state.Cursors[t.Index] = t.Cursor
	}
	return nil
}
func (s *MemoryRoundStore) Commit(ctx context.Context, c *Candidate, result TranslatedSegment) (CommitOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.confirmed[c.ID] {
		return CommitExisting, nil
	}
	s.confirmed[c.ID] = true
	c.Segment.Target, c.Segment.Issues = result.TargetText, result.Issues
	if c.Mode == RoundModeTranslate {
		c.Segment.Status = "translated"
	}
	s.state.Completed = append(s.state.Completed, c.Index)
	delete(s.candidates, c.ID)
	s.dropPromotionPayload(c.ID)
	return CommitAccepted, nil
}

func (s *MemoryRoundStore) Retire(_ context.Context, c *Candidate, cursor WorkCursor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.candidates, c.ID)
	s.dropPromotionPayload(c.ID)
	s.state.Cursors[c.Index] = cursor
	return nil
}

// ApplyConfirmed runs on the serial resource path, never on a worker.
func (s *MemoryRoundStore) ApplyConfirmed(ctx context.Context, c *Candidate, result TranslatedSegment) (bool, error) {
	if s.apply == nil {
		return false, nil
	}
	return true, s.apply(ctx, BatchResult{Segments: []TranslatedSegment{result}})
}
