// Package workstate owns durable round work, private candidates and atomic
// acceptance. It deliberately has no dependency on the pipeline or service.
package workstate

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

var (
	ErrStale            = errors.New("work baseline is stale")
	ErrStopped          = errors.New("job does not allow this work")
	ErrManifest         = errors.New("round work manifest is missing or inconsistent")
	ErrBudget           = errors.New("request attempt budget exhausted")
	ErrCandidateVersion = errors.New("candidate version is stale")
)

type Scope struct {
	JobID            int
	ResourceID       int
	JobResourceID    int
	RoundID          int
	RetryEpoch       int64
	SourceGeneration int64
	SourceRevisionID *int
}

type Cursor struct {
	// These identify the response that authorizes clearing an alignment network
	// cursor. They are handoff metadata; the candidate payload retains them.
	LastAlignmentRequestID   string
	CandidateVersion         int64
	WorkID                   string
	State                    string
	PoolIndex                int
	MainAttempts             int
	AlignmentAttempts        int
	NetworkAttempts          int
	MainNetworkAttempts      int
	AlignmentNetworkAttempts int
	PromptPhase              string
	NextAttemptAt            *time.Time
	LastError                string
	Data                     json.RawMessage
}

type Work struct {
	SegmentID   int
	RetryEpoch  int64
	CandidateID string
	Cursor      Cursor
}

type RoundState struct {
	Sealed          bool
	ManifestVersion int
	Total           int
	PoolIndex       int
	Members         []int
	Completed       []int
	Work            []Work
}

type Candidate struct {
	ID                     string
	WorkID                 string
	LastAlignmentRequestID string
	ParentRequestID        string
	Version                int64
	Scope                  Scope
	SegmentID              int
	DTOVersion             int
	Mode                   string
	SnapshotDigest         string
	BaselineVersion        int64
	BaselineTarget         *string
	BaselineStatus         string
	State                  string
	Payload                json.RawMessage
}

type Request struct {
	ID          string
	CandidateID string
	Scope       Scope
	SegmentIDs  []int
	Members     []RequestMember
	Stage       string
	BackendID   int
	BudgetModel string
	InputDigest string
	// Logical attempts are distinct from individual network invocations. The
	// caller sets these only when opening a new main/alignment logical attempt.
	MainAttempt          bool
	AlignmentAttempt     bool
	MaxMainAttempts      int
	MaxAlignmentAttempts int
	MaxNetworkAttempts   int
}

// RequestMember freezes each candidate and its cursor before one invocation.
// NetworkAttempt zero opens a new logical attempt; otherwise it continues the
// already debited logical attempt without refreshing its network allowance.
type RequestMember struct {
	SegmentID        int    `json:"segment_id"`
	WorkID           string `json:"work_id"`
	CandidateID      string `json:"candidate_id"`
	CandidateVersion int64  `json:"candidate_version"`
	Pool             int    `json:"pool"`
	LogicalAttempt   int    `json:"logical_attempt"`
	NetworkAttempt   int    `json:"network_attempt"`
}

type RequestResult struct {
	State        string // sent, received, unknown, completed, failed; never changes reservation
	UsageKnown   bool
	InputTokens  int64
	OutputTokens int64
	DurationMS   int64
	LastError    string
}

type Outcome string

const (
	Committed        Outcome = "committed"
	AlreadyCommitted Outcome = "already_committed"
	ConfirmedNoop    Outcome = "confirmed_noop"
	Stale            Outcome = "stale"
	RetryableStorage Outcome = "retryable_storage"
	Rejected         Outcome = "rejected"
	Fatal            Outcome = "fatal"
)

type CommitInput struct {
	Scope            Scope
	SegmentID        int
	CommitID         string
	CandidateID      string
	CandidateVersion int64
	BaselineVersion  int64
	BaselineTarget   *string
	BaselineStatus   string
	Target           string
	Status           string // empty preserves current status
	Issues           []qa.QualityIssue
	ClearReview      bool
	Noop             bool
}

type CommitResult struct {
	Outcome        Outcome
	ContentVersion int64
}

// Confirmation describes a small durable completed fact, never a draft.
type Confirmation struct {
	SegmentID   int
	CommitID    string
	CandidateID string
	Outcome     Outcome
}
