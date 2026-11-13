package resourcebudget

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
)

func limits(n int64) Limits { return Limits{n, n, n, n, n, n, n, n} }
func newBudget(t *testing.T, a, i int64) *RootBudget {
	t.Helper()
	b, err := New(Policy{limits(a), limits(i)})
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func source(rows ...Record) Source {
	return SourceFunc(func(ctx context.Context, emit func(Record) error) error {
		for _, r := range rows {
			if err := emit(r); err != nil {
				return err
			}
		}
		return nil
	})
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestActorPinsBoundedAndIdempotent(t *testing.T) {
	b := newBudget(t, 2, 2)
	must(t, b.RebuildActor(context.Background(), 1, source()))
	r1, err := b.PinActor(1)
	must(t, err)
	r2, err := b.PinActor(1)
	must(t, err)
	if _, err = b.PinActor(1); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	if err = b.ForgetActor(1); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if err = b.Rebuild(context.Background(), source()); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); r1() }()
	}
	wg.Wait()
	if b.pinCount != 1 || b.pins[1] != 1 {
		t.Fatal("one token released another caller's pin")
	}
	if err = b.ForgetActor(1); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	r2()
	must(t, b.ForgetActor(1))
	if b.pinCount != 0 || len(b.pins) != 0 {
		t.Fatal("pins retained")
	}
	if _, err = b.PinActor(1); !errors.Is(err, ErrActorMissing) {
		t.Fatal(err)
	}
}
func TestCanDrainOutsideUsesActualOwners(t *testing.T) {
	b := newBudget(t, 100, 100)
	scope := Scope{ActorID: 1, JobID: 1}
	other := Scope{ActorID: 2, JobID: 2}
	must(t, b.Claim(scope, "input:1", Vector{PayloadBytes: 1, PrepareOwners: 1}))
	must(t, b.Claim(other, "storage", Vector{LogicalStorageBytes: 1, QueuedDescriptors: 1}))
	if b.CanDrainOutside(scope) {
		t.Fatal("self input/cold storage invented a consumer")
	}
	must(t, b.Claim(other, "runtime", Vector{MainResponseOwners: 1}))
	if !b.CanDrainOutside(scope) {
		t.Fatal("another response owner omitted")
	}
	must(t, b.Transfer(other, "runtime", Vector{}))
	if b.CanDrainOutside(scope) {
		t.Fatal("retired owner still counted")
	}
}

func TestBulkInventoryRetirementIsAtomicAndBounded(t *testing.T) {
	b := newBudget(t, 100, 100)
	one := Scope{ActorID: 1, JobID: 1}
	two := Scope{ActorID: 2, JobID: 2}
	orphan := Scope{ActorID: 0, JobID: 3}
	must(t, b.Rebuild(context.Background(), source(Record{one, "one", Vector{LogicalStorageBytes: 10}}, Record{two, "two", Vector{LogicalStorageBytes: 20}}, Record{orphan, "orphan", Vector{LogicalStorageBytes: 30}})))
	must(t, b.RebuildActor(context.Background(), 1, source(Record{one, "one", Vector{LogicalStorageBytes: 10}})))
	if ids := b.CachedActorIDs(); len(ids) != 1 || ids[0] != 1 {
		t.Fatal(ids)
	}
	before := b.Snapshot(1)
	if err := b.ReleaseInventoryTotals(Vector{LogicalStorageBytes: 10}, Vector{LogicalStorageBytes: 5}, map[int]Vector{1: {LogicalStorageBytes: 6}}); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
	if b.Snapshot(1).Instance != before.Instance || b.Snapshot(1).Actor != before.Actor {
		t.Fatal("partial bulk release leaked")
	}
	must(t, b.Attach(one, "one", Vector{LogicalStorageBytes: 10}))
	if err := b.ReleaseInventoryTotals(Vector{LogicalStorageBytes: 10}, Vector{}, map[int]Vector{1: {LogicalStorageBytes: 10}}); !errors.Is(err, ErrIdentity) {
		t.Fatal("attached ownership retired", err)
	}
	must(t, b.Detach(one, "one"))
	must(t, b.ReleaseInventoryTotals(Vector{LogicalStorageBytes: 60}, Vector{LogicalStorageBytes: 30}, map[int]Vector{1: {LogicalStorageBytes: 10}}))
	if b.Snapshot(1).Instance != (Vector{}) || b.Snapshot(1).Actor != (Vector{}) || len(b.actors) != 1 {
		t.Fatal("bulk release retained stock or loaded history")
	}
}

