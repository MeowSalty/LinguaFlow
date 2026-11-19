package pipeline

import (
	"context"
	"errors"
	"fmt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
)

func (h MainResponseHeader) Facts() responsecontract.ResponseFacts {
	return responsecontract.ResponseFacts{CaptureBytes: h.CaptureBytes, InputMemberBytes: h.InputMemberBytes, OutputMemberBytes: h.OutputMemberBytes, ModelOutputBytes: h.ModelOutputBytes, CorrectionBytes: h.CorrectionBytes, PublicBytes: h.PublicBytes, PublicKind: h.PublicKind, MetadataBytes: h.MetadataBytes, BusinessReservation: h.BusinessBound}
}
func (s *MemoryRoundStore) PublicationDomain() responsecontract.PublicationDomain {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.publication.Authority == nil {
		s.publication = responsecontract.PublicationDomain{Medium: responsecontract.PublicationMemory, Authority: responsecontract.NewPublicationAuthority()}
	}
	return s.publication
}
func (s *MemoryRoundStore) BindPublicationDomain(domain responsecontract.PublicationDomain) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return backend.ErrAdmissionClosed
	}
	if domain.Medium != responsecontract.PublicationMemory || domain.Authority == nil {
		return responsecontract.ErrPublicationDomain
	}
	if len(s.requests) > 0 && s.publication.Authority != domain.Authority {
		return responsecontract.ErrPublicationDomain
	}
	s.publication = domain
	return nil
}
func (s *MemoryRoundStore) ResponseHeader(_ context.Context, id string) (MainResponseHeader, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.requests[id]
	if r == nil {
		return MainResponseHeader{}, fmt.Errorf("missing response")
	}
	return r.response.MainResponseHeader, nil
}
func (s *MemoryRoundStore) confirmMutation(ctx context.Context, id string, err error) (MainResponseHeader, error) {
	h, e := s.ResponseHeader(ctx, id)
	if err != nil {
		return h, err
	}
	return h, e
}
func (s *MemoryRoundStore) CaptureResponse(ctx context.Context, in CapturedResponse) (MainResponseHeader, error) {
	s.mutation.Lock()
	defer s.mutation.Unlock()
	return s.confirmMutation(ctx, in.ID, s.captureResponse(ctx, in))
}
func (s *MemoryRoundStore) PublishOutputs(ctx context.Context, in OutputPublication) (MainResponseHeader, error) {
	s.mutation.Lock()
	h, err := s.confirmMutation(ctx, in.Key.RequestID, s.publishOutputs(ctx, in))
	s.mutation.Unlock()
	if errors.Is(err, backend.ErrAdmissionClosed) {
		return h, err
	}
	return h, errors.Join(err, s.RetireResponse(ctx, in.Key.RequestID))
}
func (s *MemoryRoundStore) PublishBusiness(ctx context.Context, id string, out glossary.ResponseBusiness, install func()) (MainResponseHeader, error) {
	s.mutation.Lock()
	defer s.mutation.Unlock()
	return s.confirmMutation(ctx, id, s.publishBusiness(ctx, id, out, install))
}
func (s *MemoryRoundStore) PromoteMember(ctx context.Context, key MemberKey, c *Candidate, next WorkCursor, failure MemberOutcome) (MainResponseHeader, error) {
	s.mutation.Lock()
	h, err := s.confirmMutation(ctx, key.RequestID, s.promoteMember(ctx, key, c, next, failure))
	s.mutation.Unlock()
	if errors.Is(err, backend.ErrAdmissionClosed) {
		return h, err
	}
	return h, errors.Join(err, s.RetireResponse(ctx, key.RequestID))
}

// RetireResponse is called after the complete dictionary/store publication has
// exited. Its cleanup mutex is separate from both publication and state locks.
func (s *MemoryRoundStore) RetireResponse(_ context.Context, id string) error {
	s.retirement.Lock()
	defer s.retirement.Unlock()
	s.mu.Lock()
	r := s.requests[id]
	if r == nil {
		s.mu.Unlock()
		return nil
	}
	retired := memoryResponse{retiredBackings: r.retiredBackings}
	r.retiredBackings = nil
	s.mu.Unlock()
	err := retired.retryRetiredBackings()
	s.mu.Lock()
	if failed := retired.retiredBackings; failed != nil {
		tail := failed
		for tail.retiredNext != nil {
			tail = tail.retiredNext
		}
		tail.retiredNext = r.retiredBackings
		r.retiredBackings = failed
	}
	s.mu.Unlock()
	return err
}
