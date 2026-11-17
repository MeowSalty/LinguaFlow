package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unsafe"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
)

type ResponseMember struct {
	Index            int
	SegmentID        int
	WorkID           string
	CandidateID      string
	BaselineVersion  int64
	BaselineTarget   *string
	BaselineStatus   string
	SourceGeneration int64
	SourceRevisionID *int
}

type MainResponseHeader struct {
	DatabaseID     int
	ID             string
	ResourceID     int
	RoundID        int
	SnapshotDigest string
	FreezeDigest   string
	ResponseDigest string
	FreezeBytes    int64
	ResponseBytes  int64
	CorrectedBytes int64
	MetadataBytes  int64
	BusinessBound  int64
	MemberCount    int
	Cursor         int
	Phase          string
	Truncated      bool
}

type MainResponse struct {
	ReleaseOwned func()
	MainResponseHeader
	FreezePayload    []byte
	Raw              []byte
	CorrectedTargets []byte
	CorrectedIndex   []byte
	Members          []ResponseMember
	Receipt          []byte
	Record           RequestRecord
}

type memoryResponse struct {
	business          *memoryResponseBusiness
	receiptAllocation *backend.OwnedAllocation
	runtime           *ExecutionRuntime
	historyAdmission  *backend.RequestAdmission
	historyOwner      string
	retiredNext       *memoryResponse
	intent            RequestIntent
	freezeIdentity    string
	response          MainResponse
	record            RequestRecord
	status            []byte
}

// A store reference and each reader share exactly one immutable backing owner.
// Replacing the current value only ends the store reference.
type memoryResponseBusiness struct {
	mu          sync.Mutex
	refs        int
	body, index []byte
	allocation  *backend.OwnedAllocation
	admission   *backend.RequestAdmission
	owner       string
	bytes       int64
}

func (b *memoryResponseBusiness) borrow() func() {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	b.refs++
	b.mu.Unlock()
	var once sync.Once
	return func() { once.Do(b.release) }
}

func (b *memoryResponseBusiness) release() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refs--
	if b.refs != 0 {
		return
	}
	b.body, b.index = nil, nil
	if b.admission != nil {
		_ = b.admission.ReleaseBuild(b.owner)
	} else {
		b.allocation.Release()
	}
	b.allocation = nil
}

func (s *MemoryRoundStore) SaveResponseBusiness(ctx context.Context, id string, in glossary.ResponseBusiness) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.requests[id]
	if r == nil || r.response.Cursor != 0 || r.response.Phase != "response_pending" || int64(len(in.Body)) > r.response.BusinessBound || len(in.Index) != r.response.MemberCount*16 || len(in.Receipt) > 32<<10 {
		return fmt.Errorf("invalid response business handoff")
	}
	owner := "business:" + r.historyOwner + ":" + NewWorkID()
	content := &memoryResponseBusiness{refs: 1, body: in.Body, index: in.Index, allocation: in.ContentAllocation, owner: owner, bytes: int64(cap(in.Body)) + int64(cap(in.Index))}
	if r.historyAdmission != nil && content.bytes > 0 {
		if err := backend.TransferBuildAllocationToBuild(ctx, in.ContentAllocation, owner); err != nil {
			return err
		}
		content.admission = r.historyAdmission
	} else {
		content.allocation = in.ContentAllocation.Transfer()
		if content.bytes > 0 && content.allocation == nil {
			return backend.ErrBuildCapacity
		}
	}
	old := r.business
	r.business = content
	// Immutable business buffers transfer into the store. The producer only
	// borrows them afterwards; retirement replaces buffers instead of mutating.
	r.response.CorrectedTargets = in.Body
	r.response.CorrectedIndex = in.Index
	r.response.CorrectedBytes = int64(len(in.Body))
	old.release()
	if r.historyAdmission != nil && len(in.Receipt) > 0 {
		if err := backend.TransferBuildAllocationToBuild(ctx, in.ReceiptAllocation, r.historyOwner); err != nil {
			return err
		}
	}
	r.response.Receipt = in.Receipt
	if r.historyAdmission == nil {
		r.receiptAllocation = in.ReceiptAllocation.Transfer()
	}
	r.response.Phase = "glossary_processed"
	return nil
}

