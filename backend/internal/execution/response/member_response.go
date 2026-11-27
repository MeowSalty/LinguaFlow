package response

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"sync"
	"sync/atomic"
)

// ResponsePhase is processing progress, independently of invocation and work state.
type ResponsePhase string

const (
	// MainParseDiagnosticLimit bounds the ledger fact used to prepare a prompt upgrade.
	MainParseDiagnosticLimit  = 2048
	PromptUpgradeCursorPrefix = "prompt_upgrade:"
)

const (
	InputsReady       ResponsePhase = "inputs_ready"
	CapturePending    ResponsePhase = "capture_pending"
	OutputsReady      ResponsePhase = "outputs_ready"
	BusinessReady     ResponsePhase = "business_ready"
	Promoted          ResponsePhase = "promoted"
	ResponseCancelled ResponsePhase = "cancelled"
)

func (p ResponsePhase) Valid() bool {
	switch p {
	case InputsReady, CapturePending, OutputsReady, BusinessReady, Promoted, ResponseCancelled:
		return true
	}
	return false
}
func (p ResponsePhase) Terminal() bool { return p == Promoted || p == ResponseCancelled }
func (p ResponsePhase) CanAdvance(next ResponsePhase) bool {
	return p.Valid() && (p == next || !p.Terminal() && next == ResponseCancelled ||
		p == InputsReady && next == CapturePending || p == CapturePending && next == OutputsReady ||
		p == OutputsReady && next == BusinessReady || p == BusinessReady && next == Promoted)
}

type MemberKey struct {
	RequestID    string
	Ordinal      int
	CaptureEpoch int64
}
type PublicKind string

const (
	PublicNone    PublicKind = "none"
	PublicEntries PublicKind = "entries"
	PublicReceipt PublicKind = "receipt"
)

// ResponseFacts describe retained payload, never heap capacities or protocol reservations.
type ResponseFacts struct {
	CaptureBytes, InputMemberBytes, OutputMemberBytes, ModelOutputBytes, CorrectionBytes, PublicBytes, MetadataBytes int64
	PublicKind                                                                                                       PublicKind
	BusinessReservation                                                                                              int64
}

func (f ResponseFacts) Validate() error {
	if f.CaptureBytes < 0 || f.InputMemberBytes < 0 || f.OutputMemberBytes < 0 || f.ModelOutputBytes < 0 || f.CorrectionBytes < 0 || f.PublicBytes < 0 || f.MetadataBytes < 0 || f.BusinessReservation < 0 || f.ModelOutputBytes > math.MaxInt64-f.CorrectionBytes || f.OutputMemberBytes != f.ModelOutputBytes+f.CorrectionBytes {
		return errors.New("invalid response byte partitions")
	}
	switch f.PublicKind {
	case PublicNone:
		if f.PublicBytes != 0 {
			return errors.New("public bytes without publication")
		}
	case PublicEntries, PublicReceipt:
	default:
		return errors.New("invalid public kind")
	}
	return nil
}

type MemberContentKind string

const (
	MemberTooLarge MemberContentKind = "content_limit"
	MemberInvalid  MemberContentKind = "invalid_content"
	MemberMissing  MemberContentKind = "missing_content"
)

// MemberContentError is created by content preflight only. It cannot wrap system failures.
type MemberContentError struct {
	Ordinal         int
	Kind            MemberContentKind
	Limit, Observed int64
}

func (e *MemberContentError) Error() string {
	return fmt.Sprintf("member %d %s (limit=%d observed=%d)", e.Ordinal, e.Kind, e.Limit, e.Observed)
}

type MemberOutcome struct {
	Key             MemberKey
	Accepted        bool
	Reason          MemberContentKind
	Limit, Observed int64
}

func (o MemberOutcome) Resolve(key MemberKey, accepted bool) (MemberOutcome, error) {
	if key.RequestID == "" || key.Ordinal < 0 || key.CaptureEpoch < 0 || o.Key != key || o.Accepted != accepted || o.Limit < 0 || o.Observed < 0 {
		return o, errors.New("invalid member outcome identity")
	}
	if accepted {
		if o.Reason != "" || o.Limit != 0 || o.Observed != 0 {
			return o, errors.New("accepted member has rejection facts")
		}
		return o, nil
	}
	if o.Code() == 0 {
		return o, errors.New("missing member rejection reason")
	}
	return o, nil
}
func (o MemberOutcome) Code() int {
	switch o.Reason {
	case MemberTooLarge:
		return 1
	case MemberInvalid:
		return 2
	case MemberMissing:
		return 3
	}
	return 0
}
func (o MemberOutcome) String() string {
	if o.Accepted {
		return ""
	}
	return (&MemberContentError{Ordinal: o.Key.Ordinal, Kind: o.Reason, Limit: o.Limit, Observed: o.Observed}).Error()
}

type PublicationMedium uint8

const (
	PublicationMemory PublicationMedium = iota + 1
	PublicationSQL
)

// Authority is explicitly registered by the endpoint constructor (not inferred from adapter types).
type PublicationAuthority struct{ identity uint64 }

var publicationAuthorities sync.Map
var publicationSequence atomic.Uint64

func NewPublicationAuthority() *PublicationAuthority {
	return &PublicationAuthority{identity: publicationSequence.Add(1)}
}

// RegisterPublicationAuthority is called with the original SQL client, never a transaction client.
func RegisterPublicationAuthority(owner any) *PublicationAuthority {
	if owner == nil || !reflect.TypeOf(owner).Comparable() {
		return nil
	}
	next := NewPublicationAuthority()
	actual, _ := publicationAuthorities.LoadOrStore(owner, next)
	return actual.(*PublicationAuthority)
}

type PublicationDomain struct {
	Medium           PublicationMedium
	Authority        *PublicationAuthority
	JobID, ProjectID int
}

var ErrPublicationDomain = errors.New("incompatible response publication domain")

func ValidatePublication(store, dictionary PublicationDomain) error {
	if store.Authority == nil || dictionary.Authority == nil || store.Medium != dictionary.Medium || store.Authority != dictionary.Authority || store.JobID != dictionary.JobID || store.ProjectID != dictionary.ProjectID {
		return ErrPublicationDomain
	}
	if store.Medium == PublicationMemory {
		return nil
	}
	if store.Medium != PublicationSQL || store.ProjectID <= 0 || store.JobID <= 0 {
		return ErrPublicationDomain
	}
	return nil
}

// PreparedPublication owns pre-paid installation and settlement actions.
// Installation cannot allocate, fail, or reacquire its publisher's locks.
type PreparedPublication struct {
	once             sync.Once
	Install          func()
	Rollback, Retire func() error
	Park             func()
	installed        atomic.Bool
}

func (p *PreparedPublication) InstallOnce() error {
	if p == nil || p.Install == nil {
		return errors.New("missing prepared publication installation")
	}
	p.once.Do(func() { p.Install(); p.installed.Store(true) })
	return nil
}
func (p *PreparedPublication) Installed() bool { return p != nil && p.installed.Load() }
