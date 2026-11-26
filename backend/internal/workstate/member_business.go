package workstate

import (
	"context"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workresponsemember"
)

type businessTransactionKey struct{}

// WithBusinessTransaction makes glossary and output publication one SQL commit.
func WithBusinessTransaction(ctx context.Context, tx *ent.Client) context.Context {
	return context.WithValue(ctx, businessTransactionKey{}, tx)
}
func memberClient(ctx context.Context, client *ent.Client) *ent.Client {
	if tx, ok := ctx.Value(businessTransactionKey{}).(*ent.Client); ok {
		return tx
	}
	return client
}
func memberTransaction(ctx context.Context, client *ent.Client, fn func(*ent.Client) error) error {
	if tx, ok := ctx.Value(businessTransactionKey{}).(*ent.Client); ok {
		return fn(tx)
	}
	return Transaction(ctx, client, fn)
}
func (s *Store) LoadResponsePublic(ctx context.Context, id string, limit int64) (OutputMember, error) {
	client := memberClient(ctx, s.client)
	r, e := memberHeaderQuery(client.WorkRequest.Query().Where(workrequest.IdentityEQ(id))).Only(ctx)
	if e != nil {
		return OutputMember{}, e
	}
	if r.PromotionState != "outputs_ready" && r.PromotionState != "business_ready" || r.PublicBytes > limit {
		return OutputMember{}, ErrManifest
	}
	field := workrequest.FieldResponseGlossary
	if r.PromotionState == "business_ready" {
		field = workrequest.FieldGlossaryReceipt
	}
	reservation, e := reserveMemberBytes(ctx, r.PublicBytes)
	if e != nil {
		return OutputMember{}, e
	}
	installed := false
	defer func() {
		if !installed {
			reservation.release()
		}
	}()
	row, e := client.WorkRequest.Query().Where(workrequest.IDEQ(r.ID), workrequest.PublicDigestEQ(r.PublicDigest), workrequest.PublicBytesEQ(r.PublicBytes), memberPayloadBound(field, r.PublicBytes)).Select(workrequest.FieldID, field).Only(ctx)
	if e != nil {
		return OutputMember{}, e
	}
	payload := row.ResponseGlossary
	if r.PromotionState == "business_ready" {
		payload = row.GlossaryReceipt
	}
	if int64(len(payload)) != r.PublicBytes || memberPayloadDigest(ctx, payload) != r.PublicDigest {
		return OutputMember{}, ErrManifest
	}
	if e = reservation.complete(payload); e != nil {
		return OutputMember{}, e
	}
	installed = true
	return OutputMember{Payload: payload, Bytes: int64(len(payload)), Release: reservation.release}, nil
}
func (s *Store) StageCorrectedMember(ctx context.Context, in OutputMember) error {
	return memberTransaction(ctx, s.client, func(tx *ent.Client) error {
		r, e := memberRequest(ctx, tx, in.Key)
		if e != nil {
			return e
		}
		if r.PromotionState != "outputs_ready" {
			return ErrManifest
		}
		row, e := tx.WorkResponseMember.Query().Where(workresponsemember.RequestIDEQ(r.ID), workresponsemember.OrdinalEQ(in.Key.Ordinal)).Select(workresponsemember.FieldID, workresponsemember.FieldCorrectedBytes, workresponsemember.FieldCorrectedDigest).Only(ctx)
		if e != nil {
			return e
		}
		if row.CorrectedDigest != "" {
			if row.CorrectedDigest == in.Digest {
				return nil
			}
			return ErrManifest
		}
		if in.Bytes <= 0 || int64(len(in.Payload)) != in.Bytes || memberPayloadDigest(ctx, in.Payload) != in.Digest {
			return ErrManifest
		}
		backend.ObserveMemberWork(ctx, backend.MemberWorkEvent{Phase: "member", Operation: "correction_write", Bytes: in.Bytes, Rows: 2})
		if e = tx.WorkResponseMember.UpdateOneID(row.ID).SetCorrectedPayload(in.Payload).SetCorrectedBytes(in.Bytes).SetCorrectedDigest(in.Digest).Select("id").Exec(ctx); e != nil {
			return e
		}
		return tx.WorkRequest.UpdateOneID(r.ID).AddOutputMemberBytes(in.Bytes).AddCorrectionBytes(in.Bytes).AddMemberStorageBytes(in.Bytes + int64(len(in.Digest))).Select("id").Exec(ctx)
	})
}
func (s *Store) AbortMemberBusiness(ctx context.Context, id string) error {
	return memberTransaction(ctx, s.client, func(tx *ent.Client) error {
		r, e := memberHeaderQuery(tx.WorkRequest.Query().Where(workrequest.IdentityEQ(id))).Only(ctx)
		if e != nil {
			return e
		}
		if r.PromotionState != "outputs_ready" {
			return nil
		}
		var retired, payload int64
		for start := 0; start < r.PromotionCount; start += 16 {
			rows, e := tx.WorkResponseMember.Query().Where(workresponsemember.RequestIDEQ(r.ID), workresponsemember.OrdinalGTE(start), workresponsemember.OrdinalLT(min(start+16, r.PromotionCount)), workresponsemember.CorrectedBytesGT(0)).Select(workresponsemember.FieldID, workresponsemember.FieldCorrectedBytes, workresponsemember.FieldCorrectedDigest).All(ctx)
			if e != nil {
				return e
			}
			for _, row := range rows {
				if e = tx.WorkResponseMember.UpdateOneID(row.ID).ClearCorrectedPayload().SetCorrectedBytes(0).SetCorrectedDigest("").Select("id").Exec(ctx); e != nil {
					return e
				}
				retired += row.CorrectedBytes + int64(len(row.CorrectedDigest))
				payload += row.CorrectedBytes
			}
		}
		if retired == 0 {
			return nil
		}
		// Payload and digest have distinct actual storage costs.
		return tx.WorkRequest.UpdateOneID(r.ID).AddOutputMemberBytes(-payload).AddCorrectionBytes(-payload).AddMemberStorageBytes(-retired).Select("id").Exec(ctx)
	})
}
