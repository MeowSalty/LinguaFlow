package pipeline

import (
	"context"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"
	"sync"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
)

func memberResponseDigest(ctx context.Context, payload []byte) string {
	backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "member", Operation: "hash", Bytes: int64(len(payload))})
	return responseDigest(payload)
}

// CaptureEpoch belongs to the invocation; recovery's current scope is separate.
type MemberKey = responsecontract.MemberKey
type MemberOutcome = responsecontract.MemberOutcome
type FrozenContext struct {
	Pool, MainAttempt                    int
	Mode, Format, SourceLang, TargetLang string
	TextMode                             bool
}
type InputMemberFrame struct {
	Key MemberKey
	ResponseMember
	PromptID   string
	Payload    []byte
	Bytes      int64
	Digest     string
	Release    func()
	Allocation *backend.OwnedAllocation
}
type OutputMemberFrame struct {
	StoredBytes int64
	Key         MemberKey
	Payload     []byte
	Bytes       int64
	Digest      string
	Release     func()
	Allocation  *backend.OwnedAllocation
}
type CapturedResponse struct {
	MainResponseHeader
	Payload    []byte
	Record     RequestRecord
	Release    func()
	Allocation *backend.OwnedAllocation
}
type OutputPublication struct {
	Key              MemberKey
	Members          []OutputMemberFrame
	Next             int
	Digest           string
	Glossary         []byte
	PublicAllocation *backend.OwnedAllocation
	Complete         bool
}

// MemberResponseStore never exposes the complete frozen request or output set.
type MemberResponseStore interface {
	LoadInputMember(context.Context, MemberKey, int64) (InputMemberFrame, error)
	LoadOutputMember(context.Context, MemberKey, bool, int64) (OutputMemberFrame, error)
	CaptureResponse(context.Context, CapturedResponse) (MainResponseHeader, error)
	LoadCapture(context.Context, string, int64) (CapturedResponse, error)
	PublishOutputs(context.Context, OutputPublication) (MainResponseHeader, error)
	LoadResponsePublic(context.Context, string, int64) (OutputMemberFrame, error)
	StageCorrectedMember(context.Context, OutputMemberFrame) error
	PublishBusiness(context.Context, string, glossary.ResponseBusiness, func()) (MainResponseHeader, error)
	AbortMemberBusiness(context.Context, string) error
}

func onceRelease(fn func()) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			if fn != nil {
				fn()
			}
		})
	}
}

// archivedOutput is the canonical selected model member, without a response tree.
type archivedOutput = repair.ArchiveMember