func responseDigest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func (s *MemoryRoundStore) Reserve(ctx context.Context, intent RequestIntent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("round store closed")
	}
	if old := s.requests[intent.ID]; old != nil {
		if old.intent.InputDigest != intent.InputDigest || old.freezeIdentity != responseDigest(intent.FreezePayload) {
			return fmt.Errorf("request identity reused")
		}
		return nil
	}
	b, err := backend.NewBuildBoundsFromContext(ctx, 0, 0)
	if err != nil {
		return err
	}
	if err = backend.ReserveSlice[byte](b, int64(len(intent.PromotionMembers))); err != nil {
		return err
	}
	// The audit record survives promotion. It owns its identity separately from
	// the request payload, which can be dropped and refunded after consumption.
	historyOwner := "history:" + s.identity + ":" + intent.ID
	runtime := ExecutionRuntimeFromContext(ctx)
	historyCtx := ctx
	var admission *backend.RequestAdmission
	if runtime != nil {
		admission = runtime.Admission
		historyCtx = runtime.BuildContext(ctx, historyOwner)
	}
	historyBounds, err := backend.NewBuildBoundsFromContext(historyCtx, 0, 0)
	if err != nil {
		return err
	}
	installed := false
	defer func() {
		if !installed && admission != nil {
			_ = admission.ReleaseBuild(historyOwner)
		}
	}()
	if err = historyBounds.ReserveAllocation(1, int64(unsafe.Sizeof(memoryResponse{}))); err != nil {
		return err
	}
	for _, text := range []string{intent.ID, intent.InputDigest} {
		if err = historyBounds.ReserveAllocation(int64(len(text)), 1); err != nil {
			return err
		}
	}
	if err = historyBounds.ReserveAllocation(64, 1); err != nil {
		return err
	}
	intent.ID, intent.InputDigest = strings.Clone(intent.ID), strings.Clone(intent.InputDigest)
	s.nextRequest++
	// RequestIntent is frozen before reservation. The in-memory store retains
	// that immutable backing until response promotion rather than cloning it.
	s.requests[intent.ID] = &memoryResponse{runtime: runtime, historyAdmission: admission, historyOwner: historyOwner, intent: intent, status: make([]byte, len(intent.PromotionMembers)), freezeIdentity: responseDigest(intent.FreezePayload), record: RequestRecord{State: "reserved"}, response: MainResponse{MainResponseHeader: MainResponseHeader{ID: intent.ID, DatabaseID: s.nextRequest}}}
	installed = true
	for _, idx := range intent.Indices {
		cur := s.state.Cursors[idx]
		if intent.Phase != "prompt_upgrade" && intent.Stage == backend.RequestStageMain {
			cur.Attempt++
		}
		s.state.Cursors[idx] = cur
	}
	return nil
}

func (s *MemoryRoundStore) Record(ctx context.Context, id string, record RequestRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.requests[id]
	if r == nil {
		return fmt.Errorf("missing request")
	}
	if r.record.State != "completed" && r.record.State != "failed" && !(r.record.State == "received" && record.State == "sent") {
		if len(record.Error) > 2048 {
			return fmt.Errorf("request error exceeds audit bound")
		}
		oldError := r.record.Error
		if r.historyAdmission != nil && record.Error != oldError {
			credit, err := backend.AllocationBytes(int64(len(record.Error)), 1)
			if err != nil {
				return err
			}
			admission, source := backend.BuildOwnership(ctx)
			if admission == r.historyAdmission && source != "" && source != r.historyOwner {
				b, err := backend.NewBuildBoundsFromContext(ctx, 0, 0)
				if err != nil {
					return err
				}
				allocation, err := b.OwnAllocation(credit, 1)
				if err != nil {
					return err
				}
				record.Error = strings.Clone(record.Error)
				if err = backend.TransferBuildAllocationToBuild(ctx, allocation, r.historyOwner); err != nil {
					record.Error = ""
					allocation.Release()
					return err
				}
			} else {
				if err = r.historyAdmission.ClaimBuildBounded(r.historyOwner, credit, backend.BuildAllocationLimit(ctx)); err != nil {
					return err
				}
				record.Error = strings.Clone(record.Error)
			}
			oldCredit, err := backend.AllocationBytes(int64(len(oldError)), 1)
			if err != nil {
				return err
			}
			r.record.Error = record.Error
			oldError = ""
			if err = r.historyAdmission.ReleaseBuildBytes(r.historyOwner, oldCredit); err != nil {
				return err
			}
		}
		r.record.State = record.State
		r.record.Error = record.Error
		if record.State == "failed" || record.State == "completed" && r.response.Raw == nil && r.response.FreezePayload == nil {
			r.dropRequestBacking()
		}
	}
	if record.UsageKnown && !r.record.UsageKnown {
		r.record.UsageKnown = true
		r.record.Usage = record.Usage
	}
	if record.Duration > r.record.Duration {
		r.record.Duration = record.Duration
	}
	return nil
}

