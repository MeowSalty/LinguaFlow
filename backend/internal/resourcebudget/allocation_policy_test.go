package resourcebudget

import (
	"errors"
	"testing"
)

func TestBuildAllocationPolicyLeavesPayloadOwnershipIndependent(t *testing.T) {
	if _, err := New(Policy{MaxBuildAllocationBytes: -1}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	r, err := New(Policy{MaxBuildAllocationBytes: 1})
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{ActorID: 1, CallID: "independent"}
	if err = r.Claim(scope, "payload", Vector{PayloadBytes: 10}); err != nil {
		t.Fatal(err)
	}
	if got := r.Snapshot(1); got.Actor.PayloadBytes != 10 || got.Policy.MaxBuildAllocationBytes != 1 {
		t.Fatalf("policy replaced ownership: %+v", got)
	}
}

func TestCapacityFailureCarriesAtomicCoordinatesAndDrain(t *testing.T) {
	r, err := New(Policy{Actor: Limits{MaxPayloadBytes: 10}})
	if err != nil {
		t.Fatal(err)
	}
	consumer := Scope{ActorID: 1, CallID: "consumer"}
	if err = r.Claim(consumer, "consumer", Vector{PayloadBytes: 8, MainResponseOwners: 1}); err != nil {
		t.Fatal(err)
	}
	err = r.Claim(Scope{ActorID: 1, CallID: "builder"}, "builder", Vector{PayloadBytes: 3})
	var detail *CapacityError
	if !errors.As(err, &detail) || detail.Dimension != "payload_bytes" || detail.Used != 8 || detail.Requested != 3 || detail.Limit != 10 || detail.Owner != "actor:1/builder" || !detail.DrainOutside {
		t.Fatalf("incomplete coordinates: %+v %v", detail, err)
	}
	if got := r.Snapshot(1).Actor.PayloadBytes; got != 8 {
		t.Fatalf("failed admission mutated ownership: %d", got)
	}
}
