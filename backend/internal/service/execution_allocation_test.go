package service

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
	"github.com/MeowSalty/LinguaFlow/backend/internal/preview"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

func TestExecutionCollectorPaidSourceMovesActualBackingAtFullRoot(t *testing.T) {
	const response int64 = 64 << 10
	const maximum int64 = 1 << 20
	root, err := resourcebudget.New(resourcebudget.Policy{Actor: resourcebudget.Limits{MaxPayloadBytes: maximum}, Instance: resourcebudget.Limits{MaxPayloadBytes: maximum}})
	if err != nil {
		t.Fatal(err)
	}
	scope := resourcebudget.Scope{ActorID: 7, CallID: "collector"}
	ctx := WithExecutionInputBudget(t.Context(), root, scope, DefaultExecutionInputBytes)
	lease, err := NewExecutionInputLease(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	admission, err := backend.NewRequestAdmission(nil, backend.RequestAdmissionConfig{MainConcurrency: map[int]int{0: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Close()
	if err = admission.AttachRootBudget(root, scope); err != nil {
		t.Fatal(err)
	}
	if err = admission.ConfigurePipelineBudget(backend.PipelineBudgetLimits{Segments: 1, Candidates: response, Item: response, Responses: response, Handoff: maximum}); err != nil {
		t.Fatal(err)
	}
	if _, err = admission.PrepareMembers("collector", 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	defer admission.ReleaseBudgetEntry("collector")
	if ready, err := admission.PrepareMain("collector", 0, 1, 0, 0, 0, false); err != nil || !ready {
		t.Fatalf("prepare=%t err=%v", ready, err)
	}
	permit, wait, err := admission.TryAdmit(ctx, backend.RequestAdmissionIntent{RequestID: "collector", RoundIndex: 0, ResourceID: 1, Stage: backend.RequestStageMain}, nil)
	if err != nil || permit == nil {
		t.Fatalf("admit wait=%+v err=%v", wait, err)
	}
	defer permit.Release()
	producer := backend.WithBuildOwnership(ctx, admission, "collector-source")
	producer = backend.WithBuildReservation(producer, func(n int64) error { return admission.ClaimBuild("collector-source", n) }, func(n int64) { _ = admission.ReleaseBuildBytes("collector-source", n) })
	producer = backend.WithProviderResponseOwnership(producer, admission, "collector", backend.RequestStageMain)
	producer, err = backend.WithProviderResponseAllocation(producer, response)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = backend.CloseProviderResponseAllocation(producer) }()
	pressure := resourcebudget.Scope{ActorID: 7, CallID: "collector-pressure"}
	if err = root.Claim(pressure, "collector-pressure", resourcebudget.Vector{PayloadBytes: maximum - root.Snapshot(7).Instance.PayloadBytes}); err != nil {
		t.Fatal(err)
	}
	defer root.Transfer(pressure, "collector-pressure", resourcebudget.Vector{})
	collector := preview.NewBoundedMemoryCollector(ctx, preview.CollectorBudget{Reserve: lease.RetainPayload, Release: lease.ReleasePayload, RunBuild: lease.RunBuild, AdoptAllocation: lease.AdoptRetainedAllocation})
	if err = lease.RetainBacking(func() { _ = collector.Close() }); err != nil {
		t.Fatal(err)
	}
	event := progress.BatchEvent{Stage: "translate", SegmentIDs: []string{"one"}, JSONSchema: map[string]any{"field": "old"}}
	want, err := backend.ValueAllocationBytes([]progress.BatchEvent{event})
	if err != nil {
		t.Fatal(err)
	}
	collector.OnBatchEventContext(producer, event)
	if err = collector.Err(); err != nil {
		t.Fatal(err)
	}
	if root.Snapshot(7).Instance.PayloadBytes != maximum {
		t.Fatal("paid transfer changed full Root")
	}
	event.SegmentIDs[0], event.JSONSchema["field"] = "changed", "changed"
	if err = backend.CloseProviderResponseAllocation(producer); err != nil {
		t.Fatal(err)
	}
	permit.Release()
	admission.ReleaseBudgetEntry("collector")
	admission.Close()
	if err = root.Transfer(pressure, "collector-pressure", resourcebudget.Vector{}); err != nil {
		t.Fatal(err)
	}
	retained := root.Snapshot(7).Instance.PayloadBytes
	if retained != want || retained <= 0 {
		t.Fatalf("producer close changed collector backing=%d actual=%d", retained, want)
	}
	got := collector.Events()
	if len(got) != 1 || got[0].SegmentIDs[0] != "one" || got[0].JSONSchema["field"] != "old" {
		t.Fatal("collector borrowed mutable producer fields")
	}
	clear(got)
	got = nil
	lease.Close()
	if root.Snapshot(7).Instance != (resourcebudget.Vector{}) {
		t.Fatalf("collector retained root=%+v", root.Snapshot(7).Instance)
	}
}

func TestExecutionRetainedTransferFailurePreservesCallerBacking(t *testing.T) {
	for _, closedTarget := range []bool{false, true} {
		name := "root-conflict"
		if closedTarget {
			name = "closed-target"
		}
		t.Run(name, func(t *testing.T) {
			root, err := resourcebudget.New(resourcebudget.Policy{})
			if err != nil {
				t.Fatal(err)
			}
			targetRoot := root
			if !closedTarget {
				targetRoot, err = resourcebudget.New(resourcebudget.Policy{})
				if err != nil {
					t.Fatal(err)
				}
			}
			scope := resourcebudget.Scope{ActorID: 7, CallID: name}
			ctx := WithExecutionInputBudget(t.Context(), targetRoot, scope, DefaultExecutionInputBytes)
			lease, err := NewExecutionInputLease(ctx, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			if closedTarget {
				lease.Close()
			}
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
			before := b.Snapshot()
			if err = lease.AdoptRetainedAllocation(ctx, b, int64(cap(body))); !errors.Is(err, resourcebudget.ErrIdentity) {
				t.Fatal(err)
			}
			if body[0] != 42 || b.Snapshot() != before || admission.BuildBytes("source") != int64(cap(body)) || root.Snapshot(7).Instance.PayloadBytes != int64(cap(body)) {
				t.Fatal("failed transfer refunded still-referenced backing", b.Snapshot(), root.Snapshot(7))
			}
			// The failed attempt must also return the receipt rights so the
			// original caller can retry or retire its own unchanged backing.
			receipt, err := b.RetainedAllocation(int64(cap(body)))
			if err != nil {
				t.Fatal(err)
			}
			if err = receipt.ReturnToBuild(); err != nil {
				t.Fatal(err)
			}
			receipt.Release()
			if root.Snapshot(7).Instance.PayloadBytes != int64(cap(body)) {
				t.Fatal("returned receipt refunded twice")
			}
			body = nil
			b.CloseRetired()
			if root.Snapshot(7).Instance != (resourcebudget.Vector{}) || targetRoot.Snapshot(7).Instance != (resourcebudget.Vector{}) {
				t.Fatal("destroyed backing retained capacity")
			}
		})
	}
}

func TestExecutionOutputOptionalAllocationPreservesFiniteLease(t *testing.T) {
	for _, limit := range []int64{0, 1, 1 << 20} {
		r, err := resourcebudget.New(resourcebudget.Policy{MaxBuildAllocationBytes: limit})
		if err != nil {
			t.Fatal(err)
		}
		ctx := WithExecutionInputBudget(context.Background(), r, resourcebudget.Scope{ActorID: 1, CallID: "allocation"}, DefaultExecutionInputBytes)
		raw, lease, err := MarshalExecutionOutput(ctx, struct{ Text string }{Text: "body"})
		if limit == 1 {
			if !errors.Is(err, backend.ErrBuildCapacity) || raw != nil || lease != nil || r.Snapshot(1).Instance != (resourcebudget.Vector{}) {
				t.Fatalf("limit failure retained body: %v %+v", err, r.Snapshot(1))
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		credit, err := backend.AllocationBytes(int64(cap(raw)), 1)
		if err != nil {
			t.Fatal(err)
		}
		if credit <= 0 || r.Snapshot(1).Instance.PayloadBytes != credit {
			t.Fatalf("output has no finite live lease: %+v", r.Snapshot(1))
		}
		clear(raw)
		raw = nil
		lease.Close()
		if r.Snapshot(1).Instance != (resourcebudget.Vector{}) {
			t.Fatal("output lease leaked")
		}
	}
}

func TestExecutionCapacityFailureLogsAndPublishesCoordinates(t *testing.T) {
	broker := event.NewBroker(nil)
	env := newJobRoundTestEnv(t, broker)
	job, resources, _ := seedJobWithRounds(t, env, JobStatusRunning, 1, 0, []jobResourceSpec{{status: JobResourceStatusRunning, segmentCount: 1}})
	ch := broker.Subscribe(job.ID)
	defer broker.Unsubscribe(job.ID, ch)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	err := env.svc.MarkJobResourceFailed(context.Background(), job.ID, resources[0].ID, &backend.AllocationCapacityError{Used: 3, Requested: 4, Limit: 6})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case failure := <-ch:
		if failure.Type != "resource_failed" || failure.Stage != "job_resource" || failure.Metadata["dimension"] != "allocation_bytes" || failure.Metadata["used"] != int64(3) || failure.Metadata["requested"] != int64(4) || failure.Metadata["limit"] != int64(6) || failure.Metadata["owner"] == "" {
			t.Fatalf("incomplete capacity event: %+v", failure)
		}
	default:
		t.Fatal("capacity event missing")
	}
	for _, field := range []string{`"stage":"job_resource"`, `"dimension":"allocation_bytes"`, `"used":3`, `"requested":4`, `"limit":6`, `"owner":`} {
		if !strings.Contains(logs.String(), field) {
			t.Fatalf("capacity log missing %s: %s", field, logs.String())
		}
	}
}