func (s *MemoryRoundStore) AbortRequest(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.requests[id]
	if r == nil || r.record.State == "cancelled_before_dispatch" {
		return nil
	}
	if r.record.State != "reserved" {
		return fmt.Errorf("request already sent")
	}
	r.record.State = "cancelled_before_dispatch"
	for _, idx := range r.intent.Indices {
		cur := s.state.Cursors[idx]
		if r.intent.Stage == backend.RequestStageMain && r.intent.Phase != "prompt_upgrade" {
			cur.Attempt--
		}
		s.state.Cursors[idx] = cur
	}
	r.dropRequestBacking()
	return nil
}

func (s *MemoryRoundStore) HandoffResponse(ctx context.Context, response MainResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.requests[response.ID]
	if r == nil || len(response.Raw) == 0 || len(response.FreezePayload) == 0 {
		return fmt.Errorf("invalid response handoff")
	}
	// Reserve froze this immutable payload; its existing digest is borrowed.
	fd := r.freezeIdentity
	b, err := backend.NewBuildBoundsFromContext(ctx, 0, 0)
	if err != nil {
		return err
	}
	digestCredit, err := b.OwnAllocation(64, 1)
	if err != nil {
		return err
	}
	rd := responseDigest(response.Raw)
	if r.response.ResponseDigest != "" {
		defer digestCredit.Release()
		if r.response.ResponseDigest != rd || r.response.FreezeDigest != fd {
			return fmt.Errorf("response identity reused")
		}
		return nil
	}
	if r.historyAdmission != nil {
		if err = backend.TransferProviderResponseToBuild(ctx, digestCredit, r.historyOwner); err != nil {
			rd = ""
			digestCredit.Release()
			return err
		}
	}
	response.DatabaseID = r.response.DatabaseID
	response.ID = r.intent.ID
	response.FreezePayload = r.intent.FreezePayload
	r.intent.FreezePayload = nil
	// Handoff transfers immutable raw/member backing to the durable in-memory
	// record. The producer subsequently keeps only its header.
	r.intent.PromotionMembers = nil
	response.FreezeBytes, response.ResponseBytes = int64(len(response.FreezePayload)), int64(len(response.Raw))
	response.MemberCount = len(response.Members)
	response.FreezeDigest, response.ResponseDigest = fd, rd
	response.Cursor, response.Phase = 0, "response_pending"
	r.response = response
	r.record = response.Record
	r.record.State = "completed"
	for _, m := range response.Members {
		cur := s.state.Cursors[m.Index]
		cur.State = "response_pending"
		s.state.Cursors[m.Index] = cur
	}
	return nil
}

func (s *MemoryRoundStore) ResponseHeaders(_ context.Context, after, limit int) ([]MainResponseHeader, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit < 1 || limit > 256 {
		return nil, after, fmt.Errorf("invalid response page")
	}
	var headers []MainResponseHeader
	for _, r := range s.requests {
		if r.response.DatabaseID > after && (r.response.Phase == "response_pending" || r.response.Phase == "glossary_processed") {
			headers = append(headers, r.response.MainResponseHeader)
		}
	}
	sort.Slice(headers, func(i, j int) bool { return headers[i].DatabaseID < headers[j].DatabaseID })
	if len(headers) > limit {
		headers = headers[:limit]
	}
	if len(headers) > 0 {
		after = headers[len(headers)-1].DatabaseID
	}
	return headers, after, nil
}

