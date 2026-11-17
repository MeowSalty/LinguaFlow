package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

func TestExecutionZeroLeaseClaimsOnlyActualOwnershipAndCanRegrow(t *testing.T) {
	root, err := resourcebudget.New(resourcebudget.Policy{Actor: resourcebudget.Limits{MaxPayloadBytes: 64}, Instance: resourcebudget.Limits{MaxPayloadBytes: 64}})
	if err != nil {
		t.Fatal(err)
	}
	scope := resourcebudget.Scope{ActorID: 1, CallID: "zero-lease"}
	ctx := WithExecutionInputBudget(t.Context(), root, scope, DefaultExecutionInputBytes)
	changed := root.Changed()
	lease, err := NewExecutionInputLease(ctx, 0)
	if err != nil {
		t.Fatal("legal empty lease rejected", err)
	}
	defer lease.Close()
	if lease.usage != (resourcebudget.Vector{}) || root.Snapshot(1).Instance != (resourcebudget.Vector{}) || root.Changed() != changed {
		t.Fatal("empty initialization created Root ownership")
	}
	if err = root.Claim(scope, "empty-claim", resourcebudget.Vector{}); !errors.Is(err, resourcebudget.ErrInvalid) {
		t.Fatal("Root empty Claim contract changed", err)
	}
	if err = lease.RetainPayload(0); err != nil {
		t.Fatal(err)
	}
	if err = lease.ReleasePayload(0); err != nil {
		t.Fatal(err)
	}
	if err = lease.Prepare(false); err != nil {
		t.Fatal(err)
	}
	if root.Changed() != changed {
		t.Fatal("empty operations mutated Root")
	}
	if err = lease.RunBuild(ctx, func() error {
		if root.Snapshot(1).Instance != (resourcebudget.Vector{BuildWorkers: 1}) {
			t.Fatal("zero payload build used virtual bytes", root.Snapshot(1))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if root.Snapshot(1).Instance != (resourcebudget.Vector{}) {
		t.Fatal("empty build worker survived completion")
	}
	if err = lease.Prepare(true); err != nil {
		t.Fatal(err)
	}
	if root.Snapshot(1).Instance != (resourcebudget.Vector{PrepareOwners: 1}) {
		t.Fatal("actual prepare owner was not claimed")
	}
	if err = lease.Prepare(false); err != nil {
		t.Fatal(err)
	}
	if err = lease.RetainPayload(64); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 64)
	body[0] = 42
	if root.Snapshot(1).Instance != (resourcebudget.Vector{PayloadBytes: int64(cap(body))}) {
		t.Fatal("first actual payload was not claimed")
	}
	if err = lease.RetainPayload(1); !errors.Is(err, resourcebudget.ErrCapacity) || body[0] != 42 || lease.usage.PayloadBytes != 64 || root.Snapshot(1).Instance.PayloadBytes != 64 {
		t.Fatal("failed growth changed existing backing", err, root.Snapshot(1))
	}
	body = nil
	if err = lease.ReleasePayload(64); err != nil {
		t.Fatal(err)
	}
	if root.Snapshot(1).Instance != (resourcebudget.Vector{}) {
		t.Fatal("complete release kept an identity charged")
	}
	if err = lease.RetainPayload(32); err != nil {
		t.Fatal("released identity could not fund a later actual payload", err)
	}
	body = make([]byte, 32)
	if err = lease.RetainBacking(func() { clear(body); body = nil }); err != nil {
		t.Fatal(err)
	}
	lease.Close()
	if body != nil || root.Snapshot(1).Instance != (resourcebudget.Vector{}) || executionInputState(ctx).payload != 0 || len(executionInputState(ctx).leases) != 0 {
		t.Fatal("zero lease lifecycle retained backing or accounting")
	}
	if err = lease.RetainPayload(0); !errors.Is(err, resourcebudget.ErrIdentity) {
		t.Fatal("closed lease was reused", err)
	}
	if err = lease.RunBuild(ctx, func() error { t.Fatal("closed lease executed a build"); return nil }); !errors.Is(err, resourcebudget.ErrIdentity) {
		t.Fatal(err)
	}
}

func TestExecutionZeroLeasePreservesRootIdentityValidationAndEmptyClose(t *testing.T) {
	for _, scope := range []resourcebudget.Scope{
		{CallID: "actor-zero"},
		{ActorID: 1},
		{ActorID: 1, JobID: 1, CallID: "both"},
		{ActorID: 1, JobID: -1},
		{ActorID: 1, CallID: strings.Repeat("x", 4097)},
	} {
		root, err := resourcebudget.New(resourcebudget.Policy{})
		if err != nil {
			t.Fatal(err)
		}
		ctx := WithExecutionInputBudget(t.Context(), root, scope, DefaultExecutionInputBytes)
		changed := root.Changed()
		if lease, err := NewExecutionInputLease(ctx, 0); lease != nil || !errors.Is(err, resourcebudget.ErrIdentity) {
			t.Fatal("empty lease bypassed scope validation", lease, err)
		}
		if root.Changed() != changed {
			t.Fatal("invalid zero claim changed Root")
		}
	}
	root, err := resourcebudget.New(resourcebudget.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithExecutionInputBudget(context.Background(), root, resourcebudget.Scope{ActorID: 1, CallID: "empty-close"}, DefaultExecutionInputBytes)
	changed := root.Changed()
	lease, err := NewExecutionInputLease(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	lease.Close()
	lease.Close()
	if root.Changed() != changed || root.Snapshot(1).Instance != (resourcebudget.Vector{}) {
		t.Fatal("empty close submitted a Root mutation")
	}
}

func TestExecutionZeroLeaseAdoptsActualBackingWithoutInitialClaim(t *testing.T) {
	root, err := resourcebudget.New(resourcebudget.Policy{Instance: resourcebudget.Limits{MaxPayloadBytes: 64}})
	if err != nil {
		t.Fatal(err)
	}
	scope := resourcebudget.Scope{ActorID: 1, CallID: "zero-adopt"}
	ctx := WithExecutionInputBudget(t.Context(), root, scope, DefaultExecutionInputBytes)
	lease, err := NewExecutionInputLease(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	admission, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Close()
	if err = admission.AttachRootBudget(root, scope); err != nil {
		t.Fatal(err)
	}
	ctx = backend.WithBuildOwnership(ctx, admission, "source")
	ctx = backend.WithBuildReservation(ctx, func(n int64) error { return admission.ClaimBuild("source", n) }, func(n int64) {
		if err := admission.ReleaseBuildBytes("source", n); err != nil {
			t.Error(err)
		}
	})
	b, err := backend.NewBuildBoundsFromContext(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.CloseRetired()
	if err = b.ReserveAllocation(64, 1); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 64)
	body[0] = 42
	if err = lease.AdoptRetainedAllocation(ctx, b, int64(cap(body))); err != nil {
		t.Fatal("empty target could not receive its actual receipt", err)
	}
	if b.Snapshot().LiveBytes != 0 || root.Snapshot(1).Instance.PayloadBytes != int64(cap(body)) || lease.usage.PayloadBytes != int64(cap(body)) {
		t.Fatal("adoption duplicated or lost original backing", b.Snapshot(), root.Snapshot(1))
	}
	if err = lease.RetainBacking(func() { clear(body); body = nil }); err != nil {
		t.Fatal(err)
	}
	b.CloseRetired()
	if body[0] != 42 || root.Snapshot(1).Instance.PayloadBytes != 64 {
		t.Fatal("source close refunded receiver backing")
	}
	lease.Close()
	if body != nil || root.Snapshot(1).Instance != (resourcebudget.Vector{}) {
		t.Fatal("adopted backing did not drain", root.Snapshot(1))
	}
}
