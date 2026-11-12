package pipeline

import (
	"context"
	"errors"
	"reflect"
	"sync"
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

func TestStagedFixedRecoveryCapacityPrecedesBlockedPreparation(t *testing.T) {
	for _, name := range []string{"fixed", "active", "loading_scope", "aggregate_only"} {
		t.Run(name, func(t *testing.T) {
			limits := resourcebudget.Limits{MaxUnconfirmedMembers: 8, MaxPayloadBytes: 64 << 20, MaxPrepareOwners: 1, MaxMainResponseOwners: 8, MaxAuxResponseOwners: 8, MaxBuildWorkers: 8, MaxQueuedDescriptors: 32, MaxLogicalStorageBytes: 64 << 20}
			root, err := resourcebudget.New(resourcebudget.Policy{Actor: limits, Instance: limits})
			if err != nil {
				t.Fatal(err)
			}
			scope := resourcebudget.Scope{ActorID: 1, CallID: "current"}
			inventory := resourcebudget.SourceFunc(func(_ context.Context, emit func(resourcebudget.Record) error) error {
				return emit(resourcebudget.Record{Scope: scope, Identity: "paid_future", Usage: resourcebudget.Vector{UnconfirmedMembers: 1}})
			})
			if err := root.Rebuild(context.Background(), inventory); err != nil {
				t.Fatal(err)
			}
			if err := root.RebuildActor(context.Background(), scope.ActorID, inventory); err != nil {
				t.Fatal(err)
			}
			holder := resourcebudget.Scope{ActorID: 1, CallID: "occupied_prepare"}
			if err := root.Claim(holder, "holder", resourcebudget.Vector{PrepareOwners: 1}); err != nil {
				t.Fatal(err)
			}
			var releaseOnce sync.Once
			releaseHolder := func() {
				releaseOnce.Do(func() { root.Release(holder, "holder", resourcebudget.Vector{PrepareOwners: 1}) })
			}
			defer releaseHolder()
			candidateLimits := DefaultCandidateLimits()
			candidateLimits.Segments = 1
			r := stagedTestRuntime(t, candidateLimits)
			if err := r.Admission.RestoreInventory(1, 0, 0, 0); err != nil {
				t.Fatal(err)
			}
			var owner *WindowConsumer
			if name == "active" || name == "loading_scope" {
				ownerRound := 2
				if name == "loading_scope" {
					ownerRound = 0
				}
				owner = r.Window.EnterScope(2, ownerRound)
				defer owner.Leave()
			}
			if name != "aggregate_only" {
				if err := r.Window.RestoreRaw("paid_future", 1, 2, 2, nil); err != nil {
					t.Fatal(err)
				}
			}
			if err := r.Admission.AttachRootBudget(root, scope); err != nil {
				t.Fatal(err)
			}
			memory := NewMemoryRoundStore(nil)
			if _, err := memory.Seal(context.Background(), []int{0}); err != nil {
				t.Fatal(err)
			}
			store := &windowResourceStore{RoundStore: memory, resourceID: 1, roundID: 1}
			h := &windowBatchProbe{batchSize: 2}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := RunStagedRound(ctx, Round{Concurrency: 1, Handler: h.handler(t, r)}, newTestDoc(1), store, r, quietLogger(), nil)
				done <- err
			}()
			if name == "active" || name == "loading_scope" {
				select {
				case err := <-done:
					t.Fatalf("drainable or unclaimed inventory classified as permanent before preparation: %v", err)
				case <-time.After(25 * time.Millisecond):
				}
				cancel()
				select {
				case err := <-done:
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("blocked active producer did not cancel: %v", err)
					}
				case <-time.After(3 * time.Second):
					releaseHolder()
					t.Fatal("cancelled idle coordinator did not join after releasing preparation capacity")
				}
			} else {
				select {
				case err := <-done:
					want := ErrCandidateCapacity
					if name == "aggregate_only" {
						want = ErrResourceYield
					}
					if !errors.Is(err, want) {
						t.Fatalf("fixed recovery masked by preparation capacity: %v", err)
					}
					if name == "aggregate_only" && errors.Is(err, ErrCandidateCapacity) {
						t.Fatal("unclaimed aggregate prematurely rejected as permanent")
					}
				case <-ctx.Done():
					t.Fatal("fixed recovery waited for an unavailable preparation ticket")
				}
				if ctx.Err() != nil {
					t.Fatal("permanent recovery failure waited for deadline")
				}
			}
			if batches, _ := h.snapshot(); len(batches) != 0 {
				t.Fatal("unavailable recovery capacity dispatched HTTP")
			}
			if snapshot := r.Window.Snapshot(); snapshot.Segments != 1 {
				t.Fatalf("paid recovery inventory removed: %+v", snapshot)
			}
			releaseHolder()
		})
	}
}