func TestCanDrainOutsideDisappearanceDoesNotProveCapacityStillFull(t *testing.T) {
	l := limits(10)
	l.MaxMainResponseOwners = 1
	b, err := New(Policy{Actor: l, Instance: l})
	must(t, err)
	current := Scope{ActorID: 1, CallID: "current"}
	other := Scope{ActorID: 2, CallID: "other"}
	must(t, b.Claim(other, "other-runtime", Vector{MainResponseOwners: 1}))
	probe := b.Claim(current, "runtime", Vector{MainResponseOwners: 1})
	if !errors.Is(probe, ErrCapacity) || !CapacityHasDrain(probe) {
		t.Fatalf("failed probe lost same-lock consumer: %v", probe)
	}
	// Deterministic order for the pipeline race: the failed probe precedes
	// external release, and consumer classification follows that release.
	must(t, b.Transfer(other, "other-runtime", Vector{}))
	if b.CanDrainOutside(current) {
		t.Fatal("retired consumer still visible")
	}
	if !CapacityHasDrain(probe) {
		t.Fatal("release rewrote failed probe's consumer evidence")
	}
	must(t, b.Claim(current, "runtime", Vector{MainResponseOwners: 1}))
	t.Log("failed capacity probe -> external release -> CanDrainOutside false -> fresh claim succeeds; false alone cannot classify a stale probe")
}

func TestAtomicActorAndInstanceCapacity(t *testing.T) {
	b := newBudget(t, 10, 15)
	s1, s2, s3 := Scope{ActorID: 1, JobID: 1}, Scope{ActorID: 1, JobID: 2}, Scope{ActorID: 2, JobID: 3}
	must(t, b.Claim(s1, "first", Vector{PayloadBytes: 6, PrepareOwners: 1}))
	before := b.Snapshot(1)
	if err := b.Claim(s2, "same-actor", Vector{PayloadBytes: 5}); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	if got := b.Snapshot(1); got.Actor != before.Actor || got.Instance != before.Instance || got.ActiveIdentities != 1 {
		t.Fatal("actor refusal leaked roots")
	}
	must(t, b.Claim(s3, "second", Vector{PayloadBytes: 9}))
	if err := b.Claim(s2, "instance-full", Vector{PayloadBytes: 1}); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	if b.Snapshot(2).Instance.PayloadBytes != 15 || b.Snapshot(1).Actor.PayloadBytes != 6 {
		t.Fatal("instance refusal mutated actor")
	}
	must(t, b.Release(s1, "first", Vector{PrepareOwners: 1}))
	if b.Snapshot(1).Instance.PayloadBytes != 15 {
		t.Fatal("worker release refunded payload")
	}
}

