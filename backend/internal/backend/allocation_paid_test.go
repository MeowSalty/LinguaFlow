package backend

import (
	"context"
	"errors"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
	"testing"
)

func paidCapacityFixture(t *testing.T, stage RequestStage, inputs ...int64) (*RequestAdmission, *resourcebudget.RootBudget, context.Context, RequestAdmissionIntent) {
	t.Helper()
	const response = int64(1024)
	wire, totalInput := int64(0), int64(0)
	if len(inputs) > 0 {
		wire, totalInput = inputs[0], inputs[0]
	}
	if len(inputs) > 1 {
		totalInput += inputs[1]
	}
	a, root := buildAdmission(t, response+RequestMetadataBytes+totalInput)
	if err := a.ConfigurePipelineBudget(PipelineBudgetLimits{Segments: 1, Candidates: 1, Item: 1, Responses: response, Handoff: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	intent := RequestAdmissionIntent{RequestID: "paid", Stage: stage}
	a.mu.Lock()
	if stage == RequestStageMain {
		a.budget.requests["paid"] = requestBudget{granted: true, responseOwned: true, raw: response, responseCredit: response, known: totalInput, wire: totalInput}
		a.budget.mainOwners[0] = 1
	} else {
		a.budget.auxOwners, a.budget.auxResponse = 1, response
		intent.InputBytes = totalInput
		a.budget.auxInput = totalInput
		a.candidateReads = map[string]*candidateReadCredit{"paid": {remaining: response, inputRemaining: totalInput}}
	}
	err := a.syncRootLocked(0)
	a.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithBuildOwnership(context.Background(), a, "response:paid")
	if len(inputs) > 0 {
		ctx = WithRequestCredits(ctx, wire, response)
	}
	ctx = WithBuildReservation(ctx, func(n int64) error { return a.ClaimBuild("response:paid", n) }, func(n int64) {
		if e := a.ReleaseBuildBytes("response:paid", n); e != nil {
			t.Error(e)
		}
	})
	ctx = WithProviderResponseOwnership(ctx, a, "paid", stage)
	ctx, err = WithProviderResponseAllocation(ctx, response)
	if err != nil {
		t.Fatal(err)
	}
	return a, root, ctx, intent
}
func finishPaidFixture(t *testing.T, a *RequestAdmission, ctx context.Context, intent RequestAdmissionIntent) {
	t.Helper()
	if err := CloseProviderResponseAllocation(ctx); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.releaseRequestBudgetLocked(intent)
	err := a.syncRootLocked(0)
	a.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
}

func TestRequestPermitCompletionKeepsActualResponseOwnerUntilRelease(t *testing.T) {
	for _, stage := range []RequestStage{RequestStageMain, RequestStageAlignment} {
		t.Run(string(stage), func(t *testing.T) {
			a, root, ctx, intent := paidCapacityFixture(t, stage)
			a.mu.Lock()
			a.completions = 1
			if stage == RequestStageMain {
				a.main[0], a.mainResults[0] = 1, 1
			} else {
				a.alignment, a.auxResults = 1, 1
			}
			a.mu.Unlock()
			permit := &RequestPermit{admission: a, intent: intent, shared: stage == RequestStageMain}
			defer permit.Release()
			b, err := NewBuildBoundsFromContext(ctx, 1024, 1024)
			if err != nil {
				t.Fatal(err)
			}
			defer b.CloseRetired()
			owner, err := b.OwnAllocation(16, 1)
			if err != nil {
				t.Fatal(err)
			}
			backing := make([]byte, 16)
			backing[0] = 42
			permit.RequestDone()
			before := root.Snapshot(1).Instance
			permit.Complete()
			permit.Complete()
			if a.Snapshot().PendingResults != 0 || root.Snapshot(1).Instance != before || ProviderResponseSnapshot(ctx).LiveBytes != 16 || backing[0] != 42 {
				t.Fatal("semantic completion released the original response source")
			}
			if err := CloseProviderResponseAllocation(ctx); err == nil {
				t.Fatal("completed response closed with an actual borrower")
			}
			backing = nil
			owner.Release()
			b.CloseRetired()
			if err := CloseProviderResponseAllocation(ctx); err != nil {
				t.Fatal(err)
			}
			permit.Release()
			permit.Release()
			permit.Complete()
			if a.Snapshot().PendingResults != 0 || root.Snapshot(1).Instance != (resourcebudget.Vector{}) {
				t.Fatal("physical response ownership did not retire exactly once")
			}
		})
	}
}
func TestPaidCapacityTransferKeepsFullRootAndTargetLifetime(t *testing.T) {
	for _, stage := range []RequestStage{RequestStageMain, RequestStageAlignment} {
		for _, lease := range []bool{false, true} {
			t.Run(string(stage)+map[bool]string{false: "/build", true: "/lease"}[lease], func(t *testing.T) {
				a, root, ctx, intent := paidCapacityFixture(t, stage)
				b, err := NewBuildBoundsFromContext(ctx, 1024, 1024)
				if err != nil {
					t.Fatal(err)
				}
				if err = b.ReserveAllocation(512, 1); err != nil {
					t.Fatal(err)
				}
				backing := make([]byte, 512)
				receipt, err := b.RetainedAllocation(int64(cap(backing)))
				if err != nil {
					t.Fatal(err)
				}
				before := root.Snapshot(1).Actor.PayloadBytes
				if lease {
					err = TransferProviderResponseToLease(ctx, receipt, root, a.rootScope, "retained")
				} else {
					err = TransferProviderResponseToBuild(ctx, receipt, "retained")
				}
				if err != nil || root.Snapshot(1).Actor.PayloadBytes != before {
					t.Fatal(err, root.Snapshot(1))
				}
				receipt.Release()
				b.CloseRetired()
				finishPaidFixture(t, a, ctx, intent)
				if root.Snapshot(1).Actor.PayloadBytes != 512 {
					t.Fatal("response close refunded live target", root.Snapshot(1))
				}
				backing = nil
				if lease {
					err = root.Transfer(a.rootScope, "retained", resourcebudget.Vector{})
				} else {
					err = a.ReleaseBuild("retained")
				}
				if err != nil || root.Snapshot(1).Actor.PayloadBytes != 0 {
					t.Fatal(err, root.Snapshot(1))
				}
			})
		}
	}
}
func TestPaidCapacityTransferRollbackAndStaleSource(t *testing.T) {
	a, root, ctx, intent := paidCapacityFixture(t, RequestStageMain)
	receipt, err := OwnProviderResponseAllocation(ctx, 100, 1)
	if err != nil {
		t.Fatal(err)
	}
	before := root.Snapshot(1).Actor.PayloadBytes
	if err = TransferProviderResponseToLease(ctx, receipt, root, a.rootScope, a.rootID); !errors.Is(err, resourcebudget.ErrIdentity) {
		t.Fatal(err)
	}
	if root.Snapshot(1).Actor.PayloadBytes != before || ProviderResponseSnapshot(ctx).LiveBytes != 100 {
		t.Fatal("failed transfer changed credit")
	}
	other := resourcebudget.Scope{ActorID: 1, CallID: "other"}
	if err = root.Claim(other, "collision", resourcebudget.Vector{BuildWorkers: 1}); err != nil {
		t.Fatal(err)
	}
	if err = TransferProviderResponseToLease(ctx, receipt, root, a.rootScope, "collision"); !errors.Is(err, resourcebudget.ErrIdentity) {
		t.Fatal(err)
	}
	if root.Snapshot(1).Actor.PayloadBytes != before || ProviderResponseSnapshot(ctx).LiveBytes != 100 {
		t.Fatal("Root move failure changed credit")
	}
	if err = root.Transfer(other, "collision", resourcebudget.Vector{}); err != nil {
		t.Fatal(err)
	}
	receipt.Release()
	receipt.Release()
	finishPaidFixture(t, a, ctx, intent)
	if root.Snapshot(1).Actor.PayloadBytes != 0 {
		t.Fatal(root.Snapshot(1))
	}
	if _, err = OwnProviderResponseAllocation(ctx, 1, 1); !errors.Is(err, ErrBuildCapacity) {
		t.Fatal("terminal paid credit revived", err)
	}
	if _, err = OwnProviderResponseAllocation(ctx, 0, 1); !errors.Is(err, ErrBuildCapacity) {
		t.Fatal("terminal paid meter accepted a zero-byte owner", err)
	}
}
func TestPaidCapacityReceiptChecksActualMeterSource(t *testing.T) {
	a, _, ctx, intent := paidCapacityFixture(t, RequestStageMain)
	// A replacement callback does not inherit paid ownership merely because a
	// provider key remains in context; this receipt belongs to another meter.
	var live int64
	other := WithBuildReservation(ctx, func(n int64) error { live += n; return nil }, func(n int64) { live -= n })
	b, _ := NewBuildBoundsFromContext(other, 1024, 1024)
	receipt, err := b.OwnAllocation(10, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = TransferProviderResponseToLease(ctx, receipt, a.root, a.rootScope, "other"); !errors.Is(err, ErrBuildCapacity) {
		t.Fatal(err)
	}
	receipt.Release()
	if live != 0 || ProviderResponseSnapshot(ctx).LiveBytes != 0 {
		t.Fatal(live, ProviderResponseSnapshot(ctx))
	}
	rebound, err := WithProviderResponseAllocation(other, 1024)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = NewBuildBoundsFromContext(rebound, 1024, 1024)
	receipt, err = b.OwnAllocation(10, 1)
	if err != nil || live != 0 || ProviderResponseSnapshot(ctx).LiveBytes != 10 {
		t.Fatal(err, live, ProviderResponseSnapshot(ctx))
	}
	receipt.Release()
	b.CloseRetired()
	finishPaidFixture(t, a, ctx, intent)
}

func TestPaidCapacityActiveBuildSourceChecksOriginalLane(t *testing.T) {
	if HasActiveBuildAllocationSource(nil) || HasActiveBuildAllocationSource(context.Background()) {
		t.Fatal("missing source appeared admitted")
	}
	standalone, err := WithProviderResponseAllocation(context.Background(), 1024)
	if err != nil || HasActiveBuildAllocationSource(standalone) {
		t.Fatal("standalone acquired a transferable paid source", err)
	}
	for _, stage := range []RequestStage{RequestStageMain, RequestStageAlignment} {
		t.Run(string(stage), func(t *testing.T) {
			a, root, ctx, intent := paidCapacityFixture(t, stage)
			before, meterBefore := root.Snapshot(1).Actor, ProviderResponseSnapshot(ctx)
			if !HasActiveBuildAllocationSource(ctx) || root.Snapshot(1).Actor != before || ProviderResponseSnapshot(ctx) != meterBefore {
				t.Fatal("source query changed accounting or lost active lane")
			}
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			for _, invalid := range []context.Context{
				WithBuildReservation(ctx, func(int64) error { return nil }, func(int64) {}),
				WithBuildAllocation(ctx, 0, func(int64) error { return nil }, func(int64) {}),
				WithBuildOwnership(ctx, a, "another-source"),
				WithBuildOwnership(ctx, nil, "response:paid"),
				WithProviderResponseOwnership(ctx, a, "another-request", stage),
				cancelled,
			} {
				if HasActiveBuildAllocationSource(invalid) {
					t.Fatal("stale context key impersonated the original source")
				}
			}
			a.mu.Lock()
			if stage == RequestStageMain {
				r := a.budget.requests["paid"]
				r.granted = false
				a.budget.requests["paid"] = r
			} else {
				delete(a.candidateReads, "paid")
			}
			a.mu.Unlock()
			if HasActiveBuildAllocationSource(ctx) {
				t.Fatal("inactive protocol lane remained transferable")
			}
			a.mu.Lock()
			if stage == RequestStageMain {
				r := a.budget.requests["paid"]
				r.granted = true
				a.budget.requests["paid"] = r
			} else {
				a.candidateReads["paid"] = &candidateReadCredit{remaining: 1024}
			}
			a.mu.Unlock()
			a.mu.Lock()
			a.closed = true
			a.mu.Unlock()
			if HasActiveBuildAllocationSource(ctx) {
				t.Fatal("closed admission remained transferable")
			}
			a.mu.Lock()
			a.closed = false
			a.mu.Unlock()
			if err := CloseProviderResponseAllocation(ctx); err != nil || HasActiveBuildAllocationSource(ctx) {
				t.Fatal("closed source remained transferable", err)
			}
			finishPaidFixture(t, a, ctx, intent)
		})
	}
}

func TestPaidCapacityActiveBuildSourceWithoutRootTransfersInventory(t *testing.T) {
	a, err := NewRequestAdmission(nil, RequestAdmissionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err = a.ConfigurePipelineBudget(PipelineBudgetLimits{Segments: 1, Candidates: 1, Item: 1, Responses: 1024, Handoff: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.budget.requests["local"] = requestBudget{granted: true, responseOwned: true, raw: 1024, responseCredit: 1024}
	a.budget.mainOwners[0] = 1
	a.mu.Unlock()
	ctx := WithBuildOwnership(context.Background(), a, "response:local")
	ctx = WithBuildReservation(ctx, func(n int64) error { return a.ClaimBuild("response:local", n) }, func(n int64) {
		if err := a.ReleaseBuildBytes("response:local", n); err != nil {
			t.Error(err)
		}
	})
	ctx = WithProviderResponseOwnership(ctx, a, "local", RequestStageMain)
	ctx, err = WithProviderResponseAllocation(ctx, 1024)
	if err != nil || !HasActiveBuildAllocationSource(ctx) {
		t.Fatal("valid admitted inventory required an artificial Root", err)
	}
	owner, err := OwnProviderResponseAllocation(ctx, 1024, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = TransferProviderResponseToBuild(ctx, owner, "local-result"); err != nil || a.BuildBytes("local-result") != 1024 || !HasActiveBuildAllocationSource(ctx) {
		t.Fatal("source query did not correspond to a real transfer", err)
	}
	if err = owner.ReturnToBuild(); !errors.Is(err, ErrBuildCapacity) || a.BuildBytes("local-result") != 1024 {
		t.Fatal("successful transfer restored the invalidated source receipt", err)
	}
	owner.Release()
	owner, err = OwnProviderResponseAllocation(ctx, 1, 1)
	if err != nil {
		t.Fatal("active zero-credit source rejected actual capacity", err)
	}
	if err = TransferProviderResponseToBuild(ctx, owner, "local-result"); err != nil || a.BuildBytes("local-result") != 1025 {
		t.Fatal("zero-credit source failed its formal transfer", err)
	}
	owner.Release()
	finishPaidFixture(t, a, ctx, RequestAdmissionIntent{RequestID: "local", Stage: RequestStageMain})
	if err = a.ReleaseBuild("local-result"); err != nil {
		t.Fatal(err)
	}
}
func TestProviderActualPartialOutputSurvivesCapacityFailure(t *testing.T) {
	ctx, err := WithProviderResponseAllocation(WithBuildAllocation(context.Background(), 64, nil, nil), 1024)
	if err != nil {
		t.Fatal(err)
	}
	text, _ := NewResponseTextObserved(ctx, 1024, "partial")
	if err = text.Append("known"); err != nil {
		t.Fatal(err)
	}
	if err = text.Append(string(make([]byte, 100))); !errors.Is(err, ErrBuildCapacity) {
		t.Fatal(err)
	}
	resp := ResponseWithPartialText(text, Usage{PromptTokens: 7})
	text.ClearObserved()
	if resp.Text != "known" || resp.Usage.PromptTokens != 7 || ProviderResponseSnapshot(ctx).LiveBytes != 64 {
		t.Fatal(resp, ProviderResponseSnapshot(ctx))
	}
	resp.Text = ""
	ReleaseProviderResponseOutput(ctx)
	if err = CloseProviderResponseAllocation(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPaidCapacityDecodedStringKeepsOriginalRetainedReceipt(t *testing.T) {
	a, root, ctx, intent := paidCapacityFixture(t, RequestStageMain)
	b, err := NewBuildBoundsFromContext(ctx, 1024, 1024)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Text string `json:"text"`
	}
	if err = b.DecodeJSON([]byte("{\"text\":\"retained\"}"), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Text != "retained" {
		t.Fatal("decoded field missing", decoded.Text)
	}
	n := int64(len(decoded.Text))
	receipt, err := b.RetainedAllocation(n)
	if err != nil {
		t.Fatal(err, b.Snapshot())
	}
	before := root.Snapshot(1).Actor.PayloadBytes
	if err = TransferProviderResponseToBuild(ctx, receipt, "decoded"); err != nil || root.Snapshot(1).Actor.PayloadBytes != before {
		t.Fatal(err, root.Snapshot(1))
	}
	b.CloseRetired()
	finishPaidFixture(t, a, ctx, intent)
	if decoded.Text != "retained" || root.Snapshot(1).Actor.PayloadBytes != n {
		t.Fatal(decoded, root.Snapshot(1))
	}
	decoded.Text = ""
	if err = a.ReleaseBuild("decoded"); err != nil || root.Snapshot(1).Actor.PayloadBytes != 0 {
		t.Fatal(err, root.Snapshot(1))
	}
}

func TestPaidCapacityHandoffRetainsCreditUntilConsumersFinish(t *testing.T) {
	a, root, ctx, intent := paidCapacityFixture(t, RequestStageMain)
	raw, err := OwnProviderResponseAllocation(ctx, 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	rawBacking := make([]byte, 10)
	before := root.Snapshot(1).Actor.PayloadBytes
	if err = ApplyProviderResponseHandoff(ctx, "paid", int64(len(rawBacking))); err != nil || root.Snapshot(1).Actor.PayloadBytes != before {
		t.Fatal("handoff retired paid credit before its consumers", err, root.Snapshot(1))
	}
	b, err := NewBuildBoundsFromContext(ctx, 1024, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.ReserveAllocation(900, 1); err != nil {
		t.Fatal("later actual builder could not consume the live paid R", err)
	}
	retainedBacking := make([]byte, 900)
	retained, err := b.RetainedAllocation(int64(cap(retainedBacking)))
	if err != nil {
		t.Fatal(err)
	}
	if err = TransferProviderResponseToBuild(ctx, retained, "final-result"); err != nil || root.Snapshot(1).Actor.PayloadBytes != before {
		t.Fatal("consumer transfer changed total paid capacity", err, root.Snapshot(1))
	}
	b.CloseRetired()
	rawBacking = nil
	raw.Release()
	finishPaidFixture(t, a, ctx, intent)
	retained.Release()
	if root.Snapshot(1).Actor.PayloadBytes != 900 {
		t.Fatal("session close refunded the live final result", root.Snapshot(1))
	}
	retainedBacking = nil
	if err = a.ReleaseBuild("final-result"); err != nil || root.Snapshot(1).Actor.PayloadBytes != 0 {
		t.Fatal(err, root.Snapshot(1))
	}
}

func TestPaidCapacityCanonicalFactsPreserveLiveResponseCredit(t *testing.T) {
	a, root, ctx, intent := paidCapacityFixture(t, RequestStageMain)
	a.mu.Lock()
	a.config.MainConcurrency[0] = 1
	a.mu.Unlock()
	raw, err := OwnProviderResponseAllocation(ctx, 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = ApplyProviderResponseHandoff(ctx, intent.RequestID, 10); err != nil {
		t.Fatal(err)
	}
	before := RejectionEvidenceFromContext(ctx)
	facts := responsecontract.ResponseFacts{CaptureBytes: 10, PublicKind: responsecontract.PublicNone}
	if err = a.RestoreResponseOwnership(intent.RequestID, intent.RoundIndex, intent.ResourceID, facts, responsecontract.CapturePending, true); err != nil {
		t.Fatal(err)
	}
	later, err := OwnProviderResponseAllocation(ctx, 900, 1)
	if err != nil {
		t.Fatal("canonical archive retired live paid R", err)
	}
	after := RejectionEvidenceFromContext(ctx)
	if after.MeterPaid != before.MeterPaid || after.LedgerPaid != before.LedgerPaid || after.MeterPaid != after.LedgerPaid {
		t.Fatalf("canonical facts changed live provider credit: before=%+v after=%+v", before, after)
	}
	later.Release()
	raw.Release()
	finishPaidFixture(t, a, ctx, intent)
	if root.Snapshot(1).Instance != (resourcebudget.Vector{}) {
		t.Fatalf("live facts retained paid owner: %+v", root.Snapshot(1))
	}
}

func TestPaidCapacityCanonicalAdvancementDoesNotRestoreSpentCredit(t *testing.T) {
	root, err := resourcebudget.New(resourcebudget.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	newAdmission := func(call string) *RequestAdmission {
		a, e := NewRequestAdmission(nil, RequestAdmissionConfig{MainConcurrency: map[int]int{0: 1}})
		if e != nil {
			t.Fatal(e)
		}
		if e = a.ConfigurePipelineBudget(PipelineBudgetLimits{Segments: 4, Candidates: 8192, Item: 4096, Responses: 1024, Handoff: 1 << 20}); e != nil {
			t.Fatal(e)
		}
		if e = a.AttachRootBudget(root, resourcebudget.Scope{ActorID: 1, CallID: call}); e != nil {
			t.Fatal(e)
		}
		t.Cleanup(a.Close)
		return a
	}
	a := newAdmission("canonical-live")
	if ok, e := a.ReserveMembers("live", 1, 1, 0, false); e != nil || !ok {
		t.Fatalf("reserve actual member %t %v", ok, e)
	}
	if ok, e := a.PrepareMain("live", 0, 1, 0, 0, 0, false, 1); e != nil || !ok {
		t.Fatalf("prepare %t %v", ok, e)
	}
	permit, _, err := a.TryAdmit(t.Context(), RequestAdmissionIntent{RequestID: "live", RoundIndex: 0, ResourceID: 1, Stage: RequestStageMain}, nil)
	if err != nil || permit == nil {
		t.Fatal(err)
	}
	defer permit.Release()
	ctx := WithBuildOwnership(t.Context(), a, "response:live")
	ctx = WithBuildReservation(ctx, func(n int64) error { return a.ClaimBuild("response:live", n) }, func(n int64) {
		if e := a.ReleaseBuildBytes("response:live", n); e != nil {
			t.Error(e)
		}
	})
	ctx = WithProviderResponseOwnership(ctx, a, "live", RequestStageMain)
	ctx, err = WithProviderResponseAllocation(ctx, 1024)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := OwnProviderResponseAllocation(ctx, int64(len("capture")), 1)
	if err != nil {
		t.Fatal(err)
	}
	rawBacking := make([]byte, len("capture"), len("capture"))
	copy(rawBacking, "capture")
	if err = ApplyProviderResponseHandoff(ctx, "live", int64(len(rawBacking))); err != nil {
		t.Fatal(err)
	}
	facts := responsecontract.ResponseFacts{CaptureBytes: int64(len(rawBacking)), PublicKind: responsecontract.PublicNone}
	check := func(phase responsecontract.ResponsePhase, want int64) {
		t.Helper()
		if e := a.RestoreResponseOwnership("live", 0, 1, facts, phase, true); e != nil {
			t.Fatal(e)
		}
		if _, e := WithProviderResponseAllocation(ctx, 1024); e != nil {
			t.Fatal("canonical advancement broke provider rebind", e)
		}
		observed := RejectionEvidenceFromContext(ctx)
		a.mu.Lock()
		saved := a.budget.requests["live"]
		a.mu.Unlock()
		if observed.MeterPaid != want || observed.LedgerPaid != want || saved.restored || !saved.liveProvider {
			t.Fatalf("%s restored spent credit or marked actual live as cold: %+v restored=%t", phase, observed, saved.restored)
		}
	}
	check(responsecontract.CapturePending, 1024)
	// Inventory was loaded earlier in this same runtime. That global fact cannot
	// turn a newly dispatched and registered provider into a cold response.
	a.mu.Lock()
	a.budget.inventoryRestored = true
	a.mu.Unlock()
	canonicalBackings := map[string][]byte{}
	transfer := func(text, target string) int64 {
		t.Helper()
		credit, e := OwnProviderResponseAllocation(ctx, int64(len(text)), 1)
		if e != nil {
			t.Fatal(e)
		}
		backing := make([]byte, len(text), len(text))
		copy(backing, text)
		if e = TransferProviderResponseToBuild(ctx, credit, target); e != nil {
			t.Fatal(e)
		}
		canonicalBackings[target] = backing
		return int64(cap(backing))
	}
	model := transfer("model", "canonical-model")
	facts.ModelOutputBytes = model
	facts.OutputMemberBytes = model
	check(responsecontract.CapturePending, 1024-model) // A partial archive still retains capture.
	facts.CaptureBytes = 0
	check(responsecontract.OutputsReady, 1024-model)
	receipt := transfer("receipt", "canonical-receipt")
	facts.PublicBytes = receipt
	facts.PublicKind = responsecontract.PublicReceipt
	check(responsecontract.BusinessReady, 1024-model-receipt)
	b, err := NewBuildBoundsFromContext(ctx, 4096, 0)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := b.OwnAllocation(int64(len("candidate")), 1)
	if err != nil {
		t.Fatal(err)
	}
	candidateBacking := make([]byte, len("candidate"), len("candidate"))
	copy(candidateBacking, "candidate")
	if ok, e := a.BeginPromotionWithAllocation(ctx, "live", "candidate", int64(len(candidateBacking)), false, candidate); e != nil || !ok {
		t.Fatalf("actual promotion %t %v", ok, e)
	}
	facts.ModelOutputBytes = 0
	facts.OutputMemberBytes = 0
	facts.PublicBytes = 0
	facts.PublicKind = responsecontract.PublicNone
	check(responsecontract.Promoted, 1024-model-receipt-int64(len(candidateBacking)))
	if err = a.ApplyPromotion("live", "candidate", int64(len(candidateBacking))); err != nil {
		t.Fatal(err)
	}
	b.CloseRetired()
	rawBacking = nil
	raw.Release()
	if err = CloseProviderResponseAllocation(ctx); err != nil {
		t.Fatal(err)
	}
	permit.Release()
	candidateBacking = nil
	a.ReleaseBudgetEntry("candidate")
	delete(canonicalBackings, "canonical-model")
	if err = a.ReleaseBuild("canonical-model"); err != nil {
		t.Fatal(err)
	}
	delete(canonicalBackings, "canonical-receipt")
	if err = a.ReleaseBuild("canonical-receipt"); err != nil {
		t.Fatal(err)
	}
	a.Close()
	if root.Snapshot(1).Instance != (resourcebudget.Vector{}) {
		t.Fatalf("live close leaked canonical owners: %+v", root.Snapshot(1))
	}
	cold := newAdmission("canonical-cold")
	coldFacts := responsecontract.ResponseFacts{CaptureBytes: 7, PublicKind: responsecontract.PublicNone}
	if err = cold.RestoreResponseOwnership("cold", 0, 1, coldFacts, responsecontract.CapturePending, false); err != nil {
		t.Fatal(err)
	}
	cold.mu.Lock()
	saved := cold.budget.requests["cold"]
	cold.mu.Unlock()
	if !saved.restored || saved.liveProvider || saved.responseCredit != 7 {
		t.Fatalf("cold recovery manufactured live provider: %+v", saved)
	}
	before := root.Snapshot(1).Instance.PayloadBytes
	readCtx := WithBuildOwnership(t.Context(), cold, "cold-read")
	readCtx = WithBuildReservation(readCtx, func(n int64) error { return cold.ClaimBuild("cold-read", n) }, func(n int64) {
		if e := cold.ReleaseBuildBytes("cold-read", n); e != nil {
			t.Error(e)
		}
	})
	read, err := NewBuildBoundsFromContext(readCtx, 4096, 0)
	if err != nil {
		t.Fatal(err)
	}
	owned, err := read.OwnAllocation(7, 1)
	if err != nil {
		t.Fatal(err)
	}
	actualCopy := make([]byte, len("capture"), len("capture"))
	copy(actualCopy, "capture")
	if root.Snapshot(1).Instance.PayloadBytes != before+int64(cap(actualCopy)) {
		t.Fatal("cold length was mistaken for actual read backing payment")
	}
	actualCopy = nil
	owned.Release()
	read.CloseRetired()
	if root.Snapshot(1).Instance.PayloadBytes != before {
		t.Fatal("cold read backing did not retire independently")
	}
	cold.ReleaseDurableResponse("cold")
	cold.Close()
	if root.Snapshot(1).Instance != (resourcebudget.Vector{}) {
		t.Fatalf("cold last reference leaked: %+v", root.Snapshot(1))
	}
}

func TestPaidCapacityMeasuredWireIsNotChargedTwice(t *testing.T) {
	for _, stage := range []RequestStage{RequestStageMain, RequestStageAlignment} {
		t.Run(string(stage), func(t *testing.T) {
			a, root, ctx, intent := paidCapacityFixture(t, stage, 64, 32)
			before := root.Snapshot(1).Actor.PayloadBytes
			owner, err := OwnProviderResponseAllocation(ctx, 1024+64, 1)
			if err != nil || root.Snapshot(1).Actor.PayloadBytes != before {
				t.Fatal("same measured wire was charged twice", err, root.Snapshot(1))
			}
			backing := make([]byte, 1024+64)
			if _, err = OwnProviderResponseAllocation(ctx, 32, 1); err == nil {
				t.Fatal("borrowed unrelated System/User credit")
			}
			if err = TransferProviderResponseToBuild(ctx, owner, "wire-and-response"); err != nil || root.Snapshot(1).Actor.PayloadBytes != before {
				t.Fatal(err, root.Snapshot(1))
			}
			if stage == RequestStageMain {
				if err = ApplyProviderResponseHandoff(ctx, "paid", 0); err != nil {
					t.Fatal(err)
				}
			}
			finishPaidFixture(t, a, ctx, intent)
			if root.Snapshot(1).Actor.PayloadBytes != int64(cap(backing)) {
				t.Fatal("finish refunded consumed wire twice", root.Snapshot(1))
			}
			backing = nil
			if err = a.ReleaseBuild("wire-and-response"); err != nil || root.Snapshot(1).Actor.PayloadBytes != 0 {
				t.Fatal(err, root.Snapshot(1))
			}
		})
	}
}
