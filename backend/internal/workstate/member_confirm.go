package workstate

import (
	"context"
	"errors"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
	"time"
)

// Confirmation reads only the bounded canonical header, including a resolved unknown ACK.
func (s *Store) ResponseHeader(ctx context.Context, id string) (ResponseHeader, error) {
	r, e := memberHeaderQuery(memberClient(ctx, s.client).WorkRequest.Query().Where(workrequest.IdentityEQ(id))).Only(ctx)
	if e != nil {
		return ResponseHeader{}, e
	}
	h := responseHeader(r)
	if !h.Phase.Valid() {
		return h, ErrManifest
	}
	return h, nil
}
func (s *Store) confirmed(ctx context.Context, id string, err error) (ResponseHeader, error) {
	if err != nil {
		return ResponseHeader{}, err
	}
	deadline := time.Now().Add(30 * time.Second)
	if earliest := persistence.Deadline(ctx); !earliest.IsZero() && earliest.Before(deadline) {
		deadline = earliest
	}
	ctx, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
	defer cancel()
	h, e := s.ResponseHeader(ctx, id)
	return h, errors.Join(err, e)
}
func (s *Store) CaptureResponse(ctx context.Context, in CapturedResponse) (ResponseHeader, error) {
	return s.confirmed(ctx, in.RequestID, s.captureResponse(ctx, in))
}
func (s *Store) PublishOutputs(ctx context.Context, in OutputPublication) (ResponseHeader, error) {
	return s.confirmed(ctx, in.Key.RequestID, s.publishOutputs(ctx, in))
}
func (s *Store) PromoteMember(ctx context.Context, in MemberPromotion) (ResponseHeader, error) {
	return s.confirmed(ctx, in.Key.RequestID, s.promoteMember(ctx, in))
}

func (h ResponseHeader) Facts() responsecontract.ResponseFacts {
	return responsecontract.ResponseFacts{CaptureBytes: h.CaptureBytes, InputMemberBytes: h.InputMemberBytes, OutputMemberBytes: h.OutputMemberBytes, ModelOutputBytes: h.ModelOutputBytes, CorrectionBytes: h.CorrectionBytes, PublicBytes: h.PublicBytes, PublicKind: h.PublicKind, MetadataBytes: h.MetadataBytes, BusinessReservation: h.BusinessBound}
}
