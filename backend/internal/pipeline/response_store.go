package pipeline

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

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
	BusinessBound  int64
	MemberCount    int
	Cursor         int
	Phase          string
	Truncated      bool
}

type MainResponse struct {
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
	intent         RequestIntent
	freezeIdentity string
	response       MainResponse
	record         RequestRecord
	status         []byte
}

func (s *MemoryRoundStore) SaveResponseBusiness(_ context.Context, id string, in glossary.ResponseBusiness) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.requests[id]
	if r == nil || r.response.Cursor != 0 || r.response.Phase != "response_pending" || int64(len(in.Body)) > r.response.BusinessBound || len(in.Index) != r.response.MemberCount*16 || len(in.Receipt) > 32<<10 {
		return fmt.Errorf("invalid response business handoff")
	}
	r.response.CorrectedTargets = bytes.Clone(in.Body)
	r.response.CorrectedIndex = bytes.Clone(in.Index)
	r.response.Receipt = bytes.Clone(in.Receipt)
	r.response.CorrectedBytes = int64(len(in.Body))
	r.response.Phase = "glossary_processed"
	return nil
}

func responseDigest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func (s *MemoryRoundStore) Reserve(_ context.Context, intent RequestIntent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old := s.requests[intent.ID]; old != nil {
		if old.intent.InputDigest != intent.InputDigest || old.freezeIdentity != responseDigest(intent.FreezePayload) {
			return fmt.Errorf("request identity reused")
		}
		return nil
	}
	s.nextRequest++
	intent.Indices = append([]int(nil), intent.Indices...)
	intent.FreezePayload = bytes.Clone(intent.FreezePayload)
	intent.PromotionMembers = append([]ResponseMember(nil), intent.PromotionMembers...)
	s.requests[intent.ID] = &memoryResponse{intent: intent, freezeIdentity: responseDigest(intent.FreezePayload), record: RequestRecord{State: "reserved"}, response: MainResponse{MainResponseHeader: MainResponseHeader{ID: intent.ID, DatabaseID: s.nextRequest}}}
	for _, idx := range intent.Indices {
		cur := s.state.Cursors[idx]
		if intent.Phase != "prompt_upgrade" && intent.Stage == backend.RequestStageMain {
			cur.Attempt++
		}
		s.state.Cursors[idx] = cur
	}
	return nil
}

func (s *MemoryRoundStore) Record(_ context.Context, id string, record RequestRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.requests[id]
	if r == nil {
		return fmt.Errorf("missing request")
	}
	if r.record.State != "completed" && r.record.State != "failed" && !(r.record.State == "received" && record.State == "sent") {
		r.record.State = record.State
		r.record.Error = record.Error
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
	return nil
}

func (s *MemoryRoundStore) HandoffResponse(_ context.Context, response MainResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.requests[response.ID]
	if r == nil || len(response.Raw) == 0 || len(response.FreezePayload) == 0 {
		return fmt.Errorf("invalid response handoff")
	}
	fd, rd := responseDigest(response.FreezePayload), responseDigest(response.Raw)
	if r.response.ResponseDigest != "" {
		if r.response.ResponseDigest != rd || r.response.FreezeDigest != fd {
			return fmt.Errorf("response identity reused")
		}
		return nil
	}
	response.DatabaseID = r.response.DatabaseID
	response.FreezePayload = r.intent.FreezePayload
	r.intent.FreezePayload = nil
	response.Raw = bytes.Clone(response.Raw)
	response.Members = append([]ResponseMember(nil), response.Members...)
	for i := range response.Members {
		response.Members[i].BaselineTarget = nil
	}
	r.intent.PromotionMembers = nil
	response.FreezeBytes, response.ResponseBytes = int64(len(response.FreezePayload)), int64(len(response.Raw))
	response.MemberCount = len(response.Members)
	response.FreezeDigest, response.ResponseDigest = fd, rd
	response.Cursor, response.Phase = 0, "response_pending"
	r.response = response
	r.status = make([]byte, len(response.Members))
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
	result.FreezePayload = bytes.Clone(result.FreezePayload)
	result.Raw = bytes.Clone(result.Raw)
	result.CorrectedTargets = bytes.Clone(result.CorrectedTargets)
	result.CorrectedIndex = bytes.Clone(result.CorrectedIndex)
	result.Receipt = bytes.Clone(result.Receipt)
	result.Members = append([]ResponseMember(nil), result.Members...)
	return result, nil
}

func (s *MemoryRoundStore) PromoteMember(_ context.Context, id string, cursor int, candidate *Candidate, next WorkCursor, failure string) error {
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
		s.candidates[candidate.ID] = payload
		candidate.StoredBytes = int64(len(payload))
		s.headers[candidate.ID] = HeaderForCandidate(candidate)
		next.State = "candidate"
		r.status[cursor] = 1
	} else {
		r.status[cursor] = 2
	}
	if len(r.response.CorrectedIndex) > 0 {
		body, index, err := glossary.RemoveResponseTarget(r.response.CorrectedTargets, r.response.CorrectedIndex, cursor)
		if err != nil {
			return err
		}
		r.response.CorrectedTargets, r.response.CorrectedIndex = body, index
		r.response.CorrectedBytes = int64(len(body))
	}
	s.state.Cursors[m.Index] = next
	r.response.Cursor++
	if r.response.Cursor == len(r.response.Members) {
		r.response.Phase = "promoted"
		r.response.Raw = nil
		r.response.FreezePayload = nil
		r.response.CorrectedTargets = nil
		r.response.CorrectedIndex = nil
		r.response.FreezeBytes, r.response.ResponseBytes, r.response.CorrectedBytes, r.response.BusinessBound = 0, 0, 0, 0
	}
	return nil
}
