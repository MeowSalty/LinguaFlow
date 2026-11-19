package backend

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

func rejectionDetail(t *testing.T, err, cause error, branch string) RejectionEvidence {
	t.Helper()
	var diagnostic *RejectionError
	if !errors.Is(err, cause) || !errors.As(err, &diagnostic) || diagnostic.Evidence.Branch != branch {
		t.Fatalf("rejection cause/branch lost: %v", err)
	}
	return diagnostic.Evidence
}

func TestMemberResponseRejectionPreservesCauseAndCallerIdentity(t *testing.T) {
	if AnnotateRejection(nil, RejectionEvidence{}) != nil {
		t.Fatal("nil cause became an error")
	}
	cause := &resourcebudget.CapacityError{Stage: "root_move", Dimension: "payload_bytes", Owner: "actor:1/source", Used: 100, Requested: 3, Limit: 102}
	err := AnnotateRejection(cause, RejectionEvidence{Branch: "atomic_probe", Operation: "move", SourceOwner: "source"})
	err = AnnotateRejection(err, RejectionEvidence{Branch: "promotion", Operation: "caller", Phase: "before_save", StoreKind: "memory", RequestID: "stable", MemberKnown: true, Ordinal: 0, Cursor: 0, EpochKnown: true, Epoch: 0})
	d := rejectionDetail(t, err, resourcebudget.ErrCapacity, "atomic_probe")
	var got *resourcebudget.CapacityError
	if !errors.As(err, &got) || got != cause || d.Operation != "move" || d.Phase != "before_save" || !d.MemberKnown || d.Ordinal != 0 || !d.EpochKnown || !d.QuotaKnown || d.Used != 100 || d.Requested != 3 || d.Limit != 102 {
		t.Fatalf("atomic evidence or caller identity replaced: %+v", d)
	}
	if !strings.Contains(err.Error(), "ordinal=0 cursor=0") || !strings.Contains(err.Error(), "epoch=0") {
		t.Fatal("known zero identity omitted")
	}
	plain := AnnotateRejection(ErrPipelineCapacity, RejectionEvidence{Branch: "identity_mismatch"})
	d = rejectionDetail(t, plain, ErrPipelineCapacity, "identity_mismatch")
	if d.QuotaKnown || strings.Contains(plain.Error(), "limit=0") {
		t.Fatal("identity failure invented quota evidence")
	}
}

func TestMemberResponseProviderCreditRejectionRetainsAtomicEvidence(t *testing.T) {
	a, root, ctx, intent := paidCapacityFixture(t, RequestStageMain)
	p, _ := ctx.Value(providerAllocationKey{}).(*providerAllocation)
	before := ProviderResponseSnapshot(ctx)
	rootBefore := root.Snapshot(1).Instance
	a.mu.Lock()
	r := a.budget.requests["paid"]
	r.responseCredit--
	a.budget.requests["paid"] = r
	a.mu.Unlock()
	err := p.reserve(1)
	d := rejectionDetail(t, err, ErrPipelineCapacity, "provider_credit_mismatch")
	// Restore the ledger before inspecting the error. Evidence must still show
	// the exact rejected values, rather than a post-rejection Snapshot.
	a.mu.Lock()
	r.responseCredit++
	a.budget.requests["paid"] = r
	a.mu.Unlock()
	if !d.MeterCreditKnown || !d.LedgerCreditKnown || d.MeterPaid != 1024 || d.LedgerPaid != 1023 || d.MeterID == "" || d.SourceLedger == "" || d.TargetLedger == "" || d.QuotaKnown {
		t.Fatalf("credit mismatch evidence incomplete: %+v", d)
	}
	if ProviderResponseSnapshot(ctx) != before || root.Snapshot(1).Instance != rootBefore {
		t.Fatal("rejection changed meter or root")
	}
	finishPaidFixture(t, a, ctx, intent)
}

