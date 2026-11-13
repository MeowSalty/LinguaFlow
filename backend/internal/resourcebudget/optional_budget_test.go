package resourcebudget

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
)

func optionalRoot(t *testing.T, p Policy) *RootBudget {
	t.Helper()
	b, err := New(p)
	must(t, err)
	return b
}

func optionalLimits(i int, n int64) Limits {
	v := [8]int64{}
	v[i] = n
	return Limits{v[0], v[1], v[2], v[3], v[4], v[5], v[6], v[7]}
}

func assertBudgetUnchanged(t *testing.T, b *RootBudget, actor int, before Snapshot) {
	t.Helper()
	if got := b.Snapshot(actor); got != before {
		t.Fatalf("rejected operation changed snapshot: got %+v want %+v", got, before)
	}
	select {
	case <-before.Changed:
		t.Fatal("rejected operation sent a notification")
	default:
	}
}

func TestOptionalBudgetPolicyAndEachDimension(t *testing.T) {
	optionalRoot(t, Policy{})
	for _, group := range []string{"actor", "instance"} {
		for i, dimension := range dimensions {
			t.Run(group+"/"+dimension, func(t *testing.T) {
				p := Policy{}
				if group == "actor" {
					p.Actor = optionalLimits(i, -1)
				} else {
					p.Instance = optionalLimits(i, -1)
				}
				if _, err := New(p); !errors.Is(err, ErrInvalid) {
					t.Fatalf("negative policy: %v", err)
				}
				if group == "actor" {
					p.Actor = optionalLimits(i, 2)
				} else {
					p.Instance = optionalLimits(i, 2)
				}
				b := optionalRoot(t, p)
				s := Scope{ActorID: 1, CallID: "one"}
				v := limits(7).vector()
				values := v.values()
				values[i] = 2
				v = vector(values)
				must(t, b.Claim(s, "one", v))
				before := b.Snapshot(1)
				values[i] = 3
				if err := b.Transfer(s, "one", vector(values)); !errors.Is(err, ErrCapacity) {
					t.Fatalf("positive cap not enforced: %v", err)
				}
				assertBudgetUnchanged(t, b, 1, before)
				other := Scope{ActorID: 2, CallID: "other"}
				increment := [8]int64{}
				increment[i] = 1
				otherBefore := b.Snapshot(2)
				err := b.Claim(other, "other", vector(increment))
				if group == "actor" {
					must(t, err)
					must(t, b.Release(other, "other", vector(increment)))
					must(t, b.ForgetActor(2))
				} else {
					if !errors.Is(err, ErrCapacity) {
						t.Fatalf("instance cap omitted another actor: %v", err)
					}
					assertBudgetUnchanged(t, b, 2, otherBefore)
				}
				must(t, b.Transfer(s, "one", v))
				must(t, b.Release(s, "one", v))
				must(t, b.ForgetActor(1))
			})
		}
	}
}

