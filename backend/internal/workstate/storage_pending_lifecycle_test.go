package workstate

import (
	"errors"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

// Forward every receipt movement to the real Root. The counters distinguish
// committed replacement from a rollback even when net SQL bytes are equal.
type storagePendingCreditProbe struct {
	budget                        *StorageBudget
	metadata                      *backend.RequestAdmission
	remaining, consumed, refunded int64
	refundFailure                 error
}

func (p *storagePendingCreditProbe) StorageCredit(string) (int64, bool) { return p.remaining, true }
func (p *storagePendingCreditProbe) ConsumeStorage(_ string, n int64) error {
	if err := p.budget.Root.Move(p.budget.Scope, RuntimeIdentity(p.budget.Scope.JobID), StorageIdentity(p.budget.Scope.JobID), resourcebudget.Vector{LogicalStorageBytes: n}); err != nil {
		return err
	}
	p.remaining -= n
	p.consumed += n
	return nil
}
func (p *storagePendingCreditProbe) RefundStorage(_ string, n int64) error {
	if p.refundFailure != nil {
		err := p.refundFailure
		p.refundFailure = nil
		return err
	}
	if err := p.budget.Root.Move(p.budget.Scope, StorageIdentity(p.budget.Scope.JobID), RuntimeIdentity(p.budget.Scope.JobID), resourcebudget.Vector{LogicalStorageBytes: n}); err != nil {
		return err
	}
	p.remaining += n
	p.refunded += n
	return nil
}

func TestStoragePendingMetadataSuccessSurvivesLaterRefundFailure(t *testing.T) {
	ctx, client, store, scope, first, _ := memberDeltaFixture(t)
	r, capture, _ := integrityMemberPair(t, ctx, client, store, scope, first)
	b, owned := memberDeltaBudget(t, ctx, client, scope)
	a, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 1}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err = a.ConfigurePipelineBudget(backend.PipelineBudgetLimits{Segments: 2, Candidates: 1 << 20, Item: 1 << 20, Responses: 1 << 20, Handoff: 64 << 20}); err != nil {
		t.Fatal(err)
	}
	if err = a.AttachRootBudget(b.Root, b.Scope); err != nil {
		t.Fatal(err)
	}
	if _, err = a.PrepareMembers(r.ID, 2, scope.ResourceID, scope.RoundID, nil); err != nil {
		t.Fatal(err)
	}
	credit := &storagePendingCreditProbe{budget: b, metadata: a, remaining: 8 << 20}
	b.Credit = credit
	if err = store.ReserveRequest(owned, r); err != nil {
		t.Fatal(err)
	}
	if err = store.CaptureResponse(owned, capture); err != nil {
		t.Fatal(err)
	}
	before := memberDeltaAudit(t, ctx, b)
	row := client.WorkRequest.Query().OnlyX(ctx)
	refundFailure := errors.New("fixture refund unavailable")
	rollback := errors.New("fixture rollback after write")
	credit.refundFailure = refundFailure
	err = Transaction(owned, client, func(tx *ent.Client) error {
		if err := tx.WorkRequest.UpdateOneID(row.ID).SetResponseGlossary([]byte("temporary glossary")).Exec(owned); err != nil {
			return err
		}
		a.ReleaseBudgetEntry(r.ID)
		return rollback
	})
	if !errors.Is(err, rollback) || !errors.Is(err, refundFailure) {
		t.Fatalf("missing original rollback/refund faults: %v", err)
	}
	if !b.pending || len(b.touched) != 0 {
		t.Fatal("successful retired metadata was left pending for duplicate settlement")
	}
	if err := b.Settle(ctx); err != nil {
		t.Fatal(err)
	}
	if after := memberDeltaAudit(t, ctx, b); after != before {
		t.Fatal("refund retry lost original durable baseline")
	}
}

func TestStoragePendingKnownRollbackAfterReceiptRetirementReleasesSurplus(t *testing.T) {
	ctx, client, store, scope, first, faults := memberDeltaFixture(t)
	r, capture, _ := integrityMemberPair(t, ctx, client, store, scope, first)
	b, owned := memberDeltaBudget(t, ctx, client, scope)
	a, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 1}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err = a.ConfigurePipelineBudget(backend.PipelineBudgetLimits{Segments: 2, Candidates: 1 << 20, Item: 1 << 20, Responses: 1 << 20, Handoff: 64 << 20}); err != nil {
		t.Fatal(err)
	}
	if err = a.AttachRootBudget(b.Root, b.Scope); err != nil {
		t.Fatal(err)
	}
	if _, err = a.PrepareMembers(r.ID, 2, scope.ResourceID, 0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = a.PrepareMain(r.ID, 0, scope.ResourceID, 8<<10, 0, 0, false); err != nil {
		t.Fatal(err)
	}
	permit, _, err := a.TryAdmit(ctx, backend.RequestAdmissionIntent{RequestID: r.ID, Stage: backend.RequestStageMain, RoundIndex: 0, ResourceID: scope.ResourceID}, nil)
	if err != nil || permit == nil {
		t.Fatalf("real receipt grant: %v", err)
	}
	t.Cleanup(permit.Release)
	b.Credit = a
	if err = store.ReserveRequest(owned, r); err != nil {
		t.Fatal(err)
	}
	if err = store.CaptureResponse(owned, capture); err != nil {
		t.Fatal(err)
	}
	before := memberDeltaAudit(t, ctx, b)
	row := client.WorkRequest.Query().OnlyX(ctx)
	rollback := errors.New("fixture rollback with retired completion")
	err = Transaction(owned, client, func(tx *ent.Client) error {
		if err := tx.WorkRequest.UpdateOneID(row.ID).SetResponseGlossary([]byte("temporary glossary")).Exec(owned); err != nil {
			return err
		}
		permit.Release()
		faults.failLookup.Store(true)
		return rollback
	})
	if !errors.Is(err, rollback) || !errors.Is(err, memberDeltaLookupFailure) {
		t.Fatalf("missing rollback/fact fault: %v", err)
	}
	if _, live := a.StorageCredit(r.ID); live {
		t.Fatal("completion failed to retire original receipt")
	}
	faults.failLookup.Store(false)
	if err = b.Settle(ctx); err != nil {
		t.Fatal(err)
	}
	if after := memberDeltaAudit(t, ctx, b); after != before {
		t.Fatal("retired receipt blocked exact durable rollback settlement")
	}
	if _, live := a.StorageCredit(r.ID); live {
		t.Fatal("rollback recreated retired receipt")
	}
}
func (p *storagePendingCreditProbe) SetRequestMetadataBytes(id string, members int, n int64) error {
	if p.metadata != nil {
		return p.metadata.SetRequestMetadataBytes(id, members, n)
	}
	return nil
}
func (p *storagePendingCreditProbe) MarkRequestMetadataUncertain(id string) {
	if p.metadata != nil {
		p.metadata.MarkRequestMetadataUncertain(id)
	}
}