func TestIndependentOverageDrainAndStableOwnership(t *testing.T) {
	b := newBudget(t, 10, 10)
	s := Scope{ActorID: 1, JobID: 1}
	rows := source(Record{s, "job:1:runtime", Vector{UnconfirmedMembers: 20, PayloadBytes: 5}}, Record{s, "job:1:storage", Vector{LogicalStorageBytes: 30}})
	must(t, b.Rebuild(context.Background(), rows))
	if len(b.actors) != 0 {
		t.Fatal("global rebuild retained historical actors")
	}
	if err := b.Claim(s, "new", Vector{PayloadBytes: 1}); !errors.Is(err, ErrActorMissing) {
		t.Fatal(err)
	}
	must(t, b.RebuildActor(context.Background(), 1, rows))
	must(t, b.Attach(s, "job:1:runtime", Vector{UnconfirmedMembers: 20, PayloadBytes: 5}))
	must(t, b.Attach(s, "job:1:storage", Vector{LogicalStorageBytes: 30}))
	must(t, b.Transfer(s, "job:1:runtime", Vector{UnconfirmedMembers: 19, PayloadBytes: 6}))
	// Member decrease permits payload growth only because that separate root fits.
	if err := b.Transfer(s, "job:1:runtime", Vector{UnconfirmedMembers: 18, PayloadBytes: 11}); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	if err := b.Transfer(Scope{ActorID: 2, JobID: 1}, "job:1:runtime", Vector{}); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
	must(t, b.Transfer(s, "job:1:storage", Vector{LogicalStorageBytes: 29}))
	must(t, b.Detach(s, "job:1:runtime"))
	must(t, b.Detach(s, "job:1:storage"))
	must(t, b.ForgetActor(1))
	if b.Snapshot(1).Instance != (Vector{UnconfirmedMembers: 19, PayloadBytes: 6, LogicalStorageBytes: 29}) {
		t.Fatal("unload reset durable totals")
	}
	if b.Snapshot(1).Policy.Instance.MaxUnconfirmedMembers != 10 {
		t.Fatal("rebuild expanded policy")
	}
}

func TestIndependentWorkstationsAndStorage(t *testing.T) {
	b := newBudget(t, 2, 2)
	s := Scope{ActorID: 1, CallID: "preview"}
	v := Vector{PrepareOwners: 1, MainResponseOwners: 1, AuxResponseOwners: 1, BuildWorkers: 1, QueuedDescriptors: 2, LogicalStorageBytes: 2}
	must(t, b.Claim(s, "owners", v))
	if err := b.Claim(s, "queued", Vector{QueuedDescriptors: 1}); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	must(t, b.Release(s, "owners", Vector{PrepareOwners: 1, MainResponseOwners: 1, AuxResponseOwners: 1, BuildWorkers: 1, QueuedDescriptors: 2}))
	if b.Snapshot(1).Instance != (Vector{LogicalStorageBytes: 2}) {
		t.Fatal("runtime release reset storage")
	}
	must(t, b.Detach(s, "owners"))
}

func TestMoveNoCreditGapAndNoCrossDimensionBorrow(t *testing.T) {
	b := newBudget(t, 10, 10)
	s := Scope{ActorID: 1, JobID: 1}
	must(t, b.Claim(s, "reservation", Vector{PayloadBytes: 10, LogicalStorageBytes: 10}))
	must(t, b.Move(s, "reservation", "receipt", Vector{LogicalStorageBytes: 8}))
	if b.Snapshot(1).Instance != (Vector{PayloadBytes: 10, LogicalStorageBytes: 10}) {
		t.Fatal("move changed total")
	}
	if err := b.Move(s, "reservation", "receipt", Vector{LogicalStorageBytes: 3}); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
	if got, _ := b.Usage(s, "reservation"); got.LogicalStorageBytes != 2 {
		t.Fatal("failed move partially released")
	}
	must(t, b.Release(s, "reservation", Vector{LogicalStorageBytes: 2}))
	if b.Snapshot(1).Instance.LogicalStorageBytes != 8 {
		t.Fatal("unused reservation not released")
	}
}