func TestOptionalBudgetMixedScopesAndOverflow(t *testing.T) {
	b := optionalRoot(t, Policy{Actor: Limits{MaxPayloadBytes: 2}, Instance: Limits{MaxMainResponseOwners: 1}})
	one, two := Scope{ActorID: 1, CallID: "one"}, Scope{ActorID: 2, CallID: "two"}
	must(t, b.Claim(one, "one", Vector{PayloadBytes: 2, MainResponseOwners: 1, LogicalStorageBytes: 100}))
	must(t, b.Claim(two, "two", Vector{PayloadBytes: 2, LogicalStorageBytes: 200}))
	before := b.Snapshot(2)
	if err := b.Transfer(two, "two", Vector{PayloadBytes: 2, MainResponseOwners: 1, LogicalStorageBytes: 200}); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	assertBudgetUnchanged(t, b, 2, before)
	if err := b.Transfer(two, "two", Vector{PayloadBytes: 3, LogicalStorageBytes: 200}); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	assertBudgetUnchanged(t, b, 2, before)
	must(t, b.Transfer(one, "one", Vector{PayloadBytes: 2, LogicalStorageBytes: 100}))
	must(t, b.Transfer(two, "two", Vector{PayloadBytes: 2, MainResponseOwners: 1, LogicalStorageBytes: 300}))

	u := optionalRoot(t, Policy{})
	must(t, u.Claim(one, "max", Vector{PayloadBytes: math.MaxInt64}))
	before = u.Snapshot(2)
	if err := u.Claim(two, "overflow", Vector{PayloadBytes: 1}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	assertBudgetUnchanged(t, u, 2, before)
	before = u.Snapshot(1)
	if err := u.Release(one, "max", Vector{LogicalStorageBytes: 1}); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
	assertBudgetUnchanged(t, u, 1, before)
	if err := u.Transfer(two, "max", Vector{}); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
	assertBudgetUnchanged(t, u, 1, before)
	if err := u.Transfer(one, "max", Vector{PayloadBytes: -1}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	assertBudgetUnchanged(t, u, 1, before)
}

func TestOptionalBudgetPinsCacheAndIdentityLifecycle(t *testing.T) {
	b := optionalRoot(t, Policy{})
	const actors = 12
	var releases []func()
	for actor := 1; actor <= actors; actor++ {
		must(t, b.RebuildActor(context.Background(), actor, source()))
		for pin := 0; pin < 3; pin++ {
			release, err := b.PinActor(actor)
			must(t, err)
			releases = append(releases, release)
		}
		s := Scope{ActorID: actor, CallID: fmt.Sprint(actor)}
		must(t, b.Claim(s, fmt.Sprint(actor), limits(3).vector()))
		if err := b.ForgetActor(actor); !errors.Is(err, ErrBusy) {
			t.Fatal(err)
		}
	}
	if got := b.Snapshot(1); got.Instance != limits(actors*3).vector() || got.ActiveIdentities != actors || b.pinCount != actors*3 {
		t.Fatalf("unlimited roots were not recorded: %+v pins=%d", got, b.pinCount)
	}
	var wg sync.WaitGroup
	for _, release := range releases {
		for repeat := 0; repeat < 3; repeat++ {
			wg.Go(release)
		}
	}
	wg.Wait()
	for actor := 1; actor <= actors; actor++ {
		s := Scope{ActorID: actor, CallID: fmt.Sprint(actor)}
		must(t, b.Transfer(s, fmt.Sprint(actor), Vector{}))
		must(t, b.ForgetActor(actor))
	}
	if b.Snapshot(1).Instance != (Vector{}) || len(b.actors) != 0 || len(b.active) != 0 || len(b.pins) != 0 || b.pinCount != 0 {
		t.Fatal("unlimited lifecycle leaked")
	}
}

func TestOptionalBudgetPinOverflowHasNoSideEffects(t *testing.T) {
	b := optionalRoot(t, Policy{})
	must(t, b.RebuildActor(context.Background(), 1, source()))
	// A consistent count projection reaches the representation boundary without
	// creating MaxInt64 individual tokens. Real token lifetimes are tested above.
	b.pinCount, b.pins[1] = math.MaxInt64-1, math.MaxInt64-1
	release, err := b.PinActor(1)
	must(t, err)
	if b.pinCount != math.MaxInt64 || b.pins[1] != math.MaxInt64 {
		t.Fatal("last representable pin not admitted")
	}
	before := b.Snapshot(1)
	if token, err := b.PinActor(1); token != nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("overflow token=%v error=%v", token != nil, err)
	}
	assertBudgetUnchanged(t, b, 1, before)
	if b.pinCount != math.MaxInt64 || b.pins[1] != math.MaxInt64 {
		t.Fatal("overflow changed counts")
	}
	release()
	release()
	if b.pinCount != math.MaxInt64-1 || b.pins[1] != math.MaxInt64-1 {
		t.Fatal("release was not idempotent")
	}
	select {
	case <-before.Changed:
	default:
		t.Fatal("release did not notify")
	}
	// The global count must also reject a different actor whose own count fits.
	b.actors[2] = balance{}
	b.pinCount, b.pins[1], b.pins[2] = math.MaxInt64, math.MaxInt64-1, 1
	before = b.Snapshot(2)
	if token, err := b.PinActor(2); token != nil || !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	assertBudgetUnchanged(t, b, 2, before)
	if b.pinCount != math.MaxInt64 || b.pins[2] != 1 {
		t.Fatal("global overflow changed counts")
	}
	// A prepared actor with no previous pins must not gain a pin-map entry on
	// refusal. An absent actor remains an identity/preparation error instead.
	b.actors[3] = balance{}
	for _, actor := range []int{3, 4} {
		before = b.Snapshot(actor)
		actorCount, pinActors := len(b.actors), len(b.pins)
		want := ErrInvalid
		if actor == 4 {
			want = ErrActorMissing
		}
		if token, err := b.PinActor(actor); token != nil || !errors.Is(err, want) {
			t.Fatalf("actor=%d err=%v", actor, err)
		}
		assertBudgetUnchanged(t, b, actor, before)
		if len(b.actors) != actorCount || len(b.pins) != pinActors || b.pinCount != math.MaxInt64 {
			t.Fatal("pin refusal inserted an actor or pin-map entry")
		}
		if _, exists := b.pins[actor]; exists {
			t.Fatal("pin refusal retained empty pin entry")
		}
	}
}

func TestOptionalBudgetDescriptorScopes(t *testing.T) {
	for _, group := range []string{"actor", "instance"} {
		t.Run(group, func(t *testing.T) {
			p := Policy{}
			if group == "actor" {
				p.Actor.MaxQueuedDescriptors = 1
			} else {
				p.Instance.MaxQueuedDescriptors = 1
			}
			b := optionalRoot(t, p)
			must(t, b.RebuildActor(context.Background(), 1, source()))
			one, err := b.PinActor(1)
			must(t, err)
			before := b.Snapshot(1)
			if _, err := b.PinActor(1); !errors.Is(err, ErrCapacity) {
				t.Fatal(err)
			}
			assertBudgetUnchanged(t, b, 1, before)
			if group == "actor" {
				must(t, b.RebuildActor(context.Background(), 2, source()))
				two, err := b.PinActor(2)
				must(t, err)
				two()
			} else if err := b.RebuildActor(context.Background(), 2, source()); !errors.Is(err, ErrCapacity) {
				t.Fatal(err)
			}
			one()
		})
	}
}

func TestOptionalBudgetMoveAndBulkRefund(t *testing.T) {
	for _, cap := range []int64{0, 1} {
		t.Run(fmt.Sprint(cap), func(t *testing.T) {
			b := optionalRoot(t, Policy{Instance: Limits{MaxQueuedDescriptors: cap}})
			s := Scope{ActorID: 1, JobID: 1}
			must(t, b.Claim(s, "one", Vector{LogicalStorageBytes: 4}))
			before := b.Snapshot(1)
			err := b.Move(s, "one", "two", Vector{LogicalStorageBytes: 1})
			if cap > 0 {
				if !errors.Is(err, ErrCapacity) {
					t.Fatal(err)
				}
				assertBudgetUnchanged(t, b, 1, before)
			} else {
				must(t, err)
			}
			left, _ := b.Usage(s, "one")
			must(t, b.Move(s, "one", "two", left))
			if got := b.Snapshot(1); got.Instance != before.Instance || got.ActiveIdentities != 1 {
				t.Fatalf("whole move did not reuse descriptor: %+v", got)
			}
			must(t, b.Detach(s, "two"))
			must(t, b.ReleaseInventoryTotals(Vector{LogicalStorageBytes: 4}, Vector{}, map[int]Vector{1: {LogicalStorageBytes: 4}}))
			if b.Snapshot(1).Instance != (Vector{}) {
				t.Fatal("bulk refund retained stock")
			}
		})
	}
}

func TestOptionalBudgetOverageRebuildAndIndependentGrowth(t *testing.T) {
	for _, group := range []string{"actor", "instance"} {
		t.Run(group, func(t *testing.T) {
			p := Policy{}
			limited := Limits{MaxPayloadBytes: 2, MaxLogicalStorageBytes: 2, MaxPrepareOwners: 1}
			if group == "actor" {
				p.Actor = limited
			} else {
				p.Instance = limited
			}
			b := optionalRoot(t, p)
			s := Scope{ActorID: 1, JobID: 1}
			stock := Vector{PayloadBytes: 5, LogicalStorageBytes: 5}
			rows := source(Record{s, "stored", stock})
			must(t, b.Rebuild(context.Background(), rows))
			must(t, b.RebuildActor(context.Background(), 1, rows))
			must(t, b.Attach(s, "stored", stock))
			must(t, b.Transfer(s, "stored", Vector{PayloadBytes: 5, LogicalStorageBytes: 4, PrepareOwners: 1}))
			before := b.Snapshot(1)
			if err := b.Transfer(s, "stored", Vector{PayloadBytes: 4, LogicalStorageBytes: 4, PrepareOwners: 2}); !errors.Is(err, ErrCapacity) {
				t.Fatal(err)
			}
			assertBudgetUnchanged(t, b, 1, before)
			if err := b.Transfer(s, "stored", Vector{PayloadBytes: 6, LogicalStorageBytes: 3}); !errors.Is(err, ErrCapacity) {
				t.Fatal(err)
			}
			assertBudgetUnchanged(t, b, 1, before)
			must(t, b.Transfer(s, "stored", Vector{PayloadBytes: 1, LogicalStorageBytes: 1}))
			must(t, b.Transfer(s, "stored", Vector{PayloadBytes: 2, LogicalStorageBytes: 2}))
			must(t, b.Detach(s, "stored"))
			must(t, b.ReleaseInventory(s, Vector{PayloadBytes: 2, LogicalStorageBytes: 2}))
			must(t, b.ForgetActor(1))
			if b.Snapshot(1).Instance != (Vector{}) {
				t.Fatal("overage retained after refund")
			}
		})
	}
}