func TestStoragePendingNetZeroUnknownACKDoesNotRefundCommittedCredit(t *testing.T) {
	ctx, client, store, scope, first, faults := memberDeltaFixture(t)
	r, capture, _ := integrityMemberPair(t, ctx, client, store, scope, first)
	publication := integrityPublication(r, 0, 2, true)
	var written int64
	for _, m := range publication.Members {
		written += m.Bytes + int64(len(m.Digest))
	}
	capture.Payload = []byte(strings.Repeat("c", int(written)))
	b, owned := memberDeltaBudget(t, ctx, client, scope)
	credit := &storagePendingCreditProbe{budget: b, remaining: 8 << 20}
	b.Credit = credit
	if err := store.ReserveRequest(owned, r); err != nil {
		t.Fatal(err)
	}
	if err := store.CaptureResponse(owned, capture); err != nil {
		t.Fatal(err)
	}
	before := memberDeltaAudit(t, ctx, b)
	consumed, refunded := credit.consumed, credit.refunded
	faults.loseACK.Store(true)
	if err := store.PublishOutputs(owned, publication); err != nil {
		t.Fatal(err)
	}
	if after := memberDeltaAudit(t, ctx, b); after != before {
		t.Fatalf("net replacement bytes changed: before=%d after=%d", before, after)
	}
	if credit.consumed-consumed != written || credit.refunded != refunded {
		t.Fatalf("committed replacement refunded source credit: consumed=%d expected=%d refunded=%d", credit.consumed-consumed, written, credit.refunded-refunded)
	}
	row := client.WorkRequest.Query().OnlyX(ctx)
	if row.ArchiveCursor != 2 || row.PromotionState != "outputs_ready" || row.CaptureBytes != 0 || client.WorkResponseMember.Query().CountX(ctx) != 2 || client.UsageRecord.Query().CountX(ctx) != 1 {
		t.Fatal("unknown ACK did not preserve committed output identity")
	}
	if err := store.PublishOutputs(owned, publication); err != nil {
		t.Fatal(err)
	}
	if credit.consumed != consumed+written || credit.refunded != refunded {
		t.Fatal("late ACK duplicated receipt movement")
	}
}

func TestStoragePendingSettlesBeforeJoinedMetadataReset(t *testing.T) {
	ctx, client, store, scope, first, faults := memberDeltaFixture(t)
	r, capture, _ := integrityMemberPair(t, ctx, client, store, scope, first)
	b, owned := memberDeltaBudget(t, ctx, client, scope)
	admission, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 1}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admission.Close)
	if err = admission.ConfigurePipelineBudget(backend.PipelineBudgetLimits{Segments: 2, Candidates: 1 << 20, Item: 1 << 20, Responses: 1 << 20, Handoff: 64 << 20}); err != nil {
		t.Fatal(err)
	}
	if err = admission.AttachRootBudget(b.Root, b.Scope); err != nil {
		t.Fatal(err)
	}
	if _, err = admission.PrepareMembers(r.ID, 2, scope.ResourceID, scope.RoundID, nil); err != nil {
		t.Fatal(err)
	}
	b.Credit = &storagePendingCreditProbe{budget: b, metadata: admission, remaining: 8 << 20}
	if err = store.ReserveRequest(owned, r); err != nil {
		t.Fatal(err)
	}
	if err = store.CaptureResponse(owned, capture); err != nil {
		t.Fatal(err)
	}
	faults.loseACK.Store(true)
	faults.failAfterCommit.Store(true)
	if err = store.PublishOutputs(owned, integrityPublication(r, 0, 2, true)); err == nil {
		t.Fatal("missing authoritative query failure")
	}
	// The completion permit can retire before joined reconciliation. The
	// unsettled metadata fact must survive that source lifecycle transition.
	admission.ReleaseBudgetEntry(r.ID)
	faults.failLookup.Store(false)
	if err = b.Settle(ctx); err != nil {
		t.Fatal(err)
	}
	memberDeltaAudit(t, ctx, b)
	s, c, g, h, err := b.Source.Runtime(ctx, scope.JobID)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := b.Source.Metadata(ctx, scope.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if err = admission.ResetInventoryAfterJoin(s, c, g, h, metadata); err != nil {
		t.Fatal(err)
	}
	if err = b.Close(); err != nil {
		t.Fatal(err)
	}
	if _, live := b.Root.Usage(b.Scope, StorageIdentity(scope.JobID)); live {
		t.Fatal("joined storage writer remained attached after reset")
	}
}
