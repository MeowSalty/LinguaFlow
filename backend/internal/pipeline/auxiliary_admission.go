package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/persistence"
)

// measureOnly stops the pure rendering pass before any prompt output backing.
var errMeasureOnly = errors.New("prompt measurement complete")

// preclaimAux executes the same durable reserve and joint admission as Translate,
// outside the short CPU permit. Its grant is consumed by the immediately following
// verified render/send; no second budget or dispatch authority is introduced.
func preclaimAux(ctx context.Context, remote backend.Backend, session *requestSession, req backend.Request, measured backend.PromptMeasure) error {
	return preclaimMeasured(ctx, remote, session, req, measured)
}
func preclaimMeasured(ctx context.Context, remote backend.Backend, session *requestSession, req backend.Request, measured backend.PromptMeasure) error {
	bound, ok := remote.(*requestBackend)
	if !ok {
		return nil
	}
	if err := requestBackendPolicy(ctx, bound.inner); err != nil {
		return err
	}
	intent := session.intent
	intent.ID = NewWorkID()
	intent.BackendID = bound.id
	prepared, keepPrepared := false, false
	defer func() {
		if prepared && !keepPrepared {
			bound.runtime.Admission.ReleasePrepared(intent.ID)
			bound.forgetPreparation(ctx)
		}
	}()
	wire, err := backend.MeasureRequestPrompt(bound.inner, req, measured)
	if err != nil {
		return err
	}
	input, err := boundedSum(wire, measured.SystemBytes, measured.UserBytes)
	if err != nil {
		return err
	}
	// The canonical member versions and exact measured input are frozen identity.
	// The actual prompt is rendered again only from those same versions.
	h := sha256.New()
	b, e := backend.NewBuildBounds(backend.RequestMetadataBytes, input)
	if e != nil {
		return e
	}
	frozen := struct {
		Members []RequestMember
		Measure backend.PromptMeasure
		Backend int
	}{intent.Members, measured, bound.id}
	if err = b.WriteJSON(h, frozen); err != nil {
		return err
	}
	intent.InputDigest = hex.EncodeToString(h.Sum(nil))
	admission := backend.RequestAdmissionIntent{RequestID: intent.ID, RoundIndex: intent.RoundIndex, ResourceID: intent.ResourceID, BackendID: bound.id, Stage: intent.Stage, InputBytes: input}
	for _, m := range intent.Members {
		admission.CandidateIDs = append(admission.CandidateIDs, m.CandidateID)
	}
	if intent.Stage == backend.RequestStageMain {
		progress := bound.runtime.Admission.RuntimeState().ProgressID == intent.ID || bound.runtime.Window.limits.Segments == 1 || bound.runtime.Window.limits.Bytes < bound.runtime.Window.limits.ItemBytes
		if progress {
			if err = bound.runtime.EnsureProgress(ctx, intent.ID, intent.ID, intent.ResourceID, intent.RoundIndex); err != nil {
				return err
			}
		}
		granted, e := bound.runtime.Admission.PrepareMain(intent.ID, intent.RoundIndex, intent.ResourceID, 0, input, 0, progress)
		if e != nil {
			return e
		}
		if !granted {
			return &WorkDeferred{Wait: backend.AdmissionWait{Reason: "prepared_bytes", CapacityChanged: bound.runtime.Admission.Changed()}}
		}
		prepared = true
	}
	ticket := preparationTicket(ctx)
	if !ticket.Active() {
		ticket, err = bound.runtime.Preparation.Try(ctx, admission, bound.runtime.Gate)
		if err != nil {
			return err
		}
	}
	// Determine the first stock exception before a Reserve debit can imply Q
	// in durable recovery headers. Cold/capacity-unready work stays unreserved.
	hint, hintErr := bound.runtime.Admission.ReadyHint(ctx, admission, bound.runtime.Gate)
	if errors.Is(hintErr, backend.ErrProgressRequired) && len(admission.CandidateIDs) == 1 {
		hintErr = bound.runtime.EnsureProgress(ctx, admission.CandidateIDs[0], "", intent.ResourceID, intent.RoundIndex)
		if hintErr == nil {
			hint, hintErr = bound.runtime.Admission.ReadyHint(ctx, admission, bound.runtime.Gate)
		}
	}
	if hintErr != nil {
		ticket.Release()
		if errors.Is(hintErr, backend.ErrProgressRequired) {
			return &WorkDeferred{Wait: backend.AdmissionWait{Reason: "progress_owner", CapacityChanged: bound.runtime.Admission.Changed()}, Yield: errors.Is(hintErr, backend.ErrEarlierScope)}
		}
		return hintErr
	}
	if hint.Reason != "" {
		ticket.Release()
		return &WorkDeferred{Wait: hint}
	}
	if session.store != nil {
		err = session.runtime.SaveNamed(ctx, persistence.Metadata{Operation: "reserve", Class: persistence.Normal, ResourceID: intent.ResourceID, RequestID: intent.ID}, func(saveCtx context.Context) error { return session.store.Reserve(saveCtx, intent) })
		if err != nil {
			ticket.Release()
			return err
		}
	}
	ticket.MarkReserved()
	permit, err := bound.acquire(ctx, admission, ticket)
	if errors.Is(err, backend.ErrProgressRequired) && len(admission.CandidateIDs) == 1 {
		err = bound.runtime.EnsureProgress(ctx, admission.CandidateIDs[0], "", intent.ResourceID, intent.RoundIndex)
		if err == nil {
			permit, err = bound.acquire(ctx, admission, ticket)
		}
	}
	if err != nil {
		if abort, ok := session.store.(RequestAborter); ok {
			abortErr := session.runtime.SaveNamed(session.accountingContext(ctx), persistence.Metadata{Operation: "abort", Class: persistence.Reliable, ResourceID: intent.ResourceID, RequestID: intent.ID}, func(saveCtx context.Context) error { return abort.AbortRequest(saveCtx, intent.ID) })
			if abortErr != nil {
				err = errors.Join(err, abortErr)
			}
		}
		ticket.Release()
		if errors.Is(err, backend.ErrProgressRequired) {
			return &WorkDeferred{Wait: backend.AdmissionWait{Reason: "progress_owner", CapacityChanged: bound.runtime.Admission.Changed()}, Yield: errors.Is(err, backend.ErrEarlierScope)}
		}
		return err
	}
	session.intent = intent
	session.preGrant = permit
	session.preWire = wire
	session.preInput = input
	session.preTicket = ticket
	keepPrepared = true
	return nil
}
