package backend_test

import (
	"context"
	"errors"
	"math"
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
	maximum, _ := backend.RequestMemberMetadataBound(16)
	prepared, _ := root.Usage(scope, "job:1:runtime")
	if prepared.PayloadBytes != 900+maximum+(8<<10) || a.PipelineSnapshot().HandoffBytes != 8<<10 || a.PipelineSnapshot().Segments != 0 {
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
	storage, ok := a.StorageCredit("main")
	if !ok || storage < maximum-1000 {
		t.Fatal("dispatch omitted metadata maximum after actual stored bytes")
	}
	if err := a.SetRequestMetadataBytes("main", 15, 1000); !errors.Is(err, backend.ErrPipelineCapacity) {
		t.Fatal("accepted a shorter original table")
	}
	if err := a.SetRequestMetadataBytes("main", 16, maximum+1); !errors.Is(err, backend.ErrPipelineCapacity) {
		t.Fatal("accepted unpromised metadata after dispatch")
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
	for _, n := range []int{-1, 65537, math.MaxInt} {
		if _, err := backend.RequestMemberMetadataBound(n); !errors.Is(err, backend.ErrBuildCapacity) {
			t.Fatalf("count=%d not refused before allocation: %v", n, err)
		}
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

func TestRequestMetadataUnknownFactsKeepMaximumUntilRebuild(t *testing.T) {
	a := contractAdmission(t, 256, 4, 64<<20)
	root := contractRoot(t, 64<<20)
	scope := resourcebudget.Scope{ActorID: 1, JobID: 1}
	if err := a.AttachRootBudget(root, scope); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PrepareMembers("unknown", 16, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	maximum, _ := backend.RequestMemberMetadataBound(16)
	if err := a.SetRequestMetadataBytes("unknown", 16, 1000); err != nil {
		t.Fatal(err)
	}
	a.MarkRequestMetadataUncertain("unknown")
	a.ReleaseBudgetEntry("unknown")
	retained, _ := root.Usage(scope, "job:1:runtime")
	if retained.PayloadBytes != maximum {
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