func (s *MemoryRoundStore) Response(_ context.Context, id string, freeze, raw, business int64) (MainResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.requests[id]
	if r == nil || r.response.FreezeBytes > freeze || r.response.ResponseBytes > raw || r.response.CorrectedBytes > business {
		return MainResponse{}, fmt.Errorf("saved response capacity conflict")
	}
	result := r.response
	// Promotion only reads these immutable buffers. Replacement under the store
	// lock leaves existing borrowers valid until their short build finishes.
	result.ReleaseOwned = r.business.borrow()
	return result, nil
}

func (s *MemoryRoundStore) PromoteMember(ctx context.Context, id string, cursor int, candidate *Candidate, next WorkCursor, failure string) error {
	var payload []byte
	var err error
	if candidate != nil {
		payload, err = candidate.Encode()
		if err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.requests[id]
	if r == nil || cursor < 0 || cursor >= len(r.response.Members) {
		return fmt.Errorf("invalid promotion")
	}
	m := r.response.Members[cursor]
	if r.response.Cursor > cursor {
		if candidate != nil && (r.status[cursor] != 1 || (!s.confirmed[candidate.ID] && s.headers[candidate.ID].Version <= candidate.Version && !bytes.Equal(s.candidates[candidate.ID], payload))) {
			return fmt.Errorf("promotion identity reused")
		}
		return nil
	}
	if r.response.Cursor != cursor {
		return fmt.Errorf("promotion cursor mismatch")
	}
	if candidate != nil {
		if candidate.ID != m.CandidateID || candidate.Index != m.Index || candidate.ParentRequestID != id {
			return fmt.Errorf("promotion member mismatch")
		}
	}
	if len(r.response.CorrectedIndex) > 0 {
		b, err := backend.NewBuildBoundsFromContext(ctx, int64(len(r.response.CorrectedTargets))+int64(len(r.response.CorrectedIndex)), 0)
		if err != nil {
			return err
		}
		defer b.CloseRetired()
		body, index, err := glossary.RemoveResponseTargetBounded(b, r.response.CorrectedTargets, r.response.CorrectedIndex, cursor)
		if err != nil {
			return err
		}
		allocation, err := b.RetainedAllocation(int64(cap(body)) + int64(cap(index)))
		if err != nil {
			return err
		}
		if err = allocation.DetachBuild(); err != nil {
			allocation.Release()
			return err
		}
		owner := "business:" + r.historyOwner + ":" + NewWorkID()
		content := &memoryResponseBusiness{refs: 1, body: body, index: index, allocation: allocation, owner: owner, bytes: int64(cap(body)) + int64(cap(index))}
		if r.historyAdmission != nil {
			if err = backend.TransferBuildAllocationToBuild(ctx, allocation, owner); err != nil {
				content.body, content.index = nil, nil
				body, index = nil, nil
				allocation.Release()
				return err
			}
			content.admission = r.historyAdmission
		}
		old := r.business
		r.business = content
		r.response.CorrectedTargets, r.response.CorrectedIndex = body, index
		r.response.CorrectedBytes = int64(len(body))
		old.release()
	}
	if candidate != nil {
		s.candidates[candidate.ID] = payload
		s.retainCandidateRuntime(ctx, candidate.ID)
		candidate.StoredBytes = int64(len(payload))
		s.headers[candidate.ID] = HeaderForCandidate(candidate)
		next.State = "candidate"
		r.status[cursor] = 1
	} else {
		r.status[cursor] = 2
	}
	s.state.Cursors[m.Index] = next
	r.response.Cursor++
	if r.response.Cursor == len(r.response.Members) {
		r.response.Phase = "promoted"
		r.response.Raw = nil
		r.response.FreezePayload = nil
		r.response.CorrectedTargets = nil
		r.response.CorrectedIndex = nil
		oldBusiness := r.business
		r.business = nil
		oldBusiness.release()
		r.response.Members = nil
		r.intent.Indices = nil
		r.intent.Members = nil
		r.status = nil
		r.dropRequestBacking()
		r.response.FreezeBytes, r.response.ResponseBytes, r.response.CorrectedBytes, r.response.BusinessBound = 0, 0, 0, 0
	}
	return nil
}

func (r *memoryResponse) dropRequestBacking() {
	// Identity reuse checks still need these two owned strings and the digest.
	r.intent = RequestIntent{ID: r.intent.ID, InputDigest: r.intent.InputDigest}
	r.status = nil
}
