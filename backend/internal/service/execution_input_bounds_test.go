package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

func inputTestRoot(t *testing.T) *resourcebudget.RootBudget {
	t.Helper()
	l := resourcebudget.Limits{MaxUnconfirmedMembers: 65536, MaxPayloadBytes: 2 << 30, MaxPrepareOwners: 8, MaxMainResponseOwners: 8, MaxAuxResponseOwners: 8, MaxBuildWorkers: 8, MaxQueuedDescriptors: 64, MaxLogicalStorageBytes: 2 << 30}
	r, err := resourcebudget.New(resourcebudget.Policy{Actor: l, Instance: l})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func inputFixture(t *testing.T) (*ent.Client, *ent.Project, *ent.Resource) {
	t.Helper()
	c := testClient(t)
	ctx := context.Background()
	u := c.User.Create().SetUsername("input-user").SetPasswordHash("unused").SaveX(ctx)
	p := c.Project.GetX(ctx, seedProject(t, c, u.ID))
	r := createTestResourceWithFormat(t, c, p.ID, "input.txt", "txt")
	return c, p, r
}
func inputContext(r *resourcebudget.RootBudget, p *ent.Project) context.Context {
	return WithExecutionInputBudget(context.Background(), r, resourcebudget.Scope{ActorID: *p.OwnerUserID, CallID: "input-test"}, DefaultExecutionInputBytes)
}

func TestExecutionInputUTF8AndDepthBeforeDocument(t *testing.T) {
	testExecutionInputUTF8AndDepthBeforeDocument(t, inputTestRoot(t))
}

func testExecutionInputUTF8AndDepthBeforeDocument(t *testing.T, r *resourcebudget.RootBudget) {
	t.Helper()
	c, p, res := inputFixture(t)
	ctx := context.Background()
	row := c.Segment.Create().SetResourceID(res.ID).SetSegmentIndex(0).SetSourceText("汉字").SaveX(ctx)
	bounded := WithExecutionInputBudget(ctx, r, resourcebudget.Scope{ActorID: *p.OwnerUserID, CallID: "bytes"}, 5)
	if _, _, err := LoadExecutionSegments(bounded, c, res.ID); !errors.Is(err, backend.ErrBuildCapacity) {
		t.Fatalf("UTF8 bytes must exceed five, got %v", err)
	}
	if got := r.Snapshot(*p.OwnerUserID).Instance; got != (resourcebudget.Vector{}) {
		t.Fatalf("oversize leaked: %+v", got)
	}
	deep := strings.Repeat(`{"a":`, 80) + `0` + strings.Repeat(`}`, 80)
	c.Segment.UpdateOneID(row.ID).SetMeta(deep).ExecX(ctx)
	bounded = inputContext(r, p)
	if _, _, err := LoadExecutionSegments(bounded, c, res.ID); !errors.Is(err, backend.ErrBuildCapacity) {
		t.Fatalf("depth must fail, got %v", err)
	}
	if got := r.Snapshot(*p.OwnerUserID).Instance; got != (resourcebudget.Vector{}) {
		t.Fatalf("decode failed with leaked credit: %+v", got)
	}
	if s := executionInputState(bounded); len(s.leases) != 0 || len(s.metadata) != 0 {
		t.Fatal("partial graph retained")
	}
}

func TestExecutionInputSnapshotLeaseLifecycle(t *testing.T) {
	testExecutionInputSnapshotLeaseLifecycle(t, inputTestRoot(t))
}

func testExecutionInputSnapshotLeaseLifecycle(t *testing.T, r *resourcebudget.RootBudget) {
	t.Helper()
	c, p, res := inputFixture(t)
	row := c.Segment.Create().SetResourceID(res.ID).SetSegmentIndex(0).SetSourceText("汉字").SetMeta(`{"nested":{"value":"x"}}`).SaveX(context.Background())
	ctx := inputContext(r, p)
	rows, l, err := LoadExecutionSegments(ctx, c, res.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != row.ID || rows[0].ResourceID == nil {
		t.Fatalf("projection: %+v", rows)
	}
	meta, err := ExecutionSegmentMeta(ctx, rows[0])
	if err != nil || meta["nested"] == nil {
		t.Fatalf("meta: %v %v", meta, err)
	}
	usage := r.Snapshot(*p.OwnerUserID).Instance
	if usage.PayloadBytes <= 0 || usage.PrepareOwners != 0 || usage.BuildWorkers != 0 {
		t.Fatalf("retained payload/stations: %+v", usage)
	}
	if err = l.RunBuild(ctx, func() error {
		u := r.Snapshot(*p.OwnerUserID).Instance
		if u.BuildWorkers != 1 {
			t.Fatalf("build not funded: %+v", u)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ExecutionInputOwner(ctx).ReleaseExecutionInputs()
	if got := r.Snapshot(*p.OwnerUserID).Instance; got != (resourcebudget.Vector{}) {
		t.Fatalf("release leaked: %+v", got)
	}
	if rows[0] != nil || len(executionInputState(ctx).metadata) != 0 {
		t.Fatal("backing still held")
	}
	l.Close()
}

func TestExecutionInputCapacityWaitEndsTransaction(t *testing.T) {
	c, p, res := inputFixture(t)
	c.Segment.Create().SetResourceID(res.ID).SetSegmentIndex(0).SetSourceText("old").SaveX(context.Background())
	r := inputTestRoot(t)
	blockScope := resourcebudget.Scope{ActorID: 999, CallID: "block"}
	if err := r.Claim(blockScope, "block", resourcebudget.Vector{PayloadBytes: 2 << 30, MainResponseOwners: 1}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(inputContext(r, p), 2*time.Second)
	defer cancel()
	ready := make(chan struct{})
	var reads atomic.Int32
	c.Segment.Intercept(ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(ctx context.Context, q ent.Query) (ent.Value, error) {
			v, err := next.Query(ctx, q)
			if reads.Add(1) == 2 {
				close(ready)
			}
			return v, err
		})
	}))
	done := make(chan error, 1)
	go func() {
		_, l, err := LoadExecutionSegments(ctx, c, res.ID)
		if l != nil {
			l.Close()
		}
		done <- err
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// The single-connection fixture exposes any read transaction retained while
	// waiting: a new SQL query cannot run until the first transaction rolls back.
	queryCtx, queryCancel := context.WithTimeout(context.Background(), time.Second)
	defer queryCancel()
	if _, err := c.Segment.Query().Count(queryCtx); err != nil {
		t.Fatalf("capacity wait retained transaction: %v", err)
	}
	if err := r.Transfer(blockScope, "block", resourcebudget.Vector{}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := r.Snapshot(*p.OwnerUserID).Instance; got != (resourcebudget.Vector{}) {
		t.Fatalf("wait lifecycle leaked: %+v", got)
	}
}

func TestExecutionInputConcurrentEditCannotGrowReadSnapshot(t *testing.T) {
	c, p, res := inputFixture(t)
	row := c.Segment.Create().SetResourceID(res.ID).SetSegmentIndex(0).SetSourceText("old").SaveX(context.Background())
	ready, proceed := make(chan struct{}), make(chan struct{})
	var reads atomic.Int32
	c.Segment.Intercept(ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(ctx context.Context, q ent.Query) (ent.Value, error) {
			v, err := next.Query(ctx, q)
			if reads.Add(1) == 2 {
				close(ready)
				<-proceed
			}
			return v, err
		})
	}))
	r := inputTestRoot(t)
	ctx := WithExecutionInputBudget(context.Background(), r, resourcebudget.Scope{ActorID: *p.OwnerUserID, CallID: "snapshot"}, 100)
	type loaded struct {
		rows  []*ent.Segment
		lease *ExecutionInputLease
		err   error
	}
	done := make(chan loaded, 1)
	go func() { rows, l, err := LoadExecutionSegments(ctx, c, res.ID); done <- loaded{rows, l, err} }()
	<-ready
	edited := make(chan error, 1)
	go func() {
		edited <- c.Segment.UpdateOneID(row.ID).SetSourceText(strings.Repeat("大", 200)).Exec(context.Background())
	}()
	close(proceed)
	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	defer got.lease.Close()
	if got.rows[0].SourceText != "old" {
		t.Fatalf("read grew after scalar check: %q", got.rows[0].SourceText)
	}
	if err := <-edited; err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadExecutionSegments(ctx, c, res.ID); !errors.Is(err, backend.ErrBuildCapacity) {
		t.Fatalf("fresh large snapshot accepted: %v", err)
	}
}

func TestExecutionInputLeaseOverflowAndRetirement(t *testing.T) {
	r := inputTestRoot(t)
	ctx := WithExecutionInputBudget(context.Background(), r, resourcebudget.Scope{ActorID: 1, CallID: "retire"}, DefaultExecutionInputBytes)
	for _, bad := range []int64{-1, math.MaxInt64} {
		if _, err := NewExecutionInputLease(ctx, bad); !errors.Is(err, backend.ErrBuildCapacity) {
			t.Fatalf("invalid ownership size %d: %v", bad, err)
		}
	}
	if _, err := ExecutionInputAllocationBound(math.MaxInt64, 1); !errors.Is(err, backend.ErrBuildCapacity) {
		t.Fatal(err)
	}
	if _, err := ExecutionInputAllocationBound(1, 65537); !errors.Is(err, backend.ErrBuildCapacity) {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		l, err := NewExecutionInputLease(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		if err = l.RetainPayload(math.MaxInt64); !errors.Is(err, backend.ErrBuildCapacity) {
			t.Fatal(err)
		}
		l.Close()
	}
	s := executionInputState(ctx)
	if len(s.leases) != 0 || s.payload != 0 {
		t.Fatalf("historical descriptors retained: %d %d", len(s.leases), s.payload)
	}
}

func TestExecutionGlossaryBoundedSnapshotAndMemoryAdd(t *testing.T) {
	c, p, _ := inputFixture(t)
	seedGlossaryEntry(t, c, p.ID, "汉字", "Chinese")
	r := inputTestRoot(t)
	ctx := inputContext(r, p)
	g, _, err := LoadExecutionGlossary(ctx, c, p, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := g.(glossary.EntriesLookup); !ok {
		t.Fatal("missing bounded lookup")
	}
	if _, ok := g.(glossary.EntryVisitor); !ok {
		t.Fatal("missing bounded visitor")
	}
	add, err := g.Add(ctx, glossary.Entry{Source: "word", Target: "字"})
	if err != nil || len(add.Added) != 1 {
		t.Fatalf("add: %v %v", add, err)
	}
	if count := c.GlossaryEntry.Query().CountX(context.Background()); count != 1 {
		t.Fatalf("preview persisted temporary entry: %d", count)
	}
	hits, err := g.Lookup(ctx, "汉字 word", "zh", "en")
	if err != nil || len(hits) != 2 {
		t.Fatalf("lookup: %v %v", hits, err)
	}
	eg := g.(*executionGlossary)
	if eg.database != nil {
		t.Fatal("memory preview acquired persistence")
	}
	ExecutionInputOwner(ctx).ReleaseExecutionInputs()
	if eg.head != nil || eg.entries != nil || r.Snapshot(*p.OwnerUserID).Instance != (resourcebudget.Vector{}) {
		t.Fatal("dictionary backing retained")
	}
}

type inputCapacityUsageRunner struct{}

func (inputCapacityUsageRunner) Run(context.Context, QuickTranslateRunnerInput) (*QuickTranslateResult, error) {
	return &QuickTranslateResult{Status: "failed", Metrics: []backend.MeterMetrics{{APICalls: 1, InputTokens: 7, OutputTokens: 11}, {APICalls: 1, InputTokens: 13, OutputTokens: 17}}}, resourcebudget.ErrCapacity
}
func TestExecutionInputCapacityErrorPersistsUsageOnce(t *testing.T) {
	svc, c, actor, _ := newQuickFixture(t)
	b := seedUserBackend(t, c, actor)
	plan := seedTranslatePlan(t, c, actor, b)
	svc.runner = inputCapacityUsageRunner{}
	output, err := svc.Translate(context.Background(), QuickTranslateInput{ActorUserID: actor, SourceText: "paid source", ExecutionPlanID: plan})
	if !errors.Is(err, resourcebudget.ErrCapacity) || output != nil {
		t.Fatalf("public error contract: %v %v", output, err)
	}
	rows := c.UsageRecord.Query().AllX(context.Background())
	if len(rows) != 1 || rows[0].APICalls != 2 || rows[0].InputTokens != 20 || rows[0].OutputTokens != 28 {
		t.Fatalf("actual main+aux metrics duplicated/lost: %+v", rows)
	}
}

func TestExecutionInputColdInventoryDoesNotWaitWithoutConsumer(t *testing.T) {
	c, p, res := inputFixture(t)
	c.Segment.Create().SetResourceID(res.ID).SetSegmentIndex(0).SetSourceText("small").SaveX(context.Background())
	r := inputTestRoot(t)
	s := resourcebudget.Scope{ActorID: 99, CallID: "cold"}
	if err := r.Claim(s, "cold-stock", resourcebudget.Vector{PayloadBytes: 2 << 30}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(inputContext(r, p), time.Second)
	defer cancel()
	if _, _, err := LoadExecutionSegments(ctx, c, res.ID); !errors.Is(err, resourcebudget.ErrCapacity) || ctx.Err() != nil {
		t.Fatalf("cold stock waited for invented consumer: %v %v", err, ctx.Err())
	}
	if err := r.Transfer(s, "cold-stock", resourcebudget.Vector{}); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionInputHTTPFrameHasLeaseAndNoIOBuildOwner(t *testing.T) {
	r := inputTestRoot(t)
	ctx := WithExecutionInputBudget(context.Background(), r, resourcebudget.Scope{ActorID: 1, CallID: "frame"}, DefaultExecutionInputBytes)
	raw, l, err := MarshalExecutionOutput(ctx, map[string]any{"text": "<ruby>&中文"})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]string
	if err = json.Unmarshal(raw, &decoded); err != nil || decoded["text"] != "<ruby>&中文" {
		t.Fatalf("encoded frame changed value: %s %v", raw, err)
	}
	u := r.Snapshot(1).Instance
	if u.PayloadBytes <= int64(len(raw)) || u.BuildWorkers != 0 || u.PrepareOwners != 0 {
		t.Fatalf("frame backing/CPU lifecycle: %+v", u)
	}
	raw = nil
	l.Close()
	if r.Snapshot(1).Instance != (resourcebudget.Vector{}) {
		t.Fatal("HTTP frame lease leaked")
	}
}
