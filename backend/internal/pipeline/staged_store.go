package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

// ErrCandidateStale lets storage report a failed content CAS without exposing
// database types. Other members of the same received batch can still be saved.
var ErrCandidateStale = errors.New("candidate baseline changed")

type WorkCursor struct {
	Pool           int
	Attempt        int
	LogicalAttempt int
	NetworkAttempt int
	Phase          string
	State          string
	NextAttemptAt  time.Time
}

type RoundRecovery struct {
	Sealed    bool
	Members   []int
	Completed []int
	Cursors   map[int]WorkCursor
}

type RequestIntent struct {
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
}

// RequestMember freezes one candidate's identity and pre-dispatch attempt
// cursor. A batch shares a request, never its members' attempt counters.
type RequestMember struct {
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
	Committer
	Load(context.Context) (RoundRecovery, error)
	Seal(context.Context, []int) (RoundRecovery, error)
	Candidates(context.Context, int, int) ([]*Candidate, int, error)
	Save(context.Context, *Candidate) error
	Retire(context.Context, *Candidate, WorkCursor) error
	Cursor(context.Context, int, WorkCursor) error
	Reserve(context.Context, RequestIntent) error
	Record(context.Context, string, RequestRecord) error
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
type MemoryRoundStore struct {
	mu         sync.Mutex
	state      RoundRecovery
	candidates map[string][]byte
	confirmed  map[string]bool
	apply      func(context.Context, BatchResult) error
	identity   string
}

func NewMemoryRoundStore(apply func(context.Context, BatchResult) error) *MemoryRoundStore {
	return &MemoryRoundStore{state: RoundRecovery{Cursors: map[int]WorkCursor{}}, candidates: map[string][]byte{}, confirmed: map[string]bool{}, apply: apply, identity: NewWorkID()}
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
func (s *MemoryRoundStore) Save(_ context.Context, c *Candidate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	payload, err := c.Encode()
	if err != nil {
		return err
	}
	s.candidates[c.ID] = payload
	c.StoredBytes = int64(len(payload))
	s.state.Cursors[c.Index] = WorkCursor{Pool: c.PoolIndex, Attempt: c.MainAttempt, LogicalAttempt: c.LogicalAttempt, NetworkAttempt: c.NetworkAttempt, NextAttemptAt: c.NextAttemptAt, State: "candidate"}
	return nil
}
func (s *MemoryRoundStore) Cursor(_ context.Context, index int, c WorkCursor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Cursors[index] = c
	return nil
}
func (s *MemoryRoundStore) Reserve(context.Context, RequestIntent) error        { return nil }
func (s *MemoryRoundStore) Record(context.Context, string, RequestRecord) error { return nil }
func (s *MemoryRoundStore) AbortRequest(context.Context, string) error          { return nil }
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
	return CommitAccepted, nil
}

func (s *MemoryRoundStore) Retire(_ context.Context, c *Candidate, cursor WorkCursor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.candidates, c.ID)
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
