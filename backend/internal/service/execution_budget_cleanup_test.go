package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"entgo.io/ent"
	lfent "github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/job"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tasklife"
	"github.com/MeowSalty/LinguaFlow/backend/internal/workstate"
)

func seedCleanupStock(t *testing.T, ctx context.Context, c *lfent.Client, projectID, resourceID, actorID int) int {
	t.Helper()
	create := c.Job.Create().SetProjectID(projectID).SetExecutionPlanID(1).SetStatus(JobStatusFailed)
	if actorID > 0 {
		create.SetCreatedByID(actorID)
	}
	j := create.SaveX(ctx)
	jr := c.JobResource.Create().SetJobID(j.ID).SetResourceID(resourceID).SetStatus(JobResourceStatusFailed).SaveX(ctx)
	r := c.JobRound.Create().SetJobID(j.ID).SetJobResourceID(jr.ID).SetRoundIndex(0).SetMode("translate").SetStatus("failed").SetManifestSealed(true).SaveX(ctx)
	s := c.Segment.Query().Where(segment.ResourceIDEQ(resourceID)).FirstX(ctx)
	w := c.WorkItem.Create().SetJobID(j.ID).SetResourceID(resourceID).SetJobRoundID(r.ID).SetSegmentID(s.ID).SetState("candidate").SetMemberOwned(true).SaveX(ctx)
	c.WorkCandidate.Create().SetIdentity(fmt.Sprintf("candidate-%d", j.ID)).SetWorkItemID(w.ID).SetDtoVersion(1).SetSnapshotDigest("digest").SetMode("translate").SetSourceGeneration(0).SetBaselineVersion(1).SetBaselineStatus("pending").SetPayload(json.RawMessage(`{"target":"saved"}`)).SetGrowthReserved(true).SaveX(ctx)
	c.WorkRequest.Create().SetIdentity(fmt.Sprintf("request-%d", j.ID)).SetJobID(j.ID).SetResourceID(resourceID).SetJobRoundID(r.ID).SetRetryEpoch(0).SetSegmentIds([]int{s.ID}).SetStage("main").SetBackendID(0).SetBudgetModel("m").SetInputDigest("digest").SetFreezePayload([]byte("freeze")).SetResponsePayload([]byte("raw")).SetState("failed").SaveX(ctx)
	return j.ID
}

func installCleanup(t *testing.T, ctx context.Context, c *lfent.Client, projects *ProjectService, preparedActors ...int) *ExecutionBudgetCleanup {
	t.Helper()
	limits := resourcebudget.Limits{MaxUnconfirmedMembers: 10000, MaxPayloadBytes: 1 << 30, MaxPrepareOwners: 32, MaxMainResponseOwners: 32, MaxAuxResponseOwners: 32, MaxBuildWorkers: 32, MaxQueuedDescriptors: 32, MaxLogicalStorageBytes: 1 << 30}
	root, err := resourcebudget.New(resourcebudget.Policy{Actor: limits, Instance: limits})
	if err != nil {
		t.Fatal(err)
	}
	return installCleanupRoot(t, ctx, c, projects, root, preparedActors...)
}

func installCleanupRoot(t *testing.T, ctx context.Context, c *lfent.Client, projects *ProjectService, root *resourcebudget.RootBudget, preparedActors ...int) *ExecutionBudgetCleanup {
	t.Helper()
	source := workstate.InventorySource{DB: c, Driver: "sqlite", ItemBytes: 1024}
	if err := root.Rebuild(ctx, source); err != nil {
		t.Fatal(err)
	}
	for _, actor := range preparedActors {
		filtered := source
		filtered.ActorID = actor
		if err := root.RebuildActor(ctx, actor, filtered); err != nil {
			t.Fatal(err)
		}
	}
	cleanup := &ExecutionBudgetCleanup{Root: root, Source: source, InventoryMu: &sync.RWMutex{}, Lifecycle: &tasklife.Coordinator{}}
	cleanup.FreezeRegistration = cleanup.Lifecycle.FreezeRegistration
	projects.SetExecutionBudgetCleanup(cleanup)
	return cleanup
}