func TestWindowPreparationProbeDoesNotReserveAggregateInventory(t *testing.T) {
	limits := DefaultCandidateLimits()
	limits.Segments = 2
	r := stagedTestRuntime(t, limits)
	if err := r.Admission.RestoreInventory(4, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	consumer := r.Window.EnterScope(1, 1)
	defer consumer.Leave()
	consumer.RecoveryComplete()
	before := r.Admission.PipelineSnapshot()
	wake := r.Admission.Changed()
	result, gotWake, aggregateOnly := r.Window.probePrepareOwned(1, 1, consumer, nil)
	if result != WindowBusy || !aggregateOnly || gotWake != wake {
		t.Fatalf("unclaimed overage probe: result=%v aggregate=%v", result, aggregateOnly)
	}
	if !reflect.DeepEqual(before, r.Admission.PipelineSnapshot()) || r.Admission.Changed() != wake || len(r.Window.entries) != 0 {
		t.Fatal("pure preparation probe changed inventory, owner or fair-turn credit")
	}
	if err := r.Window.RestoreRaw("current_raw", 1, 1, 1, consumer); err != nil {
		t.Fatal(err)
	}
	if result, _, aggregateOnly = r.Window.probePrepareOwned(1, 1, consumer, nil); result != WindowBusy || aggregateOnly {
		t.Fatalf("active claimed response treated as anonymous aggregate: result=%v aggregate=%v", result, aggregateOnly)
	}
}

func TestFixedUnclaimedRecoveryTransfersOnlyActualRestoredMembers(t *testing.T) {
	limits := DefaultCandidateLimits()
	limits.Segments = 5
	r := stagedTestRuntime(t, limits)
	if r.Window.FixUnclaimedRecovery() {
		t.Fatal("fresh runtime invented fixed recovery")
	}
	if err := r.Admission.RestoreInventory(4, 256, 0, 0); err != nil {
		t.Fatal(err)
	}
	loading := r.Window.EnterScope(2, 0)
	if r.Window.FixUnclaimedRecovery() {
		t.Fatal("loading resource established joined recovery proof")
	}
	loading.Leave()
	if err := r.Admission.ChangeStation(1, 0, 0); err != nil {
		t.Fatal(err)
	}
	if r.Window.FixUnclaimedRecovery() {
		t.Fatal("live preparation owner established fixed recovery proof")
	}
	if err := r.Admission.ChangeStation(-1, 0, 0); err != nil {
		t.Fatal(err)
	}
	if !r.Window.FixUnclaimedRecovery() || r.Window.FixUnclaimedRecovery() {
		t.Fatal("joined scalar recovery was not frozen exactly once")
	}
	current := r.Window.EnterScope(1, 1)
	defer current.Leave()
	current.RecoveryComplete()
	if limit := r.Window.fixedBatchLimit(current); limit != 1 {
		t.Fatalf("S5/fixed4 did not retain its singleton slot: %d", limit)
	}
	if result, _, _ := r.Window.probePrepareOwned(2, 1, current, nil); result != WindowPinnedLimit {
		t.Fatalf("fixed recovery did not use existing prefix split: %v", result)
	}
	// Newly admitted ledger credit may precede its restored window view. It
	// must not consume the old fixed base merely because the view was absent.
	if ok, err := r.Admission.ReserveMembers("new_http", 1, 1, 1, true); err != nil || !ok {
		t.Fatalf("new singleton credit: %v %v", ok, err)
	}
	for range 2 {
		if err := r.Window.RestoreRaw("new_http", 1, 1, 1, current); err != nil {
			t.Fatal(err)
		}
	}
	r.Window.Release("new_http")
	if r.Window.fixedUnclaimedMembers != 4 || r.Window.Snapshot().Segments != 4 {
		t.Fatal("new HTTP restoration or abort consumed fixed recovery")
	}
	if ok, err := r.Admission.ReserveMembers("new_member", 1, 1, 1, true); err != nil || !ok {
		t.Fatalf("new candidate credit: %v %v", ok, err)
	}
	if ok, err := r.Admission.BeginPromotion("new_member", "new_candidate", 128, false); err != nil || !ok {
		t.Fatalf("new candidate promotion: %v %v", ok, err)
	}
	if err := r.Admission.ApplyPromotion("new_member", "new_candidate"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := r.Window.ClaimRecovered("new_candidate", 128, current); err != nil {
			t.Fatal(err)
		}
	}
	r.Window.Release("new_candidate")
	if r.Window.fixedUnclaimedMembers != 4 {
		t.Fatal("new candidate recovery consumed old fixed base")
	}
	if err := r.Window.RestoreRaw("invalid", 5, 2, 2, nil); err == nil || r.Window.fixedUnclaimedMembers != 4 {
		t.Fatal("failed recovery changed its fixed proof")
	}
	future := r.Window.EnterScope(2, 2)
	for range 2 {
		if err := r.Window.RestoreRaw("future_raw", 1, 2, 2, future); err != nil {
			t.Fatal(err)
		}
		if err := r.Window.ClaimRecovered("future_candidate", 128, future); err != nil {
			t.Fatal(err)
		}
	}
	future.RecoveryComplete()
	if r.Window.fixedUnclaimedMembers != 2 || r.Window.fixedBatchLimit(future) != 3 {
		t.Fatal("raw/candidate claims double counted fixed recovery")
	}
	future.Leave()
	if r.Window.fixedBatchLimit(current) != 1 {
		t.Fatal("unclaimed proof plus foreign entries lost fixed membership")
	}
	other := r.Window.EnterScope(3, 3)
	if err := r.Window.RestoreRaw("other_raw", 1, 3, 3, other); err != nil {
		t.Fatal(err)
	}
	if err := r.Window.ClaimRecovered("other_candidate", 128, other); err != nil {
		t.Fatal(err)
	}
	other.RecoveryComplete()
	if r.Window.fixedUnclaimedMembers != 0 || r.Window.fixedBatchLimit(other) != 3 {
		t.Fatal("claim transfer did not release fixed base for its real consumer")
	}
	other.Leave()
	for _, id := range []string{"future_raw", "future_candidate", "other_raw", "other_candidate"} {
		r.Window.Release(id)
	}
	if snapshot := r.Window.Snapshot(); snapshot.Segments != 0 || snapshot.Bytes != 0 {
		t.Fatalf("claimed recovery did not drain normally: %+v", snapshot)
	}
}

func TestFixedUnclaimedRecoveryRejectsNoSingletonSpace(t *testing.T) {
	for _, members := range []int64{0, 2, 4} {
		r := stagedTestRuntime(t, CandidateLimits{Segments: 2, Bytes: 16 << 20, ItemBytes: 1 << 20})
		if err := r.Admission.RestoreInventory(members, 0, 0, 0); err != nil {
			t.Fatal(err)
		}
		if r.Window.FixUnclaimedRecovery() {
			t.Fatalf("stock%d/S2 invented a singleton recovery slot", members)
		}
	}
}
