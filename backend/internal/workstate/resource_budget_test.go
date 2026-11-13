package workstate

import (
	"context"
	"database/sql"
	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"fmt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type budgetUncertainDriver struct {
	dialect.Driver
	lose *atomic.Bool
}

func (d budgetUncertainDriver) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return d.Driver.(interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	}).QueryContext(ctx, q, args...)
}
func (d budgetUncertainDriver) Tx(ctx context.Context) (dialect.Tx, error) {
	tx, err := d.Driver.Tx(ctx)
	if err != nil {
		return nil, err
	}
	return budgetUncertainTx{uncertainTx{Tx: tx, lose: d.lose}}, nil
}

type budgetUncertainTx struct{ uncertainTx }

func (tx budgetUncertainTx) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return tx.Tx.(interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	}).QueryContext(ctx, q, args...)
}

func TestBudgetIntegrationB07UnknownACKChargesConfirmedFact(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "ack.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	var lose atomic.Bool
	c := ent.NewClient(ent.Driver(budgetUncertainDriver{Driver: database.NewDriver(entsql.OpenDB(dialect.SQLite, db)), lose: &lose}))
	t.Cleanup(func() { _ = c.Close(); _ = db.Close() })
	if err := c.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	_, _, store, scope, seg := seedFixture(t, ctx, c)
	currentExecution(t, ctx, c, scope)
	actor := c.User.Create().SetUsername("ack-budget").SetPasswordHash("hash").SaveX(ctx)
	c.Job.UpdateOneID(scope.JobID).SetCreatedByID(actor.ID).ExecX(ctx)
	source := InventorySource{DB: c, Driver: "sqlite", ItemBytes: 1 << 20, ActorID: actor.ID}
	root := integrationBudget(t)
	if err := root.Rebuild(ctx, source); err != nil {
		t.Fatal(err)
	}
	if err := root.RebuildActor(ctx, actor.ID, source); err != nil {
		t.Fatal(err)
	}
	s := resourcebudget.Scope{ActorID: actor.ID, JobID: scope.JobID}
	if err := root.Claim(s, RuntimeIdentity(scope.JobID), resourcebudget.Vector{PayloadBytes: 1, LogicalStorageBytes: 8 << 20}); err != nil {
		t.Fatal(err)
	}
	InstallStorageHooks(c)
	budget := &StorageBudget{Root: root, Source: source, Scope: s}
	ctx = WithStorageBudget(ctx, budget)
	r, h, _ := rawRequest(scope, seg)
	if err := store.ReserveRequest(ctx, r); err != nil {
		t.Fatal(err)
	}
	lose.Store(true)
	if err := store.HandoffResponse(ctx, h); err != nil {
		t.Fatal(err)
	}
	_, actual, err := source.Job(ctx, scope.JobID)
	if err != nil {
		t.Fatal(err)
	}
	owned, _ := root.Usage(s, StorageIdentity(scope.JobID))
	if actual != owned.LogicalStorageBytes {
		t.Fatalf("ACK loss storage=%d root=%+v", actual, owned)
	}
	before := root.Snapshot(actor.ID).Instance
	if err := store.HandoffResponse(ctx, h); err != nil {
		t.Fatal(err)
	}
	if before != root.Snapshot(actor.ID).Instance {
		t.Fatal("unknown ACK replay duplicated charge")
	}
	// Already granted request/candidate owners cover Go memory. ACK proof must
	// remain scalar even when every spare body/build/prepare/descriptor credit
	// has been claimed by other legitimate work.
	blocker := resourcebudget.Scope{ActorID: actor.ID, CallID: "ack-pressure"}
	if err := root.Claim(blocker, "ack-pressure", resourcebudget.Vector{PayloadBytes: (1 << 30) - 1, BuildWorkers: 64, PrepareOwners: 64, QueuedDescriptors: 4096}); err != nil {
		t.Fatal(err)
	}
	defer root.Transfer(blocker, "ack-pressure", resourcebudget.Vector{})
	candidate := ready(scope, seg)
	candidate.DTOVersion = 2
	candidate.ParentRequestID = r.ID
	if err := store.SaveCandidate(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	candidate.Version = 2
	candidate.Payload = []byte(`{"version":2}`)
	lose.Store(true)
	if err := store.SaveCandidate(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	charged := root.Snapshot(actor.ID).Instance
	if err := store.SaveCandidate(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	if charged != root.Snapshot(actor.ID).Instance {
		t.Fatal("scalar candidate ACK/replay changed root accounting")
	}
	t.Log("candidate successor lost ACK then same-version replay verified at full P/Build/Prepare/Q without loading old body")
	t.Logf("B-T07 unknown ACK stable actual=%d", actual)
}

func TestBudgetIntegrationInventoryBarrierBlocksWriter(t *testing.T) {
	ctx, c, store, scope, seg, source, actor := budgetFixture(t)
	root := integrationBudget(t)
	if err := root.Rebuild(ctx, source); err != nil {
		t.Fatal(err)
	}
	source.ActorID = actor.ActorID
	var inventory sync.RWMutex
	inventory.Lock()
	budget := &StorageBudget{Root: root, Source: source, Scope: actor, InventoryLock: inventory.RLocker()}
	InstallStorageHooks(c)
	writerDone := make(chan error, 1)
	writerStarted := make(chan struct{})
	r, _, _ := rawRequest(scope, seg)
	go func() { close(writerStarted); writerDone <- store.ReserveRequest(WithStorageBudget(ctx, budget), r) }()
	<-writerStarted
	select {
	case err := <-writerDone:
		inventory.Unlock()
		t.Fatalf("writer crossed rebuild barrier: %v", err)
	case <-time.After(40 * time.Millisecond):
	}
	if err := root.RebuildActor(ctx, actor.ActorID, source); err != nil {
		inventory.Unlock()
		t.Fatal(err)
	}
	if err := root.Claim(actor, RuntimeIdentity(scope.JobID), resourcebudget.Vector{PayloadBytes: 1, LogicalStorageBytes: 8 << 20}); err != nil {
		inventory.Unlock()
		t.Fatal(err)
	}
	inventory.Unlock()
	select {
	case err := <-writerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("writer did not resume")
	}
	_, actual, err := source.Job(ctx, scope.JobID)
	if err != nil {
		t.Fatal(err)
	}
	owned, _ := root.Usage(actor, StorageIdentity(scope.JobID))
	if owned.LogicalStorageBytes != actual {
		t.Fatal("writer and actor scan lost storage credit")
	}
}

func integrationBudget(t *testing.T) *resourcebudget.RootBudget {
	t.Helper()
	limits := resourcebudget.Limits{MaxUnconfirmedMembers: 4096, MaxPayloadBytes: 1 << 30, MaxPrepareOwners: 64, MaxMainResponseOwners: 64, MaxAuxResponseOwners: 64, MaxBuildWorkers: 64, MaxQueuedDescriptors: 4096, MaxLogicalStorageBytes: 1 << 30}
	root, err := resourcebudget.New(resourcebudget.Policy{Actor: limits, Instance: limits})
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func budgetFixture(t *testing.T) (context.Context, *ent.Client, *Store, Scope, *ent.Segment, InventorySource, resourcebudget.Scope) {
	t.Helper()
	ctx, c, store, scope, seg := fixture(t)
	currentExecution(t, ctx, c, scope)
	u := c.User.Create().SetUsername("budget-user").SetPasswordHash("hash").SaveX(ctx)
	c.Job.UpdateOneID(scope.JobID).SetCreatedByID(u.ID).ExecX(ctx)
	return ctx, c, store, scope, seg, InventorySource{DB: c, Driver: "sqlite", ItemBytes: 1 << 20}, resourcebudget.Scope{ActorID: u.ID, JobID: scope.JobID}
}

func TestBudgetIntegrationB07B08StorageACKPauseRestart(t *testing.T) {
	testBudgetIntegrationStorageACKPauseRestart(t, integrationBudget(t), integrationBudget(t))
}

func TestBudgetIntegrationOptionalBudgetStorageACKPauseRestart(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		t.Run(fmt.Sprintf("mixed_%t", mixed), func(t *testing.T) {
			policy := resourcebudget.Policy{}
			if mixed {
				policy.Actor.MaxPayloadBytes = 1 << 30
				policy.Instance.MaxPrepareOwners = 1
			}
			root, err := resourcebudget.New(policy)
			if err != nil {
				t.Fatal(err)
			}
			policy.Actor.MaxLogicalStorageBytes = 1
			policy.Instance.MaxLogicalStorageBytes = 1
			restarted, err := resourcebudget.New(policy)
			if err != nil {
				t.Fatal(err)
			}
			testBudgetIntegrationStorageACKPauseRestart(t, root, restarted)
		})
	}
}

func testBudgetIntegrationStorageACKPauseRestart(t *testing.T, root, restarted *resourcebudget.RootBudget) {
	t.Helper()
	ctx, c, store, scope, seg, source, actor := budgetFixture(t)
	if err := root.Rebuild(ctx, source); err != nil {
		t.Fatal(err)
	}
	source.ActorID = actor.ActorID
	if err := root.RebuildActor(ctx, actor.ActorID, source); err != nil {
		t.Fatal(err)
	}
	InstallStorageHooks(c)
	if err := root.Claim(actor, RuntimeIdentity(scope.JobID), resourcebudget.Vector{PayloadBytes: 1, LogicalStorageBytes: 8 << 20}); err != nil {
		t.Fatal(err)
	}
	storage := &StorageBudget{Root: root, Source: source, Scope: actor}
	ctx = WithStorageBudget(ctx, storage)
	r, h, _ := rawRequest(scope, seg)
	if err := store.ReserveRequest(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := store.HandoffResponse(ctx, h); err != nil {
		t.Fatal(err)
	}
	_, actual, err := source.Job(ctx, scope.JobID)
	if err != nil {
		t.Fatal(err)
	}
	owned, ok := root.Usage(actor, StorageIdentity(scope.JobID))
	if !ok || owned.LogicalStorageBytes != actual || actual <= 0 {
		t.Fatalf("storage actual=%d owned=%+v", actual, owned)
	}
	before := root.Snapshot(actor.ActorID).Instance
	if err := store.HandoffResponse(ctx, h); err != nil {
		t.Fatal(err)
	}
	if root.Snapshot(actor.ActorID).Instance != before {
		t.Fatal("ACK replay recounted backing")
	}
	c.Job.UpdateOneID(scope.JobID).SetStatus("paused").ExecX(ctx)
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	if root.Snapshot(actor.ActorID).Instance != before {
		t.Fatal("Pause/unload refunded backing")
	}
	if err := restarted.Rebuild(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	if got := restarted.Snapshot(0).Instance.LogicalStorageBytes; got != actual {
		t.Fatalf("restart storage=%d want=%d", got, actual)
	}
	if err := restarted.RebuildActor(ctx, actor.ActorID, source); err != nil {
		t.Fatal(err)
	}
	stock := resourcebudget.Vector{LogicalStorageBytes: actual}
	if err := restarted.Attach(actor, StorageIdentity(scope.JobID), stock); err != nil {
		t.Fatal("paused stored ownership could not be attached", err)
	}
	if err := restarted.Transfer(actor, StorageIdentity(scope.JobID), stock); err != nil {
		t.Fatal("unchanged restored ownership refused", err)
	}
	if err := restarted.Detach(actor, StorageIdentity(scope.JobID)); err != nil {
		t.Fatal(err)
	}
	if err := restarted.ForgetActor(actor.ActorID); err != nil {
		t.Fatal(err)
	}
	t.Logf("B-T07/B-T08 ACK replay stable; paused backing=%d restart backing=%d", actual, actual)
}

func TestBudgetIntegrationB11B12MissingActorAndFailedBacking(t *testing.T) {
	ctx, c, store, scope, seg := fixture(t)
	currentExecution(t, ctx, c, scope)
	r, h, _ := rawRequest(scope, seg)
	if err := store.ReserveRequest(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := store.HandoffResponse(ctx, h); err != nil {
		t.Fatal(err)
	}
	c.Job.UpdateOneID(scope.JobID).SetStatus("failed").ExecX(ctx)
	source := InventorySource{DB: c, Driver: "sqlite", ItemBytes: 1 << 20}
	root := integrationBudget(t)
	if err := root.Rebuild(ctx, source); err != nil {
		t.Fatal(err)
	}
	v := root.Snapshot(0).Instance
	if v.UnconfirmedMembers != 1 || v.PayloadBytes <= 0 || v.LogicalStorageBytes <= 0 {
		t.Fatalf("missing actor/failed inventory dropped: %+v", v)
	}
	if err := root.Attach(resourcebudget.Scope{JobID: scope.JobID}, RuntimeIdentity(scope.JobID), v); err == nil {
		t.Fatal("missing actor allowed execution")
	}
	t.Logf("B-T11/B-T12 missing actor failed stock S=%d P=%d storage=%d; execution refused", v.UnconfirmedMembers, v.PayloadBytes, v.LogicalStorageBytes)
}