func assertCleanupSQL(t *testing.T, ctx context.Context, cleanup *ExecutionBudgetCleanup) {
	t.Helper()
	var want resourcebudget.Vector
	if err := cleanup.Source.Scan(ctx, func(r resourcebudget.Record) error {
		var err error
		want, err = addCleanupVector(want, r.Usage)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := cleanup.Root.Snapshot(0).Instance; got != want {
		t.Fatalf("root=%+v SQL=%+v", got, want)
	}
	for _, actor := range cleanup.Root.CachedActorIDs() {
		filtered := cleanup.Source
		filtered.ActorID = actor
		var actorWant resourcebudget.Vector
		if err := filtered.Scan(ctx, func(r resourcebudget.Record) error {
			var err error
			actorWant, err = addCleanupVector(actorWant, r.Usage)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if got := cleanup.Root.Snapshot(actor).Actor; got != actorWant {
			t.Fatalf("actor %d root=%+v SQL=%+v", actor, got, actorWant)
		}
	}
}

func TestExecutionBudgetCleanupProjectPagedActors(t *testing.T) {
	ctx, c, s, p, u, _ := storageLifecycleFixture(t)
	res := storageUpload(t, ctx, s, p, u, "cleanup.txt", "source")
	other := createTestUser(t, c, "other-budget-owner")
	for i := 0; i < 41; i++ {
		actor := u.ID
		if i%2 == 1 {
			actor = other.ID
		}
		seedCleanupStock(t, ctx, c, p.ID, res.ID, actor)
	}
	cleanup := installCleanup(t, ctx, c, s.projects, u.ID)
	if _, err := s.projects.DeleteProject(ctx, u.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	assertCleanupSQL(t, ctx, cleanup)
}

func TestExecutionBudgetCleanupOptionalBudgetProjectActors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy resourcebudget.Policy
	}{{"unlimited", resourcebudget.Policy{}}, {"overage", resourcebudget.Policy{Actor: resourcebudget.Limits{MaxPayloadBytes: 1}, Instance: resourcebudget.Limits{MaxLogicalStorageBytes: 1}}}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, c, s, p, u, _ := storageLifecycleFixture(t)
			res := storageUpload(t, ctx, s, p, u, "optional-cleanup.txt", "source")
			other := createTestUser(t, c, "other-optional-owner")
			for _, actor := range []int{u.ID, other.ID} {
				seedCleanupStock(t, ctx, c, p.ID, res.ID, actor)
			}
			root := optionalInputRoot(t, tc.policy)
			cleanup := installCleanupRoot(t, ctx, c, s.projects, root, u.ID, other.ID)
			if len(root.CachedActorIDs()) != 2 || root.Snapshot(u.ID).Instance.LogicalStorageBytes <= 1 {
				t.Fatal("fixture did not retain both cached actors and stock")
			}
			if _, err := s.projects.DeleteProject(ctx, u.ID, p.ID); err != nil {
				t.Fatal(err)
			}
			assertCleanupSQL(t, ctx, cleanup)
			if got := root.Snapshot(u.ID).Instance; got != (resourcebudget.Vector{}) {
				t.Fatalf("project deletion retained stock: %+v", got)
			}
			for _, actor := range []int{u.ID, other.ID} {
				if err := root.ForgetActor(actor); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestExecutionBudgetCleanupResourceFailedInventory(t *testing.T) {
	ctx, c, s, p, u, _ := storageLifecycleFixture(t)
	res := storageUpload(t, ctx, s, p, u, "cleanup.txt", "source")
	other := storageUpload(t, ctx, s, p, u, "keep.txt", "keep")
	seedCleanupStock(t, ctx, c, p.ID, res.ID, u.ID)
	seedCleanupStock(t, ctx, c, p.ID, other.ID, u.ID)
	cleanup := installCleanup(t, ctx, c, s.projects, u.ID)
	if err := s.DeleteResource(ctx, u.ID, p.ID, res.ID); err != nil {
		t.Fatal(err)
	}
	assertCleanupSQL(t, ctx, cleanup)
	if cleanup.Root.Snapshot(u.ID).Instance.PayloadBytes == 0 {
		t.Fatal("unrelated resource stock was refunded")
	}
}

func TestExecutionBudgetCleanupSourceUpdateFailedInventory(t *testing.T) {
	ctx, c, s, p, u, _ := storageLifecycleFixture(t)
	res := storageUpload(t, ctx, s, p, u, "cleanup.txt", "old")
	seedCleanupStock(t, ctx, c, p.ID, res.ID, u.ID)
	preview, err := s.PreviewSourceUpdate(ctx, u.ID, p.ID, res.ID, contractFile("budget-update", "cleanup.txt", "new"))
	if err != nil {
		t.Fatal(err)
	}
	cleanup := installCleanup(t, ctx, c, s.projects, u.ID)
	before := cleanup.Root.Snapshot(u.ID).Instance
	if _, _, err = s.CommitSourceUpdate(ctx, u.ID, p.ID, res.ID, preview.TaskID, preview.SourceGeneration, preview.TranslationGeneration); err != nil {
		t.Fatal(err)
	}
	assertCleanupSQL(t, ctx, cleanup)
	if after := cleanup.Root.Snapshot(u.ID).Instance; after.UnconfirmedMembers >= before.UnconfirmedMembers {
		t.Fatalf("source did not retire failed member: before=%+v after=%+v", before, after)
	}
}

func TestExecutionBudgetCleanupRollbackKeepsCredit(t *testing.T) {
	ctx, c, s, p, u, _ := storageLifecycleFixture(t)
	res := storageUpload(t, ctx, s, p, u, "cleanup.txt", "source")
	seedCleanupStock(t, ctx, c, p.ID, res.ID, u.ID)
	cleanup := installCleanup(t, ctx, c, s.projects, u.ID)
	before := cleanup.Root.Snapshot(u.ID).Instance
	sentinel := errors.New("delete rejected")
	c.Resource.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if m.Op().Is(ent.OpDelete | ent.OpDeleteOne) {
				return nil, sentinel
			}
			return next.Mutate(ctx, m)
		})
	})
	if err := s.DeleteResource(ctx, u.ID, p.ID, res.ID); !errors.Is(err, sentinel) {
		t.Fatalf("error=%v", err)
	}
	assertCleanupSQL(t, ctx, cleanup)
	if cleanup.Root.Snapshot(u.ID).Instance != before {
		t.Fatal("rollback refunded deleted-in-attempt stock")
	}
}

func TestExecutionBudgetCleanupCommittedUnknownACKRetiresExactlyOnce(t *testing.T) {
	ctx, c, s, p, u, _ := storageLifecycleFixture(t)
	res := storageUpload(t, ctx, s, p, u, "cleanup.txt", "source")
	seedCleanupStock(t, ctx, c, p.ID, res.ID, u.ID)
	cleanup := installCleanup(t, ctx, c, s.projects, u.ID)
	sentinel := errors.New("commit ACK lost")
	c.Resource.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if m.Op().Is(ent.OpDelete | ent.OpDeleteOne) {
				tx, err := m.(*lfent.ResourceMutation).Tx()
				if err != nil {
					return nil, err
				}
				tx.OnCommit(func(next lfent.Committer) lfent.Committer {
					return lfent.CommitFunc(func(ctx context.Context, tx *lfent.Tx) error {
						if err := next.Commit(ctx, tx); err != nil {
							return err
						}
						return sentinel
					})
				})
			}
			return next.Mutate(ctx, m)
		})
	})
	if err := s.DeleteResource(ctx, u.ID, p.ID, res.ID); !errors.Is(err, sentinel) {
		t.Fatalf("error=%v", err)
	}
	assertCleanupSQL(t, ctx, cleanup)
	if err := s.DeleteResource(ctx, u.ID, p.ID, res.ID); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("repeat delete=%v", err)
	}
	assertCleanupSQL(t, ctx, cleanup)
}

