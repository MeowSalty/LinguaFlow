package backend_test

import (
	"context"
	"errors"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

func TestRequestMetadataOriginalSlotsOutliveRemainingMembers(t *testing.T) {
	a := contractAdmission(t, 256, 4, 64<<20)
	root := contractRoot(t, 64<<20)
	scope := resourcebudget.Scope{ActorID: 1, JobID: 1}
	seedMetadataStock(t, root, scope, 900)
	if err := a.RestoreRootMetadata(900); err != nil {
		t.Fatal(err)
	}
	if err := a.AttachRootBudget(root, scope); err != nil {
		t.Fatal(err)
	}
	if err := contractPrepare(t, a, "main", 16, 0); err != nil {
		t.Fatal(err)
	}
	prepared, _ := root.Usage(scope, "job:1:runtime")
	if prepared.PayloadBytes != 900+(8<<10) || a.PipelineSnapshot().HandoffBytes != 8<<10 || a.PipelineSnapshot().Segments != 0 {
		t.Fatalf("metadata changed Job K/E or took S: %+v %+v", prepared, a.PipelineSnapshot())
	}
	if err := a.SetRequestMetadataBytes("main", 16, 1000); err != nil {
		t.Fatal(err)
	}
	permit, _, err := a.TryAdmit(context.Background(), backend.RequestAdmissionIntent{RequestID: "main", Stage: backend.RequestStageMain}, nil)
	if err != nil || permit == nil {
		t.Fatalf("grant: %v", err)
	}
	defer permit.Release()
	before, _ := root.Usage(scope, "job:1:runtime")
	a.ReleaseBudgetEntry("main") // S can reach zero while the original table lives.
	a.ReleasePrepared("main")    // A granted owner must not refund its metadata.
	after, _ := root.Usage(scope, "job:1:runtime")
	if before.PayloadBytes != after.PayloadBytes || after.UnconfirmedMembers != 0 {
		t.Fatalf("remaining S refunded original metadata: before=%+v after=%+v", before, after)
	}
	err = a.SetRequestMetadataBytes("main", 15, 1000)
	if !errors.Is(err, backend.ErrPipelineCapacity) {
		t.Fatal("accepted a shorter original table")
	}
	var rejection *backend.RejectionError
	if !errors.As(err, &rejection) || rejection.Evidence.Branch != "request_metadata_facts_mismatch" || rejection.Evidence.InventoryMembers != 16 || rejection.Evidence.RequestedMembers != 15 {
		t.Fatalf("original/remaining member mismatch lost rejection facts: %v", err)
	}
	if err := a.SetRequestMetadataBytes("main", 16, 1000); err != nil {
		t.Fatal("authoritative facts rejected", err)
	}
	permit.Release()
	joined, _ := root.Usage(scope, "job:1:runtime")
	if joined.PayloadBytes != 1900 {
		t.Fatalf("finish failed to transfer actual retained bytes: %+v", joined)
	}
	if err := a.ResetInventoryAfterJoin(0, 0, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, exists := root.Usage(scope, "job:1:runtime"); exists {
		t.Fatal("pure terminal audit retained P after joined facts")
	}
}

func TestRequestMetadataRestoreTransfersScalarAndUnsentRefunds(t *testing.T) {
	a := contractAdmission(t, 256, 4, 64<<20)
	root := contractRoot(t, 64<<20)
	scope := resourcebudget.Scope{ActorID: 1, JobID: 1}
	seedMetadataStock(t, root, scope, 1900)
	if err := a.RestoreRootMetadata(1900); err != nil {
		t.Fatal(err)
	}
	if err := a.AttachRootBudget(root, scope); err != nil {
		t.Fatal(err)
	}
	before, _ := root.Usage(scope, "job:1:runtime")
	if err := a.RestoreRequestMetadata("saved", 16, 1000); err != nil {
		t.Fatal(err)
	}
	after, _ := root.Usage(scope, "job:1:runtime")
	if before != after {
		t.Fatal("reattaching metadata charged scalar twice")
	}
	if err := a.RestoreRequestMetadata("bad", 16, 1000); !errors.Is(err, backend.ErrPipelineCapacity) {
		t.Fatal("restored more than scalar stock")
	}
	if _, err := a.PrepareMembers("unsent", 16, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	a.ReleaseBudgetEntry("unsent")
	refunded, _ := root.Usage(scope, "job:1:runtime")
	if refunded != before {
		t.Fatalf("unsent metadata leaked: %+v", refunded)
	}

}

func TestRequestMetadataStandaloneChargeOnce(t *testing.T) {
	const actual = 1200
	a := contractAdmission(t, 256, 4, 64<<20)
	root, err := resourcebudget.New(resourcebudget.Policy{Instance: resourcebudget.Limits{MaxPayloadBytes: 1 << 20}})
	if err != nil {
		t.Fatal(err)
	}
	scope := resourcebudget.Scope{ActorID: 1, JobID: 1}
	if err := a.AttachRootBudget(root, scope); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := a.RestoreRequestMetadata("saved", 2, actual); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.RestoreRequestMetadata("saved", 2, actual+1); !errors.Is(err, backend.ErrPipelineCapacity) {
		t.Fatalf("accepted conflicting metadata: %v", err)
	}
	if err := a.RestoreRequestMetadata("too-large", 2, 2<<20); !errors.Is(err, resourcebudget.ErrCapacity) {
		t.Fatalf("metadata ignored the root quota: %v", err)
	}
	if used, _ := root.Usage(scope, "job:1:runtime"); used.PayloadBytes != actual {
		t.Fatalf("metadata reattachment or rejected allocation charged bytes twice: %+v", used)
	}
}

func seedMetadataStock(t *testing.T, root *resourcebudget.RootBudget, scope resourcebudget.Scope, bytes int64) {
	t.Helper()
	source := resourcebudget.SourceFunc(func(_ context.Context, emit func(resourcebudget.Record) error) error {
		return emit(resourcebudget.Record{Scope: scope, Identity: "job:1:runtime", Usage: resourcebudget.Vector{PayloadBytes: bytes}})
	})
	if err := root.Rebuild(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	if err := root.RebuildActor(context.Background(), scope.ActorID, source); err != nil {
		t.Fatal(err)
	}
}

func TestRequestMetadataUnknownFactsKeepOnlyKnownActual(t *testing.T) {
	a := contractAdmission(t, 256, 4, 64<<20)
	root := contractRoot(t, 64<<20)
	scope := resourcebudget.Scope{ActorID: 1, JobID: 1}
	if err := a.AttachRootBudget(root, scope); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PrepareMembers("unknown", 16, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.SetRequestMetadataBytes("unknown", 16, 1000); err != nil {
		t.Fatal(err)
	}
	a.MarkRequestMetadataUncertain("unknown")
	a.ReleaseBudgetEntry("unknown")
	retained, _ := root.Usage(scope, "job:1:runtime")
	if retained.PayloadBytes != 1000 {
		t.Fatalf("unknown ACK guessed a refund: %+v", retained)
	}
	if err := a.ResetInventoryAfterJoin(0, 0, 0, 0, 1000); err != nil {
		t.Fatal(err)
	}
	exact, _ := root.Usage(scope, "job:1:runtime")
	if exact.PayloadBytes != 1000 {
		t.Fatal("rebuild did not settle exact retained fact")
	}
}

func TestRequestMetadataRetiredSourceSettlesAuthoritativeFactOnce(t *testing.T) {
	a := contractAdmission(t, 256, 4, 64<<20)
	root := contractRoot(t, 64<<20)
	scope := resourcebudget.Scope{ActorID: 1, JobID: 1}
	if err := a.AttachRootBudget(root, scope); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PrepareMembers("retired", 16, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.SetRequestMetadataBytes("retired", 16, 1000); err != nil {
		t.Fatal(err)
	}
	a.MarkRequestMetadataUncertain("retired")
	a.ReleaseBudgetEntry("retired")
	if held, _ := root.Usage(scope, "job:1:runtime"); held.PayloadBytes != 1000 {
		t.Fatalf("retirement dropped unknown durable fact: %+v", held)
	}
	if err := a.SetRequestMetadataBytes("retired", 16, 900); err != nil {
		t.Fatal(err)
	}
	if exact, _ := root.Usage(scope, "job:1:runtime"); exact.PayloadBytes != 900 {
		t.Fatalf("retired source did not settle authoritative bytes: %+v", exact)
	}
	if err := a.SetRequestMetadataBytes("retired", 16, 900); !errors.Is(err, backend.ErrPipelineCapacity) {
		t.Fatalf("retired source metadata was recreated: %v", err)
	}
}

func TestRequestMetadataClosedSourceCannotChangeFreshRuntime(t *testing.T) {
	old := contractAdmission(t, 256, 4, 64<<20)
	root := contractRoot(t, 64<<20)
	scope := resourcebudget.Scope{ActorID: 1, JobID: 1}
	if err := old.AttachRootBudget(root, scope); err != nil {
		t.Fatal(err)
	}
	if _, err := old.PrepareMembers("old", 16, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	if err := old.SetRequestMetadataBytes("old", 16, 1000); err != nil {
		t.Fatal(err)
	}
	old.MarkRequestMetadataUncertain("old")
	old.ReleaseBudgetEntry("old")
	old.Close()
	fresh := contractAdmission(t, 256, 4, 64<<20)
	if err := fresh.RestoreRootMetadata(1000); err != nil {
		t.Fatal(err)
	}
	if err := fresh.AttachRootBudget(root, scope); err != nil {
		t.Fatal(err)
	}
	before, _ := root.Usage(scope, "job:1:runtime")
	if err := old.SetRequestMetadataBytes("old", 16, 900); !errors.Is(err, backend.ErrAdmissionClosed) {
		t.Fatalf("closed source changed fresh runtime: %v", err)
	}
	if after, _ := root.Usage(scope, "job:1:runtime"); after != before {
		t.Fatalf("late source modified new owner: before=%+v after=%+v", before, after)
	}
	if err := old.SetRequestMetadataBytes("old", 16, 1000); err != nil {
		t.Fatal(err)
	}
	if after, _ := root.Usage(scope, "job:1:runtime"); after != before {
		t.Fatal("retiring equal old fact touched fresh owner")
	}
}
