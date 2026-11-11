package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

func TestBudgetSelfInputAndPreparedDescriptorDoNotInventDrain(t *testing.T) {
	l := resourcebudget.Limits{MaxUnconfirmedMembers: 256, MaxPayloadBytes: 32 << 10, MaxPrepareOwners: 8, MaxMainResponseOwners: 8, MaxAuxResponseOwners: 8, MaxBuildWorkers: 8, MaxQueuedDescriptors: 32, MaxLogicalStorageBytes: 64 << 20}
	root, err := resourcebudget.New(resourcebudget.Policy{Actor: l, Instance: l})
	if err != nil {
		t.Fatal(err)
	}
	scope := resourcebudget.Scope{ActorID: 1, CallID: "self"}
	if err = root.Claim(scope, "input:self:1", resourcebudget.Vector{PayloadBytes: 4 << 10, PrepareOwners: 1}); err != nil {
		t.Fatal(err)
	}
	r := stagedTestRuntime(t, DefaultCandidateLimits())
	if err = r.Admission.AttachRootBudget(root, scope); err != nil {
		t.Fatal(err)
	}
	remote := &runtimeTestBackend{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: stagedTestHandler(t, r, remote)}, newTestDoc(1), NewMemoryRoundStore(nil), r, quietLogger(), nil)
	if !errors.Is(err, backend.ErrNoCapacityDrain) || remote.calls.Load() != 0 || len(result.Resolved) != 0 {
		t.Fatalf("self-full root invented drain: %+v err=%v HTTP=%d", result, err, remote.calls.Load())
	}
	if ctx.Err() != nil {
		t.Fatal("capacity failure waited for deadline")
	}
	if r.Admission.CanDrainAggregate() {
		t.Fatal("same-scope input/queued descriptor counted as a consumer")
	}
}
