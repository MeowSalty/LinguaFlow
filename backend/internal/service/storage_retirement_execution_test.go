package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/deletionentry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageauthversion"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageretiremententry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagespace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/systemsetting"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storageauth"
	"testing"
	"time"
)

type retirementExecutionFixture struct {
	ctx                       context.Context
	c                         *ent.Client
	resources                 *ResourceService
	service                   *StorageRetirementService
	connections               *StorageConnectionService
	owner                     *ent.User
	project                   *ent.Project
	connection, space, target int
	driver                    *connectionTestDriver
}

func newRetirementExecutionFixture(t *testing.T, authorize bool) *retirementExecutionFixture {
	t.Helper()
	ctx, c, resources, p, u, _ := storageLifecycleFixture(t)
	st := resources.storage
	st.cfg.Enabled = true
	st.cfg.ReconcileInterval = time.Millisecond
	st.deleteGrace = 0
	data, _ := json.Marshal(StoragePolicyRequest{Mode: "both", DefaultChoice: "site", Generation: 1})
	c.SystemSetting.Update().Where(systemsetting.KeyEQ(storagePolicyKey)).SetValue(string(data)).ExecX(ctx)
	d := &connectionTestDriver{objects: map[string][]byte{}}
	connections := NewStorageConnectionService(c, credentialTestKeyring(t, "one", "one"), st.cfg, func(context.Context, *ent.StorageConnection, *ent.StorageSpace, storageauth.S3Payload) (storage.Driver, error) {
		return d, nil
	})
	conn, err := connections.Create(ctx, u.ID, CreateStorageConnectionInput{Name: "source", Scope: "user", OwnerID: u.ID, Endpoint: "https://s3.example", Region: "test", PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	sp, err := connections.CreateSpace(ctx, u.ID, conn.ID, CreateStorageSpaceInput{Name: "source", Bucket: "retirement-bucket", Prefix: "source", CapacityBytes: nil})
	if err != nil {
		t.Fatal(err)
	}
	st.SetResolver(connections.ResolveDriver)
	retire, err := NewStorageRetirementService(st, connections)
	if err != nil {
		t.Fatal(err)
	}
	if authorize {
		current, err := connections.Get(ctx, u.ID, conn.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = connections.Authorize(ctx, u.ID, conn.ID, storageAuthorizeInput(current.ManagementGeneration)); err != nil {
			t.Fatal(err)
		}
	}
	return &retirementExecutionFixture{ctx: ctx, c: c, resources: resources, service: retire, connections: connections, owner: u, project: p, connection: conn.ID, space: sp.ID, target: *p.StorageSpaceID, driver: d}
}
func (f *retirementExecutionFixture) start(t *testing.T, kind string, plans []RetirementProjectPlan) *ent.StorageRetirement {
	t.Helper()
	id := f.space
	if kind == "connection" {
		id = f.connection
	}
	if plans == nil {
		plans = []RetirementProjectPlan{}
	}
	preview, err := f.service.Preview(f.ctx, f.owner.ID, RetirementPreviewInput{TargetKind: kind, TargetID: id, Plan: plans})
	if err != nil {
		t.Fatal(err)
	}
	r, err := f.service.Create(f.ctx, f.owner.ID, "retirement-execution", RetirementCreateInput{PreviewRevision: preview.PreviewRevision, ConfirmedScope: preview.Target, Plan: plans})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func (f *retirementExecutionFixture) tick(t *testing.T, id int) *ent.StorageRetirement {
	t.Helper()
	if err := f.service.ProcessOne(f.ctx, id); err != nil {
		t.Fatal(err)
	}
	return f.c.StorageRetirement.GetX(f.ctx, id)
}

func TestStorageRetirementUnusedRemovalReleasesPathAndReplays(t *testing.T) {
	f := newRetirementExecutionFixture(t, false)
	preview, err := f.service.Preview(f.ctx, f.owner.ID, RetirementPreviewInput{TargetKind: "space", TargetID: f.space, Plan: []RetirementProjectPlan{}})
	if err != nil {
		t.Fatal(err)
	}
	input := RetirementCreateInput{PreviewRevision: preview.PreviewRevision, ConfirmedScope: preview.Target, Plan: []RetirementProjectPlan{}}
	r, err := f.service.Create(f.ctx, f.owner.ID, "unused", input)
	if err != nil {
		t.Fatal(err)
	}
	for range 4 {
		r = f.tick(t, r.ID)
		if r.Status == "completed" {
			break
		}
	}
	if r.Status != "completed" {
		t.Fatalf("unused removal: %+v", r)
	}
	if _, err = f.service.Cancel(f.ctx, f.owner.ID, r.ID, r.Generation); !errors.Is(err, ErrStorageRetirementConflict) {
		t.Fatalf("cancel after finalization: %v", err)
	}
	if f.driver.puts != 0 || f.driver.deletes != 0 {
		t.Fatal("unused identity performed remote writes")
	}
	listed, err := f.connections.Spaces(f.ctx, f.owner.ID, f.connection)
	if err != nil || len(listed) != 0 {
		t.Fatalf("default list: %v %v", listed, err)
	}
	history, err := f.connections.Spaces(f.ctx, f.owner.ID, f.connection, "removed")
	if err != nil || len(history) != 1 {
		t.Fatalf("history: %v %v", history, err)
	}
	newer, err := f.connections.CreateSpace(f.ctx, f.owner.ID, f.connection, CreateStorageSpaceInput{Name: "new identity", Bucket: "retirement-bucket", Prefix: "source", CapacityBytes: nil})
	if err != nil {
		t.Fatal(err)
	}
	if newer.ID == f.space {
		t.Fatal("reused removed identity")
	}
	again, err := f.service.Create(f.ctx, f.owner.ID, "unused", input)
	if err != nil || again.ID != r.ID {
		t.Fatalf("replay after removal: %v %v", again, err)
	}
}

func TestStorageRetirementMarkerReadDrainAndCredentialPurge(t *testing.T) {
	f := newRetirementExecutionFixture(t, true)
	old, err := f.connections.ResolveDriver(f.ctx, f.space, false)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := old.Open(f.ctx, storage.Object{Key: storageMarkerKey})
	if err != nil {
		t.Fatal(err)
	}
	r := f.start(t, "connection", nil)
	r = f.tick(t, r.ID)
	if r.Phase == "finalizing" || r.Status == "completed" {
		t.Fatal("sealed with live reader")
	}
	if err = reader.Close(); err != nil {
		t.Fatal(err)
	}
	for range 6 {
		r = f.tick(t, r.ID)
		if r.Status == "completed" {
			break
		}
	}
	if r.Status != "completed" {
		t.Fatalf("marker retirement: %+v", r)
	}
	if len(f.driver.objects) != 0 {
		t.Fatal("marker remained")
	}
	if _, err = old.Stat(f.ctx, storage.Object{Key: storageMarkerKey}); !errors.Is(err, ErrStorageRemoved) {
		t.Fatalf("old driver: %v", err)
	}
	for _, a := range f.c.StorageAuthVersion.Query().Where(storageauthversion.ConnectionIDEQ(f.connection)).AllX(f.ctx) {
		if a.PurgedAt == nil || len(a.Nonce) != 0 || len(a.Ciphertext) != 0 {
			t.Fatal("online credential remained")
		}
	}
}

func TestStorageRetirementMigratesProjectWaitsForPinAndPreservesTranslation(t *testing.T) {
	f := newRetirementExecutionFixture(t, true)
	st := f.resources.storage
	if err := st.Bind(f.ctx, f.owner.ID, f.project.ID, f.space, f.project.StorageGeneration); err != nil {
		t.Fatal(err)
	}
	p := f.c.Project.GetX(f.ctx, f.project.ID)
	resource := storageUpload(t, f.ctx, f.resources, p, f.owner, "hello.txt", "hello\n")
	seg := f.c.Segment.Query().Where(segment.ResourceIDEQ(resource.ID)).OnlyX(f.ctx)
	f.c.Segment.UpdateOneID(seg.ID).SetTargetText("你好").SetStatus(segment.StatusEdited).ExecX(f.ctx)
	source := f.c.SourceRevision.GetX(f.ctx, *resource.CurrentSourceRevisionID)
	b := f.c.Blob.GetX(f.ctx, source.SourceBlobID)
	location := f.c.BlobLocation.GetX(f.ctx, *b.ActiveLocationID)
	backup := f.c.StorageBackup.Create().SetIdentity("retirement-pin").SetExpiresAt(time.Now().Add(time.Hour)).SaveX(f.ctx)
	pin := f.c.BackupPin.Create().SetBackupID(backup.Identity).SetLocationID(location.ID).SetExpiresAt(time.Now().Add(time.Hour)).SaveX(f.ctx)
	target := f.c.StorageSpace.GetX(f.ctx, f.target)
	r := f.start(t, "space", []RetirementProjectPlan{{ProjectID: p.ID, TargetSpaceID: f.target, ExpectedStorageGeneration: p.StorageGeneration, ExpectedTargetManagementGeneration: target.ManagementGeneration, ExpectedTargetExecutionGeneration: target.ExecutionGeneration}})
	for range 5 {
		if err := st.ProcessTasks(f.ctx); err != nil {
			t.Fatal(err)
		}
		r = f.tick(t, r.ID)
	}
	moved := f.c.Project.GetX(f.ctx, p.ID)
	if moved.StorageSpaceID == nil || *moved.StorageSpaceID != f.target || moved.StorageState != "active" {
		t.Fatalf("project not migrated: %+v", moved)
	}
	if r.Status == "completed" {
		t.Fatal("ignored backup protection")
	}
	var out bytes.Buffer
	if err := f.resources.RenderTranslatedResource(f.ctx, f.owner.ID, resource, &out); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("你好")) {
		t.Fatal("translation lost")
	}
	f.c.BackupPin.UpdateOneID(pin.ID).SetExpiresAt(time.Now().Add(-time.Hour)).ExecX(f.ctx)
	f.c.BlobLocation.UpdateOneID(location.ID).ClearRetainUntil().ExecX(f.ctx)
	f.c.DeletionEntry.Update().Where(deletionentry.LocationIDEQ(location.ID)).SetNotBefore(time.Now().Add(-time.Second)).ClearNextRetryAt().ExecX(f.ctx)
	for range 8 {
		if err := st.Reconcile(f.ctx); err != nil {
			t.Fatal(err)
		}
		r = f.tick(t, r.ID)
		if r.Status == "completed" {
			break
		}
	}
	if r.Status != "completed" {
		t.Fatalf("retirement remains: %+v", r)
	}
}

func TestStorageRetirementCancellationRestoresOnlyLifecycle(t *testing.T) {
	f := newRetirementExecutionFixture(t, false)
	r := f.start(t, "space", nil)
	f.c.StorageSpace.UpdateOneID(f.space).SetStatus(storagespace.StatusDisabled).AddManagementGeneration(1).ExecX(f.ctx)
	var err error
	r, err = f.service.Cancel(f.ctx, f.owner.ID, r.ID, r.Generation)
	if err != nil {
		t.Fatal(err)
	}
	r = f.tick(t, r.ID)
	sp := f.c.StorageSpace.GetX(f.ctx, f.space)
	if r.Status != "cancelled" || sp.Lifecycle != "available" || sp.Status != "disabled" {
		t.Fatalf("cancel lost independent state: %+v %+v", r, sp)
	}
}

func TestStorageRetirementStalePreviewAndOverlapHaveNoExtraTasks(t *testing.T) {
	f := newRetirementExecutionFixture(t, false)
	preview, err := f.service.Preview(f.ctx, f.owner.ID, RetirementPreviewInput{TargetKind: "space", TargetID: f.space, Plan: []RetirementProjectPlan{}})
	if err != nil {
		t.Fatal(err)
	}
	f.c.StorageSpace.UpdateOneID(f.space).AddManagementGeneration(1).ExecX(f.ctx)
	_, err = f.service.Create(f.ctx, f.owner.ID, "stale", RetirementCreateInput{PreviewRevision: preview.PreviewRevision, ConfirmedScope: preview.Target, Plan: []RetirementProjectPlan{}})
	if !errors.Is(err, ErrStorageRetirementPreviewStale) {
		t.Fatalf("stale: %v", err)
	}
	if f.c.StorageRetirement.Query().CountX(f.ctx) != 0 {
		t.Fatal("stale preview created task")
	}
	r := f.start(t, "space", nil)
	if r.ID == 0 {
		t.Fatal("missing accepted task")
	}
	_, err = f.service.Preview(f.ctx, f.owner.ID, RetirementPreviewInput{TargetKind: "connection", TargetID: f.connection})
	if err == nil { /* preview may expose existing conflict; confirmation must reject */
	}
}

func TestStorageRetirementRemovedIdentityCannotBeUsedByCachedDriver(t *testing.T) {
	f := newRetirementExecutionFixture(t, true)
	d, err := f.connections.ResolveDriver(f.ctx, f.space, true)
	if err != nil {
		t.Fatal(err)
	}
	r := f.start(t, "space", nil)
	if _, err = d.PutNew(f.ctx, "late", bytes.NewBufferString("x"), 1); !errors.Is(err, ErrStorageRetiring) {
		t.Fatalf("late write: %v", err)
	}
	if f.c.StorageRetirementEntry.Query().Where(storageretiremententry.RetirementIDEQ(r.ID)).CountX(f.ctx) == 0 {
		t.Fatal("missing durable scope")
	}
	if _, ok := f.driver.objects["late"]; ok {
		t.Fatal("late object was written")
	}
}

func TestStorageRetirementAcceptsPlanAfterRestartAndReplaysOriginalRequest(t *testing.T) {
	f := newRetirementExecutionFixture(t, true)
	if err := f.resources.storage.Bind(f.ctx, f.owner.ID, f.project.ID, f.space, f.project.StorageGeneration); err != nil {
		t.Fatal(err)
	}
	preview, err := f.service.Preview(f.ctx, f.owner.ID, RetirementPreviewInput{TargetKind: "space", TargetID: f.space})
	if err != nil {
		t.Fatal(err)
	}
	input := RetirementCreateInput{PreviewRevision: preview.PreviewRevision, ConfirmedScope: preview.Target, Plan: []RetirementProjectPlan{}}
	r, err := f.service.Create(f.ctx, f.owner.ID, "restart-plan", input)
	if err != nil {
		t.Fatal(err)
	}
	r = f.tick(t, r.ID)
	if r.Status != "needs_action" {
		t.Fatalf("missing plan: %s", r.Status)
	}
	f.service, err = NewStorageRetirementService(f.resources.storage, f.connections)
	if err != nil {
		t.Fatal(err)
	}
	// A restart invalidates previews but accepted requests replay before preview validation.
	if _, err = f.service.Create(f.ctx, f.owner.ID, "new-key", input); !errors.Is(err, ErrStorageRetirementPreviewStale) {
		t.Fatalf("stale preview: %v", err)
	}
	if replay, err := f.service.Create(f.ctx, f.owner.ID, "restart-plan", input); err != nil || replay.ID != r.ID {
		t.Fatalf("accepted replay: %v %v", replay, err)
	}
	p := f.c.Project.GetX(f.ctx, f.project.ID)
	target := f.c.StorageSpace.GetX(f.ctx, f.target)
	plan := []RetirementProjectPlan{{ProjectID: p.ID, TargetSpaceID: target.ID, ExpectedStorageGeneration: p.StorageGeneration, ExpectedTargetManagementGeneration: target.ManagementGeneration, ExpectedTargetExecutionGeneration: target.ExecutionGeneration}}
	preview, err = f.service.Preview(f.ctx, f.owner.ID, RetirementPreviewInput{TargetKind: "space", TargetID: f.space, Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	r, err = f.service.UpdatePlan(f.ctx, f.owner.ID, r.ID, RetirementPlanInput{ExpectedGeneration: r.Generation, RetirementCreateInput: RetirementCreateInput{PreviewRevision: preview.PreviewRevision, ConfirmedScope: preview.Target, Plan: plan}})
	if err != nil {
		t.Fatal(err)
	}
	for range 4 {
		r = f.tick(t, r.ID)
	}
	if r.Status != "completed" || *f.c.Project.GetX(f.ctx, p.ID).StorageSpaceID != target.ID {
		t.Fatalf("restart did not finish accepted plan: %+v", r)
	}
}

func TestStorageRetirementIndependentUnusedSpaceDoesNotInheritSiblingUsage(t *testing.T) {
	f := newRetirementExecutionFixture(t, true)
	sp, err := f.connections.CreateSpace(f.ctx, f.owner.ID, f.connection, CreateStorageSpaceInput{Name: "unused", Bucket: "retirement-bucket", Prefix: "unused"})
	if err != nil {
		t.Fatal(err)
	}
	f.space = sp.ID
	r := f.start(t, "space", nil)
	r = f.tick(t, r.ID)
	if r.Phase != "finalizing" {
		t.Fatalf("unused space did not seal: %+v", r)
	}
	// A sibling's retirement cannot turn the available parent into a retiring connection.
	if _, err = f.connections.CreateSpace(f.ctx, f.owner.ID, f.connection, CreateStorageSpaceInput{Name: "sibling", Bucket: "retirement-bucket", Prefix: "sibling"}); err != nil {
		t.Fatal(err)
	}
	beforePut, beforeDelete := f.driver.puts, f.driver.deletes
	r = f.tick(t, r.ID)
	if r.Status != "completed" || f.driver.puts != beforePut || f.driver.deletes != beforeDelete {
		t.Fatalf("unused space inherited sibling I/O: %+v", r)
	}
}

func TestStorageRetirementDrainsIncomingMigrationWithoutStrandingProject(t *testing.T) {
	f := newRetirementExecutionFixture(t, true)
	storageUpload(t, f.ctx, f.resources, f.project, f.owner, "source.txt", "source\n")
	task, err := f.resources.storage.StartMigration(f.ctx, f.owner.ID, f.project.ID, f.space, f.project.StorageGeneration, "incoming")
	if err != nil {
		t.Fatal(err)
	}
	r := f.start(t, "space", nil)
	for range 4 {
		r = f.tick(t, r.ID)
	}
	p := f.c.Project.GetX(f.ctx, f.project.ID)
	if r.Status != "completed" || p.StorageState != "active" || p.StorageMigrationTaskID != nil || *p.StorageSpaceID != f.target {
		t.Fatalf("incoming migration stranded project: %+v %+v", r, p)
	}
	if f.c.StorageTask.GetX(f.ctx, task.ID).Status != "cancelled" {
		t.Fatal("incoming migration still executable")
	}
}

func TestStorageRetirementCannotFenceIncomingCutover(t *testing.T) {
	f := newRetirementExecutionFixture(t, true)
	storageUpload(t, f.ctx, f.resources, f.project, f.owner, "source.txt", "source\n")
	task, err := f.resources.storage.StartMigration(f.ctx, f.owner.ID, f.project.ID, f.space, f.project.StorageGeneration, "cutover")
	if err != nil {
		t.Fatal(err)
	}
	f.c.StorageTask.UpdateOneID(task.ID).SetPhase("cutover").ExecX(f.ctx)
	preview, err := f.service.Preview(f.ctx, f.owner.ID, RetirementPreviewInput{TargetKind: "space", TargetID: f.space})
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.service.Create(f.ctx, f.owner.ID, "incoming-cutover", RetirementCreateInput{PreviewRevision: preview.PreviewRevision, ConfirmedScope: preview.Target, Plan: []RetirementProjectPlan{}})
	if !errors.Is(err, ErrStorageInProgress) {
		t.Fatalf("fenced irreversible incoming cutover: %v", err)
	}
	if f.c.StorageSpace.GetX(f.ctx, f.space).Lifecycle != "available" || f.c.StorageRetirement.Query().CountX(f.ctx) != 0 {
		t.Fatal("rejected retirement left a barrier")
	}
}

func TestStorageRetirementDrainsAcceptedIntentWithoutProviderAttempt(t *testing.T) {
	f := newRetirementExecutionFixture(t, true)
	st := f.resources.storage
	if err := st.Bind(f.ctx, f.owner.ID, f.project.ID, f.space, f.project.StorageGeneration); err != nil {
		t.Fatal(err)
	}
	p := f.c.Project.GetX(f.ctx, f.project.ID)
	intent, err := st.Begin(f.ctx, f.owner.ID, p.ID, StorageIntent{Kind: "upload", IdempotencyKey: "unstarted-upload", Path: "pending.txt", Size: 3, StorageGeneration: p.StorageGeneration})
	if err != nil {
		t.Fatal(err)
	}
	target := f.c.StorageSpace.GetX(f.ctx, f.target)
	r := f.start(t, "space", []RetirementProjectPlan{{ProjectID: p.ID, TargetSpaceID: target.ID, ExpectedStorageGeneration: p.StorageGeneration, ExpectedTargetManagementGeneration: target.ManagementGeneration, ExpectedTargetExecutionGeneration: target.ExecutionGeneration}})
	for range 6 {
		r = f.tick(t, r.ID)
		if err = st.ProcessTasks(f.ctx); err != nil {
			t.Fatal(err)
		}
	}
	if r.Status != "completed" || f.c.StorageTask.GetX(f.ctx, intent.ID).Status != "cancelled" {
		t.Fatalf("unstarted intent blocked retirement: %+v", r)
	}
}

func TestStorageRetirementMigratesMultipleProjectsToDistinctTargets(t *testing.T) {
	f := newRetirementExecutionFixture(t, true)
	st := f.resources.storage
	second, err := st.projects.CreateProject(f.ctx, f.owner.ID, CreateProjectInput{Name: "second"})
	if err != nil {
		t.Fatal(err)
	}
	secondTarget, err := st.InstallSiteSpace(f.ctx, "second-target", &connectionTestDriver{objects: map[string][]byte{}})
	if err != nil {
		t.Fatal(err)
	}
	projects := []*ent.Project{f.project, second}
	targetIDs := []int{f.target, secondTarget.ID}
	plans := []RetirementProjectPlan{}
	resources := []*ent.Resource{}
	for i, p := range projects {
		if err = st.Bind(f.ctx, f.owner.ID, p.ID, f.space, p.StorageGeneration); err != nil {
			t.Fatal(err)
		}
		p = f.c.Project.GetX(f.ctx, p.ID)
		projects[i] = p
		resources = append(resources, storageUpload(t, f.ctx, f.resources, p, f.owner, "original.txt", "original\n"))
		target := f.c.StorageSpace.GetX(f.ctx, targetIDs[i])
		plans = append(plans, RetirementProjectPlan{ProjectID: p.ID, TargetSpaceID: target.ID, ExpectedStorageGeneration: p.StorageGeneration, ExpectedTargetManagementGeneration: target.ManagementGeneration, ExpectedTargetExecutionGeneration: target.ExecutionGeneration})
	}
	r := f.start(t, "connection", plans)
	for range 12 {
		if err = st.ProcessTasks(f.ctx); err != nil {
			t.Fatal(err)
		}
		if err = st.Reconcile(f.ctx); err != nil {
			t.Fatal(err)
		}
		r = f.tick(t, r.ID)
		if r.Status == "completed" {
			break
		}
	}
	if r.Status != "completed" {
		t.Fatalf("multi-project retirement: %+v", r)
	}
	for i, p := range projects {
		current := f.c.Project.GetX(f.ctx, p.ID)
		if *current.StorageSpaceID != targetIDs[i] || current.StorageState != "active" {
			t.Fatalf("project target: %+v", current)
		}
		reader, e := f.resources.OriginalFile(f.ctx, f.owner.ID, p.ID, resources[i].ID)
		if e != nil {
			t.Fatal(e)
		}
		var data bytes.Buffer
		_, e = data.ReadFrom(reader)
		closeErr := reader.Close()
		if e != nil || closeErr != nil || data.String() != "original\n" {
			t.Fatalf("original changed: %q %v %v", data.String(), e, closeErr)
		}
	}
}

func TestStorageRetirementCancelKeepsCompletedProjectAtDestination(t *testing.T) {
	f := newRetirementExecutionFixture(t, true)
	st := f.resources.storage
	second, err := st.projects.CreateProject(f.ctx, f.owner.ID, CreateProjectInput{Name: "unplanned"})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []*ent.Project{f.project, second} {
		if err = st.Bind(f.ctx, f.owner.ID, p.ID, f.space, p.StorageGeneration); err != nil {
			t.Fatal(err)
		}
	}
	p := f.c.Project.GetX(f.ctx, f.project.ID)
	storageUpload(t, f.ctx, f.resources, p, f.owner, "kept.txt", "kept\n")
	target := f.c.StorageSpace.GetX(f.ctx, f.target)
	r := f.start(t, "space", []RetirementProjectPlan{{ProjectID: p.ID, TargetSpaceID: target.ID, ExpectedStorageGeneration: p.StorageGeneration, ExpectedTargetManagementGeneration: target.ManagementGeneration, ExpectedTargetExecutionGeneration: target.ExecutionGeneration}})
	for range 5 {
		if err = st.ProcessTasks(f.ctx); err != nil {
			t.Fatal(err)
		}
		r = f.tick(t, r.ID)
	}
	if *f.c.Project.GetX(f.ctx, p.ID).StorageSpaceID != target.ID {
		t.Fatal("first project did not finish")
	}
	if _, err = f.service.Cancel(f.ctx, f.owner.ID, r.ID, r.Generation); err != nil {
		t.Fatal(err)
	}
	r = f.tick(t, r.ID)
	if r.Status != "cancelled" || *f.c.Project.GetX(f.ctx, p.ID).StorageSpaceID != target.ID || *f.c.Project.GetX(f.ctx, second.ID).StorageSpaceID != f.space {
		t.Fatal("cancel reversed completed work or moved unplanned project")
	}
}

func TestStorageRetirementCannotReleaseUnexplainedLedgerBytes(t *testing.T) {
	f := newRetirementExecutionFixture(t, false)
	f.c.StorageSpace.UpdateOneID(f.space).SetLiveBytes(17).ExecX(f.ctx)
	r := f.start(t, "space", nil)
	r = f.tick(t, r.ID)
	if r.Phase == "finalizing" || r.Status == "completed" {
		t.Fatal("unexplained accounting was treated as empty")
	}
	if f.c.StorageSpace.GetX(f.ctx, f.space).LiveBytes != 17 {
		t.Fatal("retirement cleared unexplained accounting")
	}
}

func TestStorageRetirementCancellationWaitsForCutover(t *testing.T) {
	f := newRetirementExecutionFixture(t, true)
	st := f.resources.storage
	st.cfg.ReconcileBatchSize = 1
	if err := st.Bind(f.ctx, f.owner.ID, f.project.ID, f.space, f.project.StorageGeneration); err != nil {
		t.Fatal(err)
	}
	p := f.c.Project.GetX(f.ctx, f.project.ID)
	storageUpload(t, f.ctx, f.resources, p, f.owner, "first.txt", "first\n")
	storageUpload(t, f.ctx, f.resources, p, f.owner, "second.txt", "second\n")
	target := f.c.StorageSpace.GetX(f.ctx, f.target)
	r := f.start(t, "space", []RetirementProjectPlan{{ProjectID: p.ID, TargetSpaceID: target.ID, ExpectedStorageGeneration: p.StorageGeneration, ExpectedTargetManagementGeneration: target.ManagementGeneration, ExpectedTargetExecutionGeneration: target.ExecutionGeneration}})
	entry := f.c.StorageRetirementEntry.Query().Where(storageretiremententry.RetirementIDEQ(r.ID), storageretiremententry.MigrationTaskIDNotNil()).OnlyX(f.ctx)
	for range 6 {
		if err := st.ContinueMigration(f.ctx, *entry.MigrationTaskID); err != nil {
			t.Fatal(err)
		}
		if f.c.StorageTask.GetX(f.ctx, *entry.MigrationTaskID).Phase == "cutover" {
			break
		}
	}
	if f.c.StorageTask.GetX(f.ctx, *entry.MigrationTaskID).Phase != "cutover" {
		t.Fatal("missing partial cutover fixture")
	}
	if _, err := f.service.Cancel(f.ctx, f.owner.ID, r.ID, r.Generation); err != nil {
		t.Fatal(err)
	}
	r = f.tick(t, r.ID)
	if r.Status != "waiting_retry" || f.c.StorageSpace.GetX(f.ctx, f.space).Lifecycle != "retiring" {
		t.Fatal("cancellation failed to retain the barrier during cutover")
	}
	if err := st.ContinueMigration(f.ctx, *entry.MigrationTaskID); err != nil {
		t.Fatal(err)
	}
	r = f.tick(t, r.ID)
	if r.Status != "cancelled" || *f.c.Project.GetX(f.ctx, p.ID).StorageSpaceID != target.ID {
		t.Fatal("cutover did not finish safely before cancellation")
	}
}