func TestMemberResponsePromotionRejectionDistinguishesSourceBranches(t *testing.T) {
	for _, kind := range []string{"request", "closed", "restored"} {
		t.Run(kind, func(t *testing.T) {
			a, root, ctx, intent := promotionPaidFixture(t)
			owner, err := OwnProviderResponseAllocation(ctx, 939, 1)
			if err != nil {
				t.Fatal(err)
			}
			before, rootBefore := ProviderResponseSnapshot(ctx), root.Snapshot(1).Instance
			callCtx, branch := ctx, "promotion_provider_identity_mismatch"
			p := owner.bounds.meterSource
			if kind == "request" {
				callCtx = WithProviderResponseOwnership(ctx, a, "other", RequestStageMain)
			}
			if kind == "closed" {
				p.mu.Lock()
				p.closed = true
				p.mu.Unlock()
			}
			if kind == "restored" {
				a.mu.Lock()
				r := a.budget.requests["paid"]
				r.restored = true
				a.budget.requests["paid"] = r
				a.mu.Unlock()
				branch = "promotion_live_response_invalid"
			}
			ok, err := a.BeginPromotionWithAllocation(callCtx, "paid", "candidate", 939, false, owner)
			d := rejectionDetail(t, err, ErrPipelineCapacity, branch)
			if ok || d.MeterID == "" || !d.BoundsBytesKnown || d.BoundsOwnedBytes != 939 || d.QuotaKnown {
				t.Fatalf("source identity rejection lost receipt facts: %+v", d)
			}
			if kind == "request" && (!d.ProviderIdentityKnown || d.SourceRequestID != "paid" || d.CallerRequestID != "other" || d.SourceStage != RequestStageMain || d.CallerLedger == "") {
				t.Fatalf("caller/source identity difference missing: %+v", d)
			}
			if kind == "closed" {
				if !d.MeterStateKnown || !d.MeterClosed {
					t.Fatal("closed meter not identified")
				}
				p.mu.Lock()
				p.closed = false
				p.mu.Unlock()
			}
			if kind == "restored" {
				a.mu.Lock()
				r := a.budget.requests["paid"]
				r.restored = false
				a.budget.requests["paid"] = r
				a.mu.Unlock()
			}
			if owner.bounds == nil || ProviderResponseSnapshot(ctx) != before || root.Snapshot(1).Instance != rootBefore {
				t.Fatal("rejected source move consumed credit or receipt")
			}
			owner.Release()
			finishPaidFixture(t, a, ctx, intent)
			a.ReleaseBudgetEntry("paid")
		})
	}
}

func TestMemberResponseRestoreDuplicateIdentityDoesNotConsumeStock(t *testing.T) {
	a, _ := buildAdmission(t, 1<<20)
	if err := a.ConfigurePipelineBudget(PipelineBudgetLimits{Segments: 4, Candidates: 4096, Item: 1024, Responses: 1024, Handoff: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	if err := a.RestoreInventory(2, 200, 0, 0); err != nil {
		t.Fatal(err)
	}
	if moved, err := a.RestoreBudgetEntryWithTransfer("candidate", PipelineBudgetEntry{Count: 1, Bytes: 100}); err != nil || moved != 1 {
		t.Fatalf("initial restore failed: %d %v", moved, err)
	}
	before := a.PipelineSnapshot()
	moved, err := a.RestoreBudgetEntryWithTransfer("candidate", PipelineBudgetEntry{Count: 1, Bytes: 101})
	d := rejectionDetail(t, err, ErrPipelineCapacity, "restore_inventory_identity_mismatch")
	if moved != 0 || !d.InventoryKnown || d.InventoryBytes != 100 || d.RequestedBytes != 101 || d.QuotaKnown || !reflect.DeepEqual(a.PipelineSnapshot(), before) {
		t.Fatalf("duplicate restore altered stock: %+v", d)
	}
}

func TestMemberResponseRejectionSnapshotHasNoLiveOwnerReference(t *testing.T) {
	a, _, ctx, intent := paidCapacityFixture(t, RequestStageMain)
	evidence := RejectionEvidenceFromContext(ctx)
	finishPaidFixture(t, a, ctx, intent)
	err := AnnotateRejection(ErrPipelineCapacity, mergeRejection(RejectionEvidence{Branch: "saved_snapshot"}, evidence))
	d := rejectionDetail(t, err, ErrPipelineCapacity, "saved_snapshot")
	if !d.MeterStateKnown || d.MeterClosed || !d.LedgerCreditKnown || d.LedgerPaid != 1024 {
		t.Fatalf("captured evidence followed the closed owner: %+v", d)
	}
}

func TestMemberResponseRejectionAllocationQuotaPreservesTypedCause(t *testing.T) {
	cause := &AllocationCapacityError{Used: 0, Requested: 2, Limit: 1}
	err := AnnotateRejection(cause, RejectionEvidence{Branch: "allocation_limit"})
	d := rejectionDetail(t, err, ErrBuildCapacity, "allocation_limit")
	var got *AllocationCapacityError
	if !errors.As(err, &got) || got != cause || !d.QuotaKnown || d.Used != 0 || d.Requested != 2 || d.Limit != 1 {
		t.Fatalf("measured zero use was confused with missing evidence: %+v", d)
	}
}
