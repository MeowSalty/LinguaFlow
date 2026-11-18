package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"sort"
	"strings"
	"unsafe"
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
	FrozenContext
	CaptureEpoch                                     int64
	FormatVersion                                    int
	ArchiveCursor                                    int
	ArchiveDigest                                    string
	InputMemberBytes, OutputMemberBytes, PublicBytes int64
	DatabaseID                                       int
	ID                                               string
	ResourceID                                       int
	RoundID                                          int
	SnapshotDigest                                   string
	FreezeDigest                                     string
	ResponseDigest                                   string
	FreezeBytes                                      int64
	ResponseBytes                                    int64
	CorrectedBytes                                   int64
	MetadataBytes                                    int64
	BusinessBound                                    int64
	MemberCount                                      int
	Cursor                                           int
	Phase                                            string
	Truncated                                        bool
}

type memoryResponseRecord struct {
	MainResponseHeader
	Receipt []byte
}
type memoryResponse struct {
	inputs                   map[int]InputMemberFrame
	outputs                  map[int]OutputMemberFrame
	capture                  *memberBacking
	memberBackings           map[int]*memberBacking
	outputBackings           map[int]*memberBacking
	archiveStaging           map[int]*memberBacking
	memberOutcomes           map[int]memoryMemberOutcome
	corrections              map[int]OutputMemberFrame
	correctionBackings       map[int]*memberBacking
	stagedCorrections        map[int]OutputMemberFrame
	stagedCorrectionBackings map[int]*memberBacking
	publicBacking            *memberBacking
	publicDigest             [sha256.Size]byte
	historyAdmission         *backend.RequestAdmission
	historyOwner             string
	retiredNext              *memoryResponse
	intent                   RequestIntent
	response                 memoryResponseRecord
	record                   RequestRecord
}

func responseDigest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func (s *MemoryRoundStore) Reserve(ctx context.Context, intent RequestIntent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("round store closed")
	}
	if len(intent.InputMembers) > 0 {
		return s.reserveMembers(ctx, intent)
	}
	if old := s.requests[intent.ID]; old != nil {
		if old.intent.InputDigest != intent.InputDigest {
			return fmt.Errorf("request identity reused")
		}
		return nil
	}
	admission, _ := backend.BuildOwnership(ctx)
	r := &memoryResponse{intent: intent, record: RequestRecord{State: "reserved"}, historyAdmission: admission, historyOwner: "history:" + s.identity + ":" + intent.ID}
	if admission != nil {
		history := backend.WithBuildOwnership(ctx, admission, r.historyOwner)
		history = backend.WithBuildReservation(history, func(n int64) error {
			return admission.ClaimBuildBounded(r.historyOwner, n, backend.BuildAllocationLimit(ctx))
		}, func(n int64) { _ = admission.ReleaseBuildBytes(r.historyOwner, n) })
		b, err := backend.NewBuildBoundsFromContext(history, 0, 0)
		if err != nil {
			return err
		}
		if err = b.ReserveAllocation(1, int64(unsafe.Sizeof(memoryResponse{}))); err != nil {
			return err
		}
	}
	s.nextRequest++
	r.response.DatabaseID = s.nextRequest
	r.response.ID = intent.ID
	s.requests[intent.ID] = r
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
		if record.State == "failed" || record.State == "completed" && r.response.Phase == "" {
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

func (s *MemoryRoundStore) ResponseHeaders(_ context.Context, after, limit int) ([]MainResponseHeader, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit < 1 || limit > 256 {
		return nil, after, fmt.Errorf("invalid response page")
	}
	var headers []MainResponseHeader
	for _, r := range s.requests {
		if r.response.DatabaseID > after && (r.response.Phase == "capture_pending" || r.response.Phase == "outputs_ready" || r.response.Phase == "business_ready") {
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

func (s *MemoryRoundStore) PromoteMember(ctx context.Context, key MemberKey, candidate *Candidate, next WorkCursor, failure string) error {
	var payload []byte
	if candidate != nil {
		var err error
		payload, err = candidate.Encode()
		if err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.memberLocked(key)
	if err != nil {
		return err
	}
	return s.promoteMemberLocked(ctx, r, key.Ordinal, candidate, next, failure, payload)
}
func (r *memoryResponse) dropRequestBacking() {
	for _, backings := range []map[int]*memberBacking{r.memberBackings, r.outputBackings, r.correctionBackings, r.stagedCorrectionBackings} {
		for id, b := range backings {
			b.drop()
			delete(backings, id)
		}
	}
	r.capture.drop()
	r.capture = nil
	r.inputs = nil
	r.outputs = nil
	r.corrections = nil
	r.stagedCorrections = nil
	r.intent = RequestIntent{ID: r.intent.ID, InputDigest: r.intent.InputDigest}
}