func TestCheckedArithmeticAndRebuildFailureAtomicity(t *testing.T) {
	b := newBudget(t, math.MaxInt64, math.MaxInt64)
	s := Scope{ActorID: 1, JobID: 1}
	if err := b.Claim(s, "negative", Vector{BuildWorkers: -1}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	must(t, b.Claim(s, "max", Vector{PayloadBytes: math.MaxInt64}))
	if err := b.Claim(s, "overflow", Vector{PayloadBytes: 1}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if b.Snapshot(1).Instance.PayloadBytes != math.MaxInt64 {
		t.Fatal("overflow changed balance")
	}
	if err := b.Rebuild(context.Background(), source()); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	must(t, b.Release(s, "max", Vector{PayloadBytes: math.MaxInt64}))
	good := source(Record{s, "saved", Vector{LogicalStorageBytes: 7}})
	must(t, b.Rebuild(context.Background(), good))
	bad := source(Record{s, "max", Vector{LogicalStorageBytes: math.MaxInt64}}, Record{s, "overflow", Vector{LogicalStorageBytes: 1}})
	if err := b.Rebuild(context.Background(), bad); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if b.Snapshot(1).Instance.LogicalStorageBytes != 7 {
		t.Fatal("failed rebuild replaced inventory")
	}
	if err := b.Rebuild(context.Background(), source(Record{s, "owner", Vector{MainResponseOwners: 1}})); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

func TestOrphanStockAndBoundedActorCache(t *testing.T) {
	b := newBudget(t, 100, 100)
	rows := make([]Record, 1000)
	for i := range rows {
		rows[i] = Record{Scope: Scope{ActorID: i, JobID: i + 1}, Identity: fmt.Sprint(i), Usage: Vector{LogicalStorageBytes: 1}}
	}
	must(t, b.Rebuild(context.Background(), source(rows...)))
	if len(b.actors) != 0 || b.Snapshot(0).Instance.LogicalStorageBytes != 1000 {
		t.Fatal("orphan omitted or history retained")
	}
	if err := b.Attach(Scope{JobID: 1}, "orphan", Vector{LogicalStorageBytes: 1}); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
	for i := 1; i <= 100; i++ {
		must(t, b.RebuildActor(context.Background(), i, source(rows[i])))
	}
	if err := b.RebuildActor(context.Background(), 101, source(rows[101])); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	must(t, b.ForgetActor(1))
	must(t, b.RebuildActor(context.Background(), 101, source(rows[101])))
}

func TestConcurrentClaimsNeverOversubscribe(t *testing.T) {
	b := newBudget(t, 25, 25)
	s := Scope{ActorID: 1, JobID: 1}
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Go(func() {
			err := b.Claim(s, fmt.Sprint(i), Vector{MainResponseOwners: 1})
			if err != nil && !errors.Is(err, ErrCapacity) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if got := b.Snapshot(1); got.Instance.MainResponseOwners != 25 || got.ActiveIdentities != 25 {
		t.Fatal("concurrent grant escaped limit")
	}
}

func TestRuntimeUpdateCannotReviveMovedStorage(t *testing.T) {
	b := newBudget(t, 1000, 1000)
	s := Scope{ActorID: 1, JobID: 1}
	must(t, b.Claim(s, "runtime", Vector{PayloadBytes: 10, MainResponseOwners: 1, LogicalStorageBytes: 100}))
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 1000 {
			if err := b.TransferRuntime(s, "runtime", Vector{PayloadBytes: 10, MainResponseOwners: 1}, 0); err != nil {
				t.Error(err)
			}
		}
	})
	wg.Go(func() {
		for range 5 {
			if err := b.Move(s, "runtime", "storage", Vector{LogicalStorageBytes: 20}); err != nil {
				t.Error(err)
			}
		}
	})
	wg.Wait()
	r, _ := b.Usage(s, "runtime")
	d, _ := b.Usage(s, "storage")
	if r.LogicalStorageBytes != 0 || d.LogicalStorageBytes != 100 || b.Snapshot(1).Instance.LogicalStorageBytes != 100 {
		t.Fatal("runtime update revived moved reservation")
	}
	if err := b.TransferRuntime(s, "runtime", r, math.MinInt64); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
}

func TestRebuildAndActorPrepareRemainQuiescent(t *testing.T) {
	b := newBudget(t, 100, 100)
	must(t, b.Rebuild(context.Background(), source()))
	started, release := make(chan struct{}), make(chan struct{})
	errDone := make(chan error, 1)
	go func() {
		errDone <- b.RebuildActor(context.Background(), 1, SourceFunc(func(ctx context.Context, emit func(Record) error) error { close(started); <-release; return nil }))
	}()
	<-started
	if err := b.Rebuild(context.Background(), source()); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if err := b.Claim(Scope{ActorID: 1, JobID: 1}, "new", Vector{PayloadBytes: 1}); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	close(release)
	must(t, <-errDone)
	must(t, b.Claim(Scope{ActorID: 1, JobID: 1}, "new", Vector{PayloadBytes: 1}))
}

func TestActiveDescriptorMapBound(t *testing.T) {
	p := Policy{limits(100), limits(100)}
	p.Instance.MaxQueuedDescriptors = 2
	b, err := New(p)
	must(t, err)
	s := Scope{ActorID: 1, JobID: 1}
	must(t, b.Claim(s, "one", Vector{PayloadBytes: 2}))
	must(t, b.Claim(s, "two", Vector{PayloadBytes: 1}))
	if err := b.Claim(s, "three", Vector{PayloadBytes: 1}); !errors.Is(err, ErrCapacity) {
		t.Fatal("unmetered active descriptor", err)
	}
	if err := b.Move(s, "one", "three", Vector{PayloadBytes: 0}); err != nil {
		t.Fatal(err)
	}
	if err := b.Move(s, "one", "three", Vector{PayloadBytes: 1}); !errors.Is(err, ErrCapacity) {
		t.Fatal("partial move created unmetered descriptor", err)
	}
	if err := b.Move(s, "one", "three", Vector{PayloadBytes: 2}); err != nil {
		t.Fatal("whole identity move must reuse its slot", err)
	}
	if b.Snapshot(1).ActiveIdentities != 2 {
		t.Fatal("move exceeded node bound")
	}
	if err := b.Claim(s, "empty", Vector{}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

func TestAttachCannotCreateUnmeteredDescriptor(t *testing.T) {
	p := Policy{limits(100), limits(100)}
	p.Instance.MaxQueuedDescriptors = 2
	b, err := New(p)
	must(t, err)
	s := Scope{ActorID: 1, JobID: 1}
	rows := source(Record{s, "stock", Vector{PayloadBytes: 3}})
	must(t, b.Rebuild(context.Background(), rows))
	must(t, b.RebuildActor(context.Background(), 1, rows))
	must(t, b.Attach(s, "one", Vector{PayloadBytes: 1}))
	must(t, b.Attach(s, "two", Vector{PayloadBytes: 1}))
	if err := b.Attach(s, "three", Vector{PayloadBytes: 1}); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	if b.Snapshot(1).Instance.PayloadBytes != 3 {
		t.Fatal("failed attach changed stock")
	}
}

func TestInactiveAndOrphanInventoryRelease(t *testing.T) {
	b := newBudget(t, 100, 100)
	s := Scope{ActorID: 1, JobID: 1}
	orphan := Scope{JobID: 2}
	actorRows := source(Record{s, "actor", Vector{LogicalStorageBytes: 20}})
	must(t, b.Rebuild(context.Background(), source(Record{s, "actor", Vector{LogicalStorageBytes: 20}}, Record{orphan, "orphan", Vector{LogicalStorageBytes: 7}})))
	if err := b.ReleaseInventory(s, Vector{LogicalStorageBytes: 1}); !errors.Is(err, ErrActorMissing) {
		t.Fatal(err)
	}
	must(t, b.RebuildActor(context.Background(), 1, actorRows))
	must(t, b.ReleaseInventory(s, Vector{LogicalStorageBytes: 5}))
	must(t, b.ReleaseInventory(orphan, Vector{LogicalStorageBytes: 7}))
	if err := b.ReleaseInventory(orphan, Vector{LogicalStorageBytes: 1}); !errors.Is(err, ErrIdentity) {
		t.Fatal("orphan cleanup borrowed actor stock", err)
	}
	must(t, b.Attach(s, "active", Vector{LogicalStorageBytes: 15}))
	if err := b.ReleaseInventory(s, Vector{LogicalStorageBytes: 1}); !errors.Is(err, ErrIdentity) {
		t.Fatal("inactive release stole active credit", err)
	}
	if b.Snapshot(1).Instance.LogicalStorageBytes != 15 {
		t.Fatal("cleanup lost factual balance")
	}
}
