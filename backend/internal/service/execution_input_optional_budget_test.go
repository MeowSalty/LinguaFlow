package service

import (
	"context"
	"errors"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

func optionalInputRoot(t *testing.T, p resourcebudget.Policy) *resourcebudget.RootBudget {
	t.Helper()
	r, err := resourcebudget.New(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestExecutionInputOptionalBudgetOwnPayloadPrecheck(t *testing.T) {
	for _, tc := range []struct {
		name            string
		actor, instance int64
	}{{"unlimited", 0, 0}, {"actor", 10, 0}, {"instance", 0, 10}, {"mixed", 10, 20}} {
		t.Run(tc.name, func(t *testing.T) {
			root := optionalInputRoot(t, resourcebudget.Policy{
				Actor:    resourcebudget.Limits{MaxPayloadBytes: tc.actor},
				Instance: resourcebudget.Limits{MaxPayloadBytes: tc.instance},
			})
			scope := resourcebudget.Scope{ActorID: 1, CallID: "optional-input"}
			ctx := WithExecutionInputBudget(context.Background(), root, scope, DefaultExecutionInputBytes)
			defer ExecutionInputOwner(ctx).ReleaseExecutionInputs()
			one, err := NewExecutionInputLease(ctx, 6)
			if err != nil {
				t.Fatal(err)
			}
			two, err := NewExecutionInputLease(ctx, 4)
			if err != nil {
				t.Fatal(err)
			}
			if root.Snapshot(1).Instance.PayloadBytes != 10 || executionInputState(ctx).payload != 10 {
				t.Fatal("lease payload not recorded")
			}
			before := root.Snapshot(1)
			three, err := NewExecutionInputLease(ctx, 1)
			if tc.actor > 0 || tc.instance > 0 {
				if !errors.Is(err, backend.ErrBuildCapacity) || three != nil {
					t.Fatalf("own payload must fail before root admission: %v", err)
				}
				if root.Snapshot(1) != before || len(executionInputState(ctx).leases) != 2 || executionInputState(ctx).payload != 10 {
					t.Fatal("precheck failure leaked lease or roots")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			one.Close()
			one.Close()
			two.Close()
			if three != nil {
				three.Close()
			}
			if root.Snapshot(1).Instance != (resourcebudget.Vector{}) || executionInputState(ctx).payload != 0 || len(executionInputState(ctx).leases) != 0 {
				t.Fatal("lease close leaked")
			}
		})
	}
}

func TestExecutionInputOptionalBudgetLifecycleAndRequestBounds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy resourcebudget.Policy
	}{{"unlimited", resourcebudget.Policy{}}, {"mixed", resourcebudget.Policy{Actor: resourcebudget.Limits{MaxPrepareOwners: 1}, Instance: resourcebudget.Limits{MaxBuildWorkers: 1}}}} {
		t.Run(tc.name+"/lifecycle", func(t *testing.T) {
			testExecutionInputSnapshotLeaseLifecycle(t, optionalInputRoot(t, tc.policy))
		})
		t.Run(tc.name+"/request_bounds", func(t *testing.T) {
			testExecutionInputUTF8AndDepthBeforeDocument(t, optionalInputRoot(t, tc.policy))
		})
	}
}

func TestExecutionInputOptionalBudgetRootStillMakesFinalDecision(t *testing.T) {
	root := optionalInputRoot(t, resourcebudget.Policy{Instance: resourcebudget.Limits{MaxPayloadBytes: 10}})
	other := resourcebudget.Scope{ActorID: 2, CallID: "other"}
	if err := root.Claim(other, "other", resourcebudget.Vector{PayloadBytes: 9}); err != nil {
		t.Fatal(err)
	}
	ctx := WithExecutionInputBudget(context.Background(), root, resourcebudget.Scope{ActorID: 1, CallID: "input"}, DefaultExecutionInputBytes)
	before := root.Snapshot(1)
	if lease, err := NewExecutionInputLease(ctx, 2); lease != nil || !errors.Is(err, resourcebudget.ErrCapacity) {
		t.Fatalf("another scope must be checked by root: lease=%v err=%v", lease != nil, err)
	}
	if root.Snapshot(1) != before || len(executionInputState(ctx).leases) != 0 {
		t.Fatal("failed root claim leaked")
	}
}
