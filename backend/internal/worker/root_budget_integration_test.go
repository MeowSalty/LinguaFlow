package worker

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

func TestBudgetIntegrationRebuildMarkUnknownAttachMetadata(t *testing.T) {
	f := newRubyPipelineFixture(t, 2)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	seg := f.client.Segment.Query().Where(segment.ResourceIDEQ(f.resourceIDs[0])).FirstX(ctx)
	f.client.Job.UpdateOneID(f.jobID).SetStatus("running").ExecX(ctx)
	old := f.client.WorkRequest.Create().SetIdentity("unresolved-metadata-attempt").SetRetryEpoch(0).SetJobID(f.jobID).SetResourceID(f.resourceIDs[0]).SetJobRoundID(f.roundIDs[0]).SetSegmentIds([]int{seg.ID}).SetStage("alignment").SetBackendID(f.snapshot.RubyRetry.Backend.ID).SetBudgetModel("stage_separated").SetInputDigest("unresolved").SaveX(ctx)
	limits := resourcebudget.Limits{MaxUnconfirmedMembers: 4096, MaxPayloadBytes: 1 << 30, MaxPrepareOwners: 16, MaxMainResponseOwners: 4, MaxAuxResponseOwners: 16, MaxBuildWorkers: 16, MaxQueuedDescriptors: 256, MaxLogicalStorageBytes: 1 << 30}
	root, err := resourcebudget.New(resourcebudget.Policy{Actor: limits, Instance: limits})
	if err != nil {
		t.Fatal(err)
	}
	source := workstate.InventorySource{DB: f.client, Driver: "sqlite", ItemBytes: f.runner.pipeCfg.Candidates.ItemBytes}
	if err = root.Rebuild(ctx, source); err != nil {
		t.Fatal(err)
	}
	before := root.Snapshot(0).Instance
	if before.PayloadBytes == 0 {
		t.Fatal("unresolved attempt member metadata was not rebuilt")
	}
	if err = f.jobs.PrepareRecovery(ctx); err != nil {
		t.Fatal(err)
	}
	if f.client.WorkRequest.GetX(ctx, old.ID).State != "unknown" {
		t.Fatal("actual startup recovery did not mark the attempt unknown")
	}
	v, storage, err := source.Job(ctx, f.jobID)
	if err != nil {
		t.Fatal(err)
	}
	v.LogicalStorageBytes = storage
	if v != before {
		t.Fatalf("MarkUnknown changed rebuilt inventory: before=%+v after=%+v", before, v)
	}
	f.runner.SetRootBudget(root, func(ctx context.Context, actor int) error {
		return service.PrepareExecutionActor(ctx, func() (func(), error) {
			as := source
			as.ActorID = actor
			if err := root.RebuildActor(ctx, actor, as); err != nil {
				return nil, err
			}
			release, err := root.PinActor(actor)
			if err != nil {
				return nil, err
			}
			return func() { release(); _ = root.ForgetActor(actor) }, nil
		})
	})
	f.runner.SetBudgetSource(source, nil)
	workstate.InstallStorageHooks(f.client)
	if err = f.runner.processJob(ctx, f.jobID); err != nil {
		t.Fatal(err)
	}
	f.assertAccepted(t, 0, 2)
	v, storage, err = source.Job(ctx, f.jobID)
	if err != nil {
		t.Fatal(err)
	}
	v.LogicalStorageBytes = storage
	if v.PayloadBytes != 0 || root.Snapshot(0).Instance != v {
		t.Fatalf("joined completed metadata was not audit-only: source=%+v root=%+v", v, root.Snapshot(0).Instance)
	}
	t.Logf("startup Rebuild P=%d -> actual PrepareRecovery MarkUnknown unchanged -> Attach/real HTTP -> joined completed P=0, audit LS=%d", before.PayloadBytes, storage)
}

func TestBudgetIntegrationPaidAuxSaveWithFullPayloadRoot(t *testing.T) {
	testBudgetPaidAuxSave(t, false)
}