func TestExecutionBudgetCleanupOrphanInstanceInventory(t *testing.T) {
	ctx, c, s, p, u, _ := storageLifecycleFixture(t)
	org := c.Organization.Create().SetName("cleanup-org").SetSlug("cleanup-org").SaveX(ctx)
	c.OrgMembership.Create().SetOrganizationID(org.ID).SetUserID(u.ID).SetRole(OrgRoleOwner).SaveX(ctx)
	p = c.Project.UpdateOneID(p.ID).ClearOwnerUserID().SetOwnerOrgID(org.ID).SaveX(ctx)
	res := storageUpload(t, ctx, s, p, u, "orphan.txt", "source")
	seedCleanupStock(t, ctx, c, p.ID, res.ID, 0)
	cleanup := installCleanup(t, ctx, c, s.projects, u.ID)
	if cleanup.Root.Snapshot(u.ID).Instance.PayloadBytes == 0 || cleanup.Root.Snapshot(u.ID).Actor.PayloadBytes != 0 {
		t.Fatal("orphan was not solely instance inventory")
	}
	if _, err := s.projects.DeleteProject(ctx, u.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	assertCleanupSQL(t, ctx, cleanup)
}

func TestExecutionBudgetCleanupSourceGuardPreservesPausedStock(t *testing.T) {
	ctx, c, s, p, u, _ := storageLifecycleFixture(t)
	res := storageUpload(t, ctx, s, p, u, "cleanup.txt", "old")
	id := seedCleanupStock(t, ctx, c, p.ID, res.ID, u.ID)
	preview, err := s.PreviewSourceUpdate(ctx, u.ID, p.ID, res.ID, contractFile("budget-guard", "cleanup.txt", "new"))
	if err != nil {
		t.Fatal(err)
	}
	c.Job.UpdateOneID(id).SetStatus(JobStatusPaused).ExecX(ctx)
	cleanup := installCleanup(t, ctx, c, s.projects, u.ID)
	before := cleanup.Root.Snapshot(u.ID).Instance
	if _, _, err = s.CommitSourceUpdate(ctx, u.ID, p.ID, res.ID, preview.TaskID, preview.SourceGeneration, preview.TranslationGeneration); !errors.Is(err, ErrSourceRevisionConflict) {
		t.Fatalf("paused source update=%v", err)
	}
	assertCleanupSQL(t, ctx, cleanup)
	if cleanup.Root.Snapshot(u.ID).Instance != before {
		t.Fatal("guard rejection changed credit")
	}
}

func TestExecutionBudgetCleanupWaitsForClaimAndBlocksRegistration(t *testing.T) {
	ctx, c, s, p, u, _ := storageLifecycleFixture(t)
	res := storageUpload(t, ctx, s, p, u, "cleanup.txt", "source")
	id := seedCleanupStock(t, ctx, c, p.ID, res.ID, u.ID)
	cleanup := installCleanup(t, ctx, c, s.projects, u.ID)
	request := c.WorkRequest.Query().Where(workrequest.JobIDEQ(id)).OnlyX(ctx)
	guard, err := cleanup.Lifecycle.LockRegistration(ctx, "translation", id)
	if err != nil {
		t.Fatal(err)
	}
	joined := guard.Claim()
	guard.Release()
	scope := resourcebudget.Scope{ActorID: u.ID, JobID: id}
	runtime, storage, err := cleanup.Source.Job(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err = cleanup.Root.Attach(scope, workstate.RuntimeIdentity(id), runtime); err != nil {
		t.Fatal(err)
	}
	if err = cleanup.Root.Attach(scope, workstate.StorageIdentity(id), resourcebudget.Vector{LogicalStorageBytes: storage}); err != nil {
		t.Fatal(err)
	}
	cancelled := make(chan struct{}, 1)
	cleanup.CancelTranslation = func(int) {
		select {
		case cancelled <- struct{}{}:
		default:
		}
	}
	done := make(chan error, 1)
	go func() { done <- s.DeleteResource(ctx, u.ID, p.ID, res.ID) }()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("writer not cancelled")
	}
	select {
	case err := <-done:
		t.Fatalf("deleted before join: %v", err)
	default:
	}
	blockedCtx, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if g, err := cleanup.Lifecycle.LockRegistration(blockedCtx, "translation", id+100); !errors.Is(err, context.DeadlineExceeded) {
		if g != nil {
			g.Release()
		}
		t.Fatalf("registration escaped deletion fence: %v", err)
	}
	// Existing worker completion controls and SQL readers must still run while
	// deletion awaits their claim. Runtime and storage detach precede join.
	if !cleanup.InventoryMu.TryRLock() {
		t.Fatal("deletion locked inventory before writer join")
	}
	cleanup.InventoryMu.RUnlock()
	completion, err := cleanup.Lifecycle.Lock(ctx, "translation", id)
	if err != nil {
		t.Fatal(err)
	}
	c.Job.UpdateOneID(id).SetStatus(JobStatusFailed).ExecX(ctx)
	completion.Release()
	if err = cleanup.Root.Detach(scope, workstate.RuntimeIdentity(id)); err != nil {
		t.Fatal(err)
	}
	if err = cleanup.Root.Detach(scope, workstate.StorageIdentity(id)); err != nil {
		t.Fatal(err)
	}
	joined()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("delete did not finish after join")
	}
	assertCleanupSQL(t, ctx, cleanup)
	if c.Job.Query().Where(job.IDEQ(id)).CountX(ctx) != 1 {
		t.Fatal("resource deletion removed job history")
	}
	if _, err := c.WorkRequest.Create().SetIdentity("late-deleted-resource").SetJobID(id).SetResourceID(res.ID).SetJobRoundID(request.JobRoundID).SetRetryEpoch(0).SetSegmentIds(request.SegmentIds).SetStage("main").SetBackendID(0).SetBudgetModel("m").SetInputDigest("digest").SetResponsePayload([]byte("late body")).Save(ctx); err == nil {
		t.Fatal("late backing write accepted a deleted resource")
	}
	assertCleanupSQL(t, ctx, cleanup)
}