func TestBudgetIntegrationPaidAuxSaveWithFullPayloadAndStorageRoot(t *testing.T) {
	testBudgetPaidAuxSave(t, true)
}

func testBudgetPaidAuxSave(t *testing.T, fillStorage bool) {
	f := newRubyPipelineFixture(t, 2)
	f.snapshot.RubyRetry.Concurrency = 2
	f.persistSnapshot(t)
	limits := resourcebudget.Limits{MaxUnconfirmedMembers: 4096, MaxPayloadBytes: 1 << 30, MaxPrepareOwners: 16, MaxMainResponseOwners: 4, MaxAuxResponseOwners: 16, MaxBuildWorkers: 16, MaxQueuedDescriptors: 256, MaxLogicalStorageBytes: 1 << 30}
	root, err := resourcebudget.New(resourcebudget.Policy{Actor: limits, Instance: limits})
	if err != nil {
		t.Fatal(err)
	}
	source := workstate.InventorySource{DB: f.client, Driver: "sqlite", ItemBytes: f.runner.pipeCfg.Candidates.ItemBytes}
	f.runner.SetRootBudget(root, nil)
	f.runner.SetBudgetSource(source, nil)
	workstate.InstallStorageHooks(f.client)
	arrived, release := make(chan struct{}, 2), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	f.respond = func(ctx context.Context, request rubyPipelineRequest) (any, error) {
		if len(request.Segments) == 0 {
			arrived <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return rubyPipelineResponse(request), nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.runner.processJob(ctx, f.jobID) }()
	for range 2 {
		select {
		case <-arrived:
		case err := <-done:
			t.Fatalf("runner stopped before both paid aux: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	blocker := resourcebudget.Scope{ActorID: f.ownerID, CallID: "save-pressure"}
	used := root.Snapshot(f.ownerID).Instance.PayloadBytes
	pressure := resourcebudget.Vector{PayloadBytes: limits.MaxPayloadBytes - used}
	if fillStorage {
		pressure.LogicalStorageBytes = limits.MaxLogicalStorageBytes - root.Snapshot(f.ownerID).Instance.LogicalStorageBytes
	}
	if err := root.Claim(blocker, "save-pressure", pressure); err != nil {
		t.Fatal(err)
	}
	defer root.Transfer(blocker, "save-pressure", resourcebudget.Vector{})
	if root.Snapshot(f.ownerID).Instance.PayloadBytes != limits.MaxPayloadBytes {
		t.Fatal("payload root did not reach exact maximum")
	}
	if fillStorage && root.Snapshot(f.ownerID).Instance.LogicalStorageBytes != limits.MaxLogicalStorageBytes {
		t.Fatal("logical storage root did not reach exact maximum")
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("already paid aux could not save while payload root was full")
	}
	job := f.client.Job.GetX(ctx, f.jobID)
	if job.Status != "completed" {
		t.Fatalf("paid aux pressure status=%s error=%s", job.Status, *job.ErrorMessage)
	}
	f.assertAccepted(t, 0, 2)
	main, aux, _, _ := f.calls()
	if main != 1 || aux != 2 {
		t.Fatal(fmt.Sprintf("HTTP main/aux=%d/%d", main, aux))
	}
	t.Logf("actual HTTP main=1/aux=2 both aux returned with actor and instance P=%d; full storage=%t; SQL candidate save/commit consumed prepaid credit without new P/LS claim", limits.MaxPayloadBytes, fillStorage)
}

func TestBudgetIntegrationPostgresSaveBoundRejectsUnreadableRepresentation(t *testing.T) {
	physical, err := postgresCandidateStorageBound(4 << 20)
	if err != nil || physical <= 4<<20 || physical > workstate.CandidateReadBytes {
		t.Fatalf("physical maximum=%d err=%v", physical, err)
	}
	if _, err = postgresCandidateStorageBound(64 << 20); err == nil {
		t.Fatal("unreadable maximum was silently truncated")
	}
}

func TestBudgetIntegrationB11B12PreviewEntriesShareRoot(t *testing.T) {
	for _, kind := range []string{"preview", "quick", "revision_preview"} {
		t.Run(kind, func(t *testing.T) {
			f := newRubyEntryFixture(t, false)
			limits := resourcebudget.Limits{MaxUnconfirmedMembers: 4096, MaxPayloadBytes: 1 << 30, MaxPrepareOwners: 16, MaxMainResponseOwners: 1, MaxAuxResponseOwners: 16, MaxBuildWorkers: 16, MaxQueuedDescriptors: 256, MaxLogicalStorageBytes: 1 << 30}
			root, err := resourcebudget.New(resourcebudget.Policy{Actor: limits, Instance: limits})
			if err != nil {
				t.Fatal(err)
			}
			f.factory.rootBudget = root
			blocker := resourcebudget.Scope{ActorID: 7, JobID: 987}
			if err := root.Claim(blocker, "stored-blocker", resourcebudget.Vector{MainResponseOwners: 1}); err != nil {
				t.Fatal(err)
			}
			actor := 7
			if kind == "quick" {
				actor = 8
			}
			ctx, cancel := context.WithTimeout(service.WithExecutionActor(context.Background(), actor), 5*time.Second)
			defer cancel()
			ctx = service.WithExecutionInputBudget(ctx, root, service.ExecutionScope(ctx), 64<<20)
			defer service.ExecutionInputOwner(ctx).ReleaseExecutionInputs()
			done := make(chan rubyEntryResult, 1)
			go func() { done <- f.run(ctx, kind) }()
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case result := <-done:
				t.Fatalf("entry returned while shared response slot occupied: %+v", result)
			case <-timer.C:
			}
			f.mu.Lock()
			calls := len(f.requests)
			f.mu.Unlock()
			if calls != 0 {
				t.Fatalf("HTTP escaped root response owner: %d", calls)
			}
			if err := root.Release(blocker, "stored-blocker", resourcebudget.Vector{MainResponseOwners: 1}); err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-done:
				if result.err != nil || result.status == "failed" {
					t.Fatalf("entry did not drain after root release: %+v", result)
				}
			case <-ctx.Done():
				t.Fatal("entry did not wake after shared root release")
			}
			f.mu.Lock()
			calls = len(f.requests)
			f.mu.Unlock()
			if calls < 1 {
				t.Fatal("entry never reached real HTTP")
			}
			service.ExecutionInputOwner(ctx).ReleaseExecutionInputs()
			if got := root.Snapshot(7).Instance; got != (resourcebudget.Vector{}) {
				t.Fatalf("entry leaked roots: %+v", got)
			}
			t.Logf("B-T11/B-T12 kind=%s caller_actor=%d blocker_actor=7 real_HTTP_calls=%d root slot blocked every entry before HTTP", kind, actor, calls)
		})
	}
}

func TestBudgetIntegrationStoredJobACKAndRestart(t *testing.T) {
	f := newRubyPipelineFixture(t, 2)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	limits := resourcebudget.Limits{MaxUnconfirmedMembers: 4096, MaxPayloadBytes: 1 << 30, MaxPrepareOwners: 16, MaxMainResponseOwners: 1, MaxAuxResponseOwners: 16, MaxBuildWorkers: 16, MaxQueuedDescriptors: 256, MaxLogicalStorageBytes: 1 << 30}
	policy := resourcebudget.Policy{Actor: limits, Instance: limits}
	root, err := resourcebudget.New(policy)
	if err != nil {
		t.Fatal(err)
	}
	source := workstate.InventorySource{DB: f.client, Driver: "sqlite", ItemBytes: f.runner.pipeCfg.Candidates.ItemBytes}
	if err = root.Rebuild(ctx, source); err != nil {
		t.Fatal(err)
	}
	prepare := func(ctx context.Context, actor int) error {
		return service.PrepareExecutionActor(ctx, func() (func(), error) {
			actorSource := source
			actorSource.ActorID = actor
			if err := root.RebuildActor(ctx, actor, actorSource); err != nil {
				return nil, err
			}
			release, err := root.PinActor(actor)
			if err != nil {
				return nil, err
			}
			return func() { release(); _ = root.ForgetActor(actor) }, nil
		})
	}
	f.runner.SetRootBudget(root, prepare)
	f.runner.SetBudgetSource(source, nil)
	workstate.InstallStorageHooks(f.client)
	if err = f.runner.processJob(ctx, f.jobID); err != nil {
		t.Fatal(err)
	}
	f.assertAccepted(t, 0, 2)
	main, aux, _, _ := f.calls()
	if main != 1 || aux != 2 {
		t.Fatalf("actual HTTP main/aux=%d/%d", main, aux)
	}
	v, storage, err := source.Job(ctx, f.jobID)
	if err != nil {
		t.Fatal(err)
	}
	v.LogicalStorageBytes = storage
	if got := root.Snapshot(0).Instance; got != v {
		t.Fatalf("joined root differs from ACK facts root=%+v DB=%+v", got, v)
	}
	if v.UnconfirmedMembers != 0 || v.PayloadBytes != 0 || storage <= 0 {
		t.Fatalf("completed job retained transient backing or lost receipts: %+v", v)
	}
	restarted, err := resourcebudget.New(policy)
	if err != nil {
		t.Fatal(err)
	}
	if err = restarted.Rebuild(ctx, source); err != nil {
		t.Fatal(err)
	}
	if got := restarted.Snapshot(0).Instance; got != v {
		t.Fatalf("restart changed reliable accounting: %+v want %+v", got, v)
	}
	if err = f.runner.processJob(ctx, f.jobID); err != nil {
		t.Fatal(err)
	}
	if nextMain, nextAux, _, _ := f.calls(); nextMain != main || nextAux != aux {
		t.Fatal("completed replay dispatched duplicate HTTP")
	}
	t.Logf("stored Job main=%d aux=%d ACK/replay/restart logical=%d, retained P/S=0", main, aux, storage)
}

func TestBudgetIntegrationStoredJobRejectsHTTPWithoutSaveCredit(t *testing.T) {
	f := newRubyPipelineFixture(t, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	limits := resourcebudget.Limits{MaxUnconfirmedMembers: 4096, MaxPayloadBytes: 1 << 30, MaxPrepareOwners: 16, MaxMainResponseOwners: 1, MaxAuxResponseOwners: 16, MaxBuildWorkers: 16, MaxQueuedDescriptors: 256, MaxLogicalStorageBytes: 32 << 10}
	root, err := resourcebudget.New(resourcebudget.Policy{Actor: limits, Instance: limits})
	if err != nil {
		t.Fatal(err)
	}
	source := workstate.InventorySource{DB: f.client, Driver: "sqlite", ItemBytes: f.runner.pipeCfg.Candidates.ItemBytes}
	if err = root.Rebuild(ctx, source); err != nil {
		t.Fatal(err)
	}
	f.runner.SetRootBudget(root, func(ctx context.Context, actor int) error {
		actorSource := source
		actorSource.ActorID = actor
		return root.RebuildActor(ctx, actor, actorSource)
	})
	f.runner.SetBudgetSource(source, nil)
	workstate.InstallStorageHooks(f.client)
	_ = f.runner.processJob(ctx, f.jobID)
	if job := f.client.Job.GetX(ctx, f.jobID); job.Status != service.JobStatusFailed {
		t.Fatalf("missing save credit did not aggregate resource failure: %s", job.Status)
	}
	main, aux, _, _ := f.calls()
	if main != 0 || aux != 0 {
		t.Fatalf("HTTP dispatched before reliable save credit: %d/%d", main, aux)
	}
	v, storage, err := source.Job(ctx, f.jobID)
	if err != nil {
		t.Fatal(err)
	}
	v.LogicalStorageBytes = storage
	if got := root.Snapshot(0).Instance; got != v {
		t.Fatalf("unsent abort root=%+v actual=%+v", got, v)
	}
	t.Logf("maximum logical save credit rejected before real HTTP; retained facts=%+v", v)
}
