package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageauthversion"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagepathclaim"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagereservation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageretirement"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageretiremententry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagespace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagewrite"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storageauth"
)

// 第 18.6/18.7 节的专项测试:精确 marker 回收、站点防复活与受限恢复。

type finalizeFixture struct {
	ctx         context.Context
	client      *ent.Client
	resources   *ResourceService
	storage     *StorageService
	connections *StorageConnectionService
	service     *StorageRetirementService
	owner       *ent.User
	connection  *ent.StorageConnection
	space       *ent.StorageSpace
	driver      storage.Driver
}

// newFinalizeFixture 建立一个 stored BYOS 连接与空间;driver 由调用方注入,
// 以便覆盖版本化、陌生版本与丢响应等提供方语义。
func newFinalizeFixture(t *testing.T, driver storage.Driver, authorize bool) *finalizeFixture {
	t.Helper()
	ctx, client, resources, _, owner, _ := storageLifecycleFixture(t)
	st := resources.storage
	st.cfg.Enabled = true
	st.cfg.ReconcileInterval = time.Millisecond
	connections := NewStorageConnectionService(client, credentialTestKeyring(t, "one", "one"), st.cfg, func(context.Context, *ent.StorageConnection, *ent.StorageSpace, storageauth.S3Payload) (storage.Driver, error) {
		return driver, nil
	})
	conn, err := connections.Create(ctx, owner.ID, CreateStorageConnectionInput{Name: "source", Scope: ScopeUser, OwnerID: owner.ID, Endpoint: "https://s3.example", Region: "test", PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	sp, err := connections.CreateSpace(ctx, owner.ID, conn.ID, CreateStorageSpaceInput{Name: "source", Bucket: "finalize-bucket", Prefix: "source"})
	if err != nil {
		t.Fatal(err)
	}
	st.SetResolver(connections.ResolveDriver)
	service, err := NewStorageRetirementService(st, connections)
	if err != nil {
		t.Fatal(err)
	}
	if authorize {
		current, err := connections.Get(ctx, owner.ID, conn.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = connections.Authorize(ctx, owner.ID, conn.ID, storageAuthorizeInput(current.ManagementGeneration)); err != nil {
			t.Fatal(err)
		}
	}
	return &finalizeFixture{
		ctx: ctx, client: client, resources: resources, storage: st, connections: connections, service: service, owner: owner,
		connection: client.StorageConnection.GetX(ctx, conn.ID), space: client.StorageSpace.GetX(ctx, sp.ID), driver: driver,
	}
}

// beginFinalizing 直接建立已确认范围、退出屏障与 finalizing 阶段,使测试聚焦于
// finalizer 自身的语义,而不重复预检/接受流程。
func (f *finalizeFixture) beginFinalizing(t *testing.T, kind string) *ent.StorageRetirement {
	t.Helper()
	ctx := f.ctx
	sp := f.client.StorageSpace.GetX(ctx, f.space.ID)
	conn := f.client.StorageConnection.GetX(ctx, f.connection.ID)
	targetID, identity := sp.ID, sp.Identity
	if kind == "connection" {
		targetID, identity = conn.ID, fmt.Sprintf("connection-%d", conn.ID)
	}
	target := RetirementTarget{
		TargetKind: kind, TargetID: targetID, TargetIdentity: identity,
		Scope: ScopeUser, OwnerID: f.owner.ID, ConfigurationSource: "stored",
		ManagementGeneration: conn.ManagementGeneration, ExecutionGeneration: conn.ExecutionGeneration,
		Spaces: []RetirementSpaceScope{{SpaceID: sp.ID, SpaceIdentity: sp.Identity, ConnectionID: conn.ID, ManagementGeneration: sp.ManagementGeneration, ExecutionGeneration: sp.ExecutionGeneration}},
	}
	scope, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	r := f.client.StorageRetirement.Create().SetTargetKind(storageretirement.TargetKind(kind)).SetTargetID(targetID).
		SetTargetIdentity(identity).SetOwnerKind(storageretirement.OwnerKindUser).SetOwnerID(f.owner.ID).SetActorID(f.owner.ID).
		SetConfirmedScope(scope).SetIdempotencyKey("finalize-" + kind).SetRequestHash("finalize-request").
		SetPhase(storageretirement.PhaseFinalizing).SetStatus(storageretirement.StatusRunning).SetGeneration(1).SaveX(ctx)
	f.client.StorageSpace.UpdateOneID(sp.ID).SetLifecycle(storagespace.LifecycleRetiring).SetRetirementID(r.ID).
		AddManagementGeneration(1).AddExecutionGeneration(1).ExecX(ctx)
	f.client.StorageConnection.UpdateOneID(conn.ID).SetLifecycle(storageconnection.LifecycleRetiring).SetRetirementID(r.ID).
		AddExecutionGeneration(1).ExecX(ctx)
	return f.client.StorageRetirement.GetX(ctx, r.ID)
}

func (f *finalizeFixture) markerEntry(t *testing.T, id int) *ent.StorageRetirementEntry {
	t.Helper()
	entry, err := f.service.markerEntry(f.ctx, id, f.space.ID)
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

// finalizeVersionDriver 是带版本语义的提供方替身:删除必须指定确切版本,
// 并允许注入陌生版本与丢响应,以验证 finalizer 的精确性与可重放性。
type finalizeVersionDriver struct {
	mu         sync.Mutex
	objects    map[string]storage.Object
	bytes      map[string][]byte
	order      []string
	next       int
	puts       int
	deletes    int
	loseDelete bool
}

func newFinalizeVersionDriver() *finalizeVersionDriver {
	return &finalizeVersionDriver{objects: map[string]storage.Object{}, bytes: map[string][]byte{}}
}

func (d *finalizeVersionDriver) PutNew(_ context.Context, key string, r io.Reader, n int64) (storage.Object, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return storage.Object{}, err
	}
	if int64(len(b)) != n {
		return storage.Object{}, storage.ErrCorrupt
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.currentLocked(key) != "" {
		return storage.Object{}, storage.ErrExists
	}
	d.puts++
	d.next++
	version := fmt.Sprintf("v%d", d.next)
	o := storage.Object{Key: key, Version: version, Size: int64(len(b))}
	d.objects[version] = o
	d.bytes[version] = b
	d.order = append(d.order, version)
	return o, nil
}

func (d *finalizeVersionDriver) Open(_ context.Context, o storage.Object) (io.ReadCloser, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	version := o.Version
	if version == "" {
		version = d.currentLocked(o.Key)
	}
	obj, ok := d.objects[version]
	if !ok || obj.Key != o.Key || obj.DeleteMarker {
		return nil, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(bytes.Clone(d.bytes[version]))), nil
}

func (d *finalizeVersionDriver) Stat(_ context.Context, o storage.Object) (storage.Object, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if o.Version != "" {
		obj, ok := d.objects[o.Version]
		if !ok || obj.Key != o.Key {
			return storage.Object{}, storage.ErrNotFound
		}
		return obj, nil
	}
	version := d.currentLocked(o.Key)
	if version == "" {
		return storage.Object{}, storage.ErrNotFound
	}
	return d.objects[version], nil
}

func (d *finalizeVersionDriver) Delete(_ context.Context, o storage.Object) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if o.Version == "" {
		// 版本化存储桶的裸删除只会留下删除标记,绝不满足精确回收。
		return storage.ErrUnsupported
	}
	if _, ok := d.objects[o.Version]; !ok {
		return nil
	}
	d.deletes++
	delete(d.objects, o.Version)
	delete(d.bytes, o.Version)
	if d.loseDelete {
		d.loseDelete = false
		return storage.ErrUnavailable
	}
	return nil
}

func (d *finalizeVersionDriver) Versions(_ context.Context, key string) ([]storage.Object, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := []storage.Object{}
	for i := len(d.order) - 1; i >= 0; i-- {
		if o, ok := d.objects[d.order[i]]; ok && o.Key == key {
			out = append(out, o)
		}
	}
	return out, nil
}

func (d *finalizeVersionDriver) Capabilities(context.Context) (storage.Capabilities, error) {
	return storage.Capabilities{ConditionalCreate: true, Versioned: true, ExactVersions: true}, nil
}

func (d *finalizeVersionDriver) currentLocked(key string) string {
	for i := len(d.order) - 1; i >= 0; i-- {
		if o, ok := d.objects[d.order[i]]; ok && o.Key == key && !o.DeleteMarker {
			return d.order[i]
		}
	}
	return ""
}

// inject 直接写入一个提供方版本,模拟外部覆盖或此前中断留下的版本。
func (d *finalizeVersionDriver) inject(key, version string, data []byte, deleteMarker bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	o := storage.Object{Key: key, Version: version, Size: int64(len(data)), DeleteMarker: deleteMarker}
	d.objects[version] = o
	if !deleteMarker {
		d.bytes[version] = data
	}
	d.order = append(d.order, version)
}

func (d *finalizeVersionDriver) count() (int, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.puts, d.deletes
}
func TestStorageRetirementFinalizeReclaimsRegisteredMarkerAndReleasesLedger(t *testing.T) {
	base := &connectionTestDriver{objects: map[string][]byte{}}
	f := newFinalizeFixture(t, base, true)
	ctx := f.ctx
	before := base.deletes
	write := f.client.StorageWrite.Query().Where(storagewrite.SpaceIDEQ(f.space.ID), storagewrite.ObjectKeyEQ(storageMarkerKey)).OnlyX(ctx)
	if write.Phase != "committed" {
		t.Fatalf("marker write not committed: %s", write.Phase)
	}
	r := f.beginFinalizing(t, "space")
	if err := f.service.Finalize(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := base.Stat(ctx, storage.Object{Key: storageMarkerKey}); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("marker survived finalize: %v", err)
	}
	entry := f.markerEntry(t, r.ID)
	if entry == nil || entry.Status != storageretiremententry.StatusCompleted || entry.Kind != storageMarkerKind {
		t.Fatalf("marker entry not confirmed: %+v", entry)
	}
	if entry.SpaceID == nil || *entry.SpaceID != f.space.ID || entry.ProviderVersion != write.ProviderVersion {
		t.Fatalf("marker entry lost exact evidence: %+v", entry)
	}
	current := f.client.StorageWrite.GetX(ctx, write.ID)
	if current.Phase != "cleaned" || current.OutcomeUnknown {
		t.Fatalf("marker write not settled: %+v", current)
	}
	res := f.client.StorageReservation.Query().Where(storagereservation.WriteIDEQ(write.ID)).OnlyX(ctx)
	if res.State != storagereservation.StateFreed {
		t.Fatalf("marker reservation not freed: %s", res.State)
	}
	space := f.client.StorageSpace.GetX(ctx, f.space.ID)
	if space.LiveBytes != 0 || space.ReservedBytes != 0 || space.CandidateBytes != 0 || space.PendingDeleteBytes != 0 {
		t.Fatalf("marker ledger not released: %+v", space)
	}
	if space.Lifecycle != storagespace.LifecycleRemoved || space.RemovedAt == nil {
		t.Fatalf("space not removed: %+v", space)
	}
	if f.client.StoragePathClaim.Query().Where(storagepathclaim.SpaceIDEQ(f.space.ID)).ExistX(ctx) {
		t.Fatal("removed space retained its active path claim")
	}
	if got := f.client.StorageRetirement.GetX(ctx, r.ID); got.Status != storageretirement.StatusCompleted || got.Phase != storageretirement.PhaseCompleted {
		t.Fatalf("retirement not completed: %+v", got)
	}
	if base.deletes != before+1 {
		t.Fatalf("expected exactly one exact marker delete, got %d", base.deletes-before)
	}
	// 重放:同一任务再次执行不得重复远端删除或重建任何身份。
	if err := f.service.Finalize(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if base.deletes != before+1 {
		t.Fatalf("replay repeated remote deletion: %d", base.deletes-before)
	}
}

func TestStorageRetirementFinalizeBlocksOnUnknownMarkerVersion(t *testing.T) {
	d := newFinalizeVersionDriver()
	f := newFinalizeFixture(t, d, true)
	ctx := f.ctx
	_, before := d.count()
	r := f.beginFinalizing(t, "space")
	// 同一 key 上的陌生版本:既不是已登记版本,内容也不匹配本空间身份。
	d.inject(storageMarkerKey, "stray", []byte(`{"version":1,"identity":"stranger","nonce":"stranger"}`), false)
	err := f.service.Finalize(ctx, r.ID)
	if !errors.Is(err, ErrStorageRetirementDependenciesRemaining) {
		t.Fatalf("unknown marker version did not block: %v", err)
	}
	if _, after := d.count(); after != before {
		t.Fatalf("blocked reclaim deleted %d versions", after-before)
	}
	if entry := f.markerEntry(t, r.ID); entry == nil || entry.Status == storageretiremententry.StatusCompleted {
		t.Fatalf("blocked reclaim confirmed the marker: %+v", entry)
	}
	if got := f.client.StorageSpace.GetX(ctx, f.space.ID); got.Lifecycle != storagespace.LifecycleRetiring {
		t.Fatalf("blocked reclaim changed lifecycle: %s", got.Lifecycle)
	}
}

func TestStorageRetirementFinalizeRejectsUnregisteredMatchingVersions(t *testing.T) {
	for _, deleteMarker := range []bool{false, true} {
		t.Run(fmt.Sprintf("delete-marker-%t", deleteMarker), func(t *testing.T) {
			d := newFinalizeVersionDriver()
			f := newFinalizeFixture(t, d, true)
			r := f.beginFinalizing(t, "space")
			_, before := d.count()
			d.inject(storageMarkerKey, "unregistered", storageMarkerBytes(f.space), deleteMarker)
			if err := f.service.Finalize(f.ctx, r.ID); !errors.Is(err, ErrStorageRetirementDependenciesRemaining) {
				t.Fatalf("unregistered exact location admitted: %v", err)
			}
			if _, after := d.count(); after != before {
				t.Fatal("deleted versions before checking the full registered set")
			}
		})
	}
}

type retirementUnavailableDelete struct{ storage.Driver }

func (d retirementUnavailableDelete) Delete(context.Context, storage.Object) error {
	return storage.ErrUnavailable
}

func TestStorageRetirementFailedFinalizerRetainsBarrierAndResumesSameTask(t *testing.T) {
	d := &connectionTestDriver{objects: map[string][]byte{}}
	f := newFinalizeFixture(t, d, true)
	r := f.beginFinalizing(t, "connection")
	f.service.cfg.RetryMaxAttempts = 1
	f.connections.factory = func(context.Context, *ent.StorageConnection, *ent.StorageSpace, storageauth.S3Payload) (storage.Driver, error) {
		return retirementUnavailableDelete{d}, nil
	}
	if err := f.service.ProcessOne(f.ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	r = f.client.StorageRetirement.GetX(f.ctx, r.ID)
	if r.Status != storageretirement.StatusFailed || r.Attempts != 1 {
		t.Fatalf("retry exhaustion: %+v", r)
	}
	if f.client.StorageSpace.GetX(f.ctx, f.space.ID).Lifecycle != storagespace.LifecycleRetiring || !f.client.StoragePathClaim.Query().Where(storagepathclaim.SpaceIDEQ(f.space.ID)).ExistX(f.ctx) {
		t.Fatal("failed finalizer released the path")
	}
	f.connections.factory = func(context.Context, *ent.StorageConnection, *ent.StorageSpace, storageauth.S3Payload) (storage.Driver, error) {
		return d, nil
	}
	if _, err := f.service.Resume(f.ctx, f.owner.ID, r.ID, r.Generation); err != nil {
		t.Fatal(err)
	}
	if err := f.service.ProcessOne(f.ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if f.client.StorageRetirement.GetX(f.ctx, r.ID).Status != storageretirement.StatusCompleted || f.client.StorageRetirement.Query().CountX(f.ctx) != 1 {
		t.Fatal("resume did not complete the original task")
	}
}

func TestStorageRetirementFinalizeBlocksOnMarkerIdentityMismatch(t *testing.T) {
	base := &connectionTestDriver{objects: map[string][]byte{}}
	f := newFinalizeFixture(t, base, true)
	ctx := f.ctx
	before := base.deletes
	r := f.beginFinalizing(t, "space")
	base.mu.Lock()
	base.objects[storageMarkerKey] = []byte(`{"version":1,"identity":"external","nonce":"external"}`)
	base.mu.Unlock()
	err := f.service.Finalize(ctx, r.ID)
	if !errors.Is(err, ErrStorageRetirementDependenciesRemaining) {
		t.Fatalf("identity mismatch did not block: %v", err)
	}
	if base.deletes != before {
		t.Fatalf("identity mismatch deleted a foreign object: %d", base.deletes-before)
	}
	if _, ok := base.objects[storageMarkerKey]; !ok {
		t.Fatal("foreign object was removed")
	}
	if entry := f.markerEntry(t, r.ID); entry == nil || entry.Status == storageretiremententry.StatusCompleted {
		t.Fatalf("blocked reclaim confirmed the marker: %+v", entry)
	}
	if got := f.client.StorageSpace.GetX(ctx, f.space.ID); got.Lifecycle != storagespace.LifecycleRetiring {
		t.Fatalf("blocked reclaim changed lifecycle: %s", got.Lifecycle)
	}
}
func TestStorageRetirementFinalizeReplaysAfterLostDeleteResponse(t *testing.T) {
	d := newFinalizeVersionDriver()
	f := newFinalizeFixture(t, d, true)
	ctx := f.ctx
	r := f.beginFinalizing(t, "space")
	puts0, deletes0 := d.count()
	d.loseDelete = true
	if err := f.service.Finalize(ctx, r.ID); err == nil {
		t.Fatal("lost delete response was reported as confirmed success")
	}
	if _, err := d.Stat(ctx, storage.Object{Key: storageMarkerKey}); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("marker unexpectedly remained: %v", err)
	}
	if entry := f.markerEntry(t, r.ID); entry == nil || entry.Status == storageretiremententry.StatusCompleted {
		t.Fatalf("unknown deletion result was confirmed: %+v", entry)
	}
	// 重放必须根据驱动事实确认缺失,而不是重建标记。
	if err := f.service.Finalize(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	puts, deletes := d.count()
	if puts != puts0 || deletes != deletes0+1 {
		t.Fatalf("replay rewrote the identity: puts=%d deletes=%d", puts-puts0, deletes-deletes0)
	}
	if entry := f.markerEntry(t, r.ID); entry == nil || entry.Status != storageretiremententry.StatusCompleted {
		t.Fatalf("replay did not confirm the marker: %+v", entry)
	}
	if got := f.client.StorageRetirement.GetX(ctx, r.ID); got.Status != storageretirement.StatusCompleted {
		t.Fatalf("replay did not complete the task: %+v", got)
	}
}

func TestStorageRetirementFinalizeRequiresRegisteredEvidenceForAbsence(t *testing.T) {
	base := &connectionTestDriver{objects: map[string][]byte{}}
	f := newFinalizeFixture(t, base, false)
	ctx := f.ctx
	// unknown 绝不是"从未使用":没有已登记事实时不得断言标记不存在。
	f.client.StorageSpace.UpdateOneID(f.space.ID).SetRemoteUseState(storagespace.RemoteUseStateUnknown).ExecX(ctx)
	r := f.beginFinalizing(t, "space")
	err := f.service.Finalize(ctx, r.ID)
	if !errors.Is(err, ErrStorageRetirementDependenciesRemaining) {
		t.Fatalf("missing registered evidence did not block: %v", err)
	}
	if base.puts != 0 || base.deletes != 0 {
		t.Fatal("absence was asserted with remote access")
	}
	entry := f.markerEntry(t, r.ID)
	if entry == nil || entry.Status == storageretiremententry.StatusCompleted {
		t.Fatalf("durable marker entry missing or falsely confirmed: %+v", entry)
	}
}

func TestStorageRetirementFinalizeUnusedIdentityRegistersConfirmedMarker(t *testing.T) {
	base := &connectionTestDriver{objects: map[string][]byte{}}
	f := newFinalizeFixture(t, base, false)
	ctx := f.ctx
	r := f.beginFinalizing(t, "space")
	if err := f.service.Finalize(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if base.puts != 0 || base.deletes != 0 {
		t.Fatal("unused identity performed remote work")
	}
	entry := f.markerEntry(t, r.ID)
	if entry == nil || entry.Status != storageretiremententry.StatusCompleted {
		t.Fatalf("unused identity did not register its confirmed marker entry: %+v", entry)
	}
	var evidence markerEvidence
	if err := json.Unmarshal(entry.Result, &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Outcome != "no_remote_attempt" {
		t.Fatalf("unused identity recorded the wrong evidence: %+v", evidence)
	}
	if got := f.client.StorageSpace.GetX(ctx, f.space.ID); got.Lifecycle != storagespace.LifecycleRemoved {
		t.Fatalf("unused identity not removed: %s", got.Lifecycle)
	}
}
func TestStorageRetirementFinalizeDeploymentWaitsForLoadedConfiguration(t *testing.T) {
	ctx, client, resources, _, _, _ := storageLifecycleFixture(t)
	st := resources.storage
	st.cfg.Enabled = true
	st.cfg.ReconcileInterval = time.Millisecond
	cfg := st.cfg
	cfg.Backends = []config.StorageBackendConfig{{ID: "site-one", Driver: "s3", Endpoint: "https://s3.example", Region: "test", Bucket: "site-bucket", Prefix: "site", AccessKeyID: "access", SecretAccessKey: "secret"}}
	cfg.DefaultSiteSpace = ""
	driver := &connectionTestDriver{objects: map[string][]byte{}}
	connections := NewStorageConnectionService(client, credentialTestKeyring(t, "one", "one"), cfg, func(context.Context, *ent.StorageConnection, *ent.StorageSpace, storageauth.S3Payload) (storage.Driver, error) {
		return driver, nil
	})
	admin := client.User.Create().SetUsername("site-admin").SetEmail("site-admin@example.test").SetPasswordHash("unused").SetRole(SystemRoleAdmin).SaveX(ctx)
	if _, err := connections.SetupSiteBackends(ctx); err != nil {
		t.Fatal(err)
	}
	conn := client.StorageConnection.Query().Where(storageconnection.BackendIDEQ("site-one")).OnlyX(ctx)
	sp := client.StorageSpace.Query().Where(storagespace.ConnectionIDEQ(conn.ID)).OnlyX(ctx)
	if sp.RemoteUseState != storagespace.RemoteUseStateNever || !client.StoragePathClaim.Query().Where(storagepathclaim.SpaceIDEQ(sp.ID)).ExistX(ctx) {
		t.Fatal("site registration did not record never-use and its path claim")
	}
	service, err := NewStorageRetirementService(st, connections)
	if err != nil {
		t.Fatal(err)
	}
	backend := cfg.Backends[0].ID
	scope, err := json.Marshal(RetirementTarget{
		TargetKind: "connection", TargetID: conn.ID, TargetIdentity: "site-one", Scope: "site", OwnerID: 0,
		ConfigurationSource: "deployment", BackendID: &backend,
		ManagementGeneration: conn.ManagementGeneration, ExecutionGeneration: conn.ExecutionGeneration,
		Spaces: []RetirementSpaceScope{{SpaceID: sp.ID, SpaceIdentity: sp.Identity, ConnectionID: conn.ID, ManagementGeneration: sp.ManagementGeneration, ExecutionGeneration: sp.ExecutionGeneration}},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := client.StorageRetirement.Create().SetTargetKind(storageretirement.TargetKindConnection).SetTargetID(conn.ID).
		SetTargetIdentity("site-one").SetOwnerKind(storageretirement.OwnerKindSite).SetOwnerID(0).SetActorID(admin.ID).
		SetConfirmedScope(scope).SetIdempotencyKey("site-retirement").SetRequestHash("site-request").
		SetPhase(storageretirement.PhaseFinalizing).SetStatus(storageretirement.StatusRunning).SetGeneration(1).SaveX(ctx)
	client.StorageSpace.UpdateOneID(sp.ID).SetLifecycle(storagespace.LifecycleRetiring).SetRetirementID(r.ID).AddManagementGeneration(1).AddExecutionGeneration(1).ExecX(ctx)
	client.StorageConnection.UpdateOneID(conn.ID).SetLifecycle(storageconnection.LifecycleRetiring).SetRetirementID(r.ID).AddExecutionGeneration(1).ExecX(ctx)
	// 标记回收完成后进入 waiting_configuration:实际加载的配置仍含原 backend,期间零远端 I/O。
	if err = service.Finalize(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	waiting := client.StorageRetirement.GetX(ctx, r.ID)
	if waiting.Phase != storageretirement.PhaseWaitingConfiguration || waiting.NextCheckAt == nil {
		t.Fatalf("deployment did not wait for configuration: %+v", waiting)
	}
	if driver.puts != 0 || driver.deletes != 0 {
		t.Fatal("waiting_configuration performed remote I/O")
	}
	if got := client.StorageSpace.GetX(ctx, sp.ID); got.Lifecycle != storagespace.LifecycleRetiring {
		t.Fatalf("waiting_configuration released the barrier: %s", got.Lifecycle)
	}
	// 配置撤下前重复检查仍然是纯等待。
	if err = service.Finalize(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if driver.puts != 0 || driver.deletes != 0 {
		t.Fatal("repeated waiting performed remote I/O")
	}
	// 实际加载配置不再含原 backend_id 时才终结。
	service.cfg.Backends = nil
	connections.cfg.Backends = nil
	if err = service.Finalize(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if got := client.StorageRetirement.GetX(ctx, r.ID); got.Status != storageretirement.StatusCompleted {
		t.Fatalf("deployment not completed after configuration removal: %+v", got)
	}
	if got := client.StorageConnection.GetX(ctx, conn.ID); got.Lifecycle != storageconnection.LifecycleRemoved || got.ActiveAuthGeneration != 0 {
		t.Fatalf("site connection not removed: %+v", got)
	}
	// 旧 backend_id 回流必须报告冲突,绝不重新登记或访问旧位置。
	service.cfg.Backends = cfg.Backends
	connections.cfg.Backends = cfg.Backends
	if _, err = connections.SetupSiteBackends(ctx); !errors.Is(err, ErrStorageRetirementConflict) {
		t.Fatalf("removed backend was resurrected: %v", err)
	}
	if n := client.StorageSpace.Query().Where(storagespace.ConnectionIDEQ(conn.ID)).CountX(ctx); n != 1 {
		t.Fatalf("resurrection created an extra space: %d", n)
	}
}

func TestStorageRetirementFinalizeDeploymentBlocksEarlyConfigurationRemoval(t *testing.T) {
	ctx, client, resources, _, _, _ := storageLifecycleFixture(t)
	st := resources.storage
	st.cfg.Enabled = true
	cfg := st.cfg
	cfg.Backends = []config.StorageBackendConfig{{ID: "site-four", Driver: "s3", Endpoint: "https://s3.example", Region: "test", Bucket: "site-four-bucket", Prefix: "site", AccessKeyID: "access", SecretAccessKey: "secret"}}
	driver := &connectionTestDriver{objects: map[string][]byte{}}
	connections := NewStorageConnectionService(client, credentialTestKeyring(t, "one", "one"), cfg, func(context.Context, *ent.StorageConnection, *ent.StorageSpace, storageauth.S3Payload) (storage.Driver, error) {
		return driver, nil
	})
	admin := client.User.Create().SetUsername("site-admin-four").SetEmail("site-admin-four@example.test").SetPasswordHash("unused").SetRole(SystemRoleAdmin).SaveX(ctx)
	if _, err := connections.SetupSiteBackends(ctx); err != nil {
		t.Fatal(err)
	}
	service, err := NewStorageRetirementService(st, connections)
	if err != nil {
		t.Fatal(err)
	}
	conn := client.StorageConnection.Query().Where(storageconnection.BackendIDEQ("site-four")).OnlyX(ctx)
	sp := client.StorageSpace.Query().Where(storagespace.ConnectionIDEQ(conn.ID)).OnlyX(ctx)
	// 该身份已有远端使用事实(含已登记且仍在的标记写入),但配置被提前撤下:精确清理无法继续。
	client.StorageSpace.UpdateOneID(sp.ID).SetRemoteUseState(storagespace.RemoteUseStateAttempted).SetVerified(true).ExecX(ctx)
	markerTask := client.StorageTask.Create().SetOperationID("marker-task").SetIdempotencyKey("marker-task").SetRequestHash("marker").
		SetKind("storage_marker").SetTargetSpaceID(sp.ID).SetStatus("completed").SetPhase("cleaned").SetCleanupStatus("done").SaveX(ctx)
	markerLocation := client.BlobLocation.Create().SetSpaceID(sp.ID).SetObjectKey(storageMarkerKey).SetSize(int64(len(storageMarkerBytes(sp)))).SetStatus("live").SaveX(ctx)
	client.DeletionEntry.Create().SetLocationID(markerLocation.ID).SetOwnerKind("site").SetOwnerID(0).SetProjectID(0).SetNotBefore(time.Now()).SetStatus("done").SaveX(ctx)
	registered := client.StorageWrite.Create().SetTaskID(markerTask.ID).SetSpaceID(sp.ID).SetLocationID(markerLocation.ID).
		SetAttemptID("marker-attempt").SetObjectKey(storageMarkerKey).SetMaxBytes(int64(len(storageMarkerBytes(sp)))).
		SetActualBytes(int64(len(storageMarkerBytes(sp)))).SetSha256("marker-digest").
		SetPhase("committed").SetExpiresAt(time.Now().Add(time.Hour)).SaveX(ctx)
	if _, err := client.StorageReservation.Create().SetWriteID(registered.ID).SetSpaceID(sp.ID).SetBytes(int64(len(storageMarkerBytes(sp)))).SetState("live").Save(ctx); err != nil {
		t.Fatal(err)
	}
	client.StorageSpace.UpdateOneID(sp.ID).AddLiveBytes(int64(len(storageMarkerBytes(sp)))).ExecX(ctx)
	driver.objects[storageMarkerKey] = storageMarkerBytes(sp)
	backend := cfg.Backends[0].ID
	scope, err := json.Marshal(RetirementTarget{TargetKind: "connection", TargetID: conn.ID, TargetIdentity: "site-four", Scope: "site", OwnerID: 0, ConfigurationSource: "deployment", BackendID: &backend, ManagementGeneration: conn.ManagementGeneration, ExecutionGeneration: conn.ExecutionGeneration, Spaces: []RetirementSpaceScope{{SpaceID: sp.ID, SpaceIdentity: sp.Identity, ConnectionID: conn.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	r := client.StorageRetirement.Create().SetTargetKind(storageretirement.TargetKindConnection).SetTargetID(conn.ID).
		SetTargetIdentity("site-four").SetOwnerKind(storageretirement.OwnerKindSite).SetOwnerID(0).SetActorID(admin.ID).
		SetConfirmedScope(scope).SetIdempotencyKey("site-four-retirement").SetRequestHash("site-four").
		SetPhase(storageretirement.PhaseFinalizing).SetStatus(storageretirement.StatusRunning).SetGeneration(1).SaveX(ctx)
	client.StorageSpace.UpdateOneID(sp.ID).SetLifecycle(storagespace.LifecycleRetiring).SetRetirementID(r.ID).ExecX(ctx)
	client.StorageConnection.UpdateOneID(conn.ID).SetLifecycle(storageconnection.LifecycleRetiring).SetRetirementID(r.ID).ExecX(ctx)
	service.cfg.Backends = nil
	connections.cfg.Backends = nil
	if err = service.Finalize(ctx, r.ID); !errors.Is(err, ErrStorageRetirementConfigurationPending) {
		t.Fatalf("early configuration removal was treated as successful cleanup: %v", err)
	}
	if driver.puts != 0 || driver.deletes != 0 {
		t.Fatal("missing deployment configuration performed remote I/O")
	}
	if got := client.StorageSpace.GetX(ctx, sp.ID); got.Lifecycle != storagespace.LifecycleRetiring {
		t.Fatalf("blocked deployment cleanup released the barrier: %s", got.Lifecycle)
	}
}
func TestStorageRetirementSiteSetupDoesNotRekindleRetiringIdentity(t *testing.T) {
	ctx, client, resources, _, _, _ := storageLifecycleFixture(t)
	st := resources.storage
	st.cfg.Enabled = true
	cfg := st.cfg
	cfg.Backends = []config.StorageBackendConfig{{ID: "site-two", Driver: "s3", Endpoint: "https://s3.example", Region: "test", Bucket: "site-two-bucket", Prefix: "site", AccessKeyID: "access", SecretAccessKey: "secret"}}
	cfg.DefaultSiteSpace = "site-two"
	driver := &connectionTestDriver{objects: map[string][]byte{}}
	connections := NewStorageConnectionService(client, credentialTestKeyring(t, "one", "one"), cfg, func(context.Context, *ent.StorageConnection, *ent.StorageSpace, storageauth.S3Payload) (storage.Driver, error) {
		return driver, nil
	})
	if _, err := connections.SetupSiteBackends(ctx); err != nil {
		t.Fatal(err)
	}
	conn := client.StorageConnection.Query().Where(storageconnection.BackendIDEQ("site-two")).OnlyX(ctx)
	sp := client.StorageSpace.Query().Where(storagespace.ConnectionIDEQ(conn.ID)).OnlyX(ctx)
	retirement := client.StorageRetirement.Create().SetTargetKind(storageretirement.TargetKindConnection).SetTargetID(conn.ID).
		SetTargetIdentity("site-two").SetOwnerKind(storageretirement.OwnerKindSite).SetOwnerID(0).
		SetConfirmedScope(json.RawMessage(`{}`)).SetIdempotencyKey("site-two-retirement").SetRequestHash("site-two").
		SetPhase(storageretirement.PhaseFinalizing).SetStatus(storageretirement.StatusRunning).SaveX(ctx)
	client.StorageConnection.UpdateOneID(conn.ID).SetLifecycle(storageconnection.LifecycleRetiring).SetRetirementID(retirement.ID).ExecX(ctx)
	client.StorageSpace.UpdateOneID(sp.ID).SetLifecycle(storagespace.LifecycleRetiring).SetRetirementID(retirement.ID).ExecX(ctx)
	// retiring 不重新启用、不创建替代空间,也不成为部署默认项。
	defaultID, err := connections.SetupSiteBackends(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if defaultID != 0 {
		t.Fatalf("retiring identity was offered as the deployment default: %d", defaultID)
	}
	if got := client.StorageConnection.GetX(ctx, conn.ID); got.Lifecycle != storageconnection.LifecycleRetiring {
		t.Fatalf("retiring identity was re-enabled: %s", got.Lifecycle)
	}
	if n := client.StorageSpace.Query().Where(storagespace.ConnectionIDEQ(conn.ID)).CountX(ctx); n != 1 {
		t.Fatalf("retiring identity gained a replacement space: %d", n)
	}
}

func TestStorageRetirementRecoverAuthorizationIsRestrictedAndIdempotent(t *testing.T) {
	base := &connectionTestDriver{objects: map[string][]byte{}}
	f := newFinalizeFixture(t, base, true)
	ctx := f.ctx
	r := f.beginFinalizing(t, "connection")
	conn := f.client.StorageConnection.GetX(ctx, f.connection.ID)
	business := f.client.StorageAuthVersion.Query().Where(storageauthversion.ConnectionIDEQ(conn.ID)).OnlyX(ctx)
	expires := time.Now().UTC().Add(time.Hour)
	payload := storageauth.S3Payload{Version: storageauth.PayloadVersion, AccessKeyID: "recovered", SecretAccessKey: "recovered-secret"}
	if _, err := f.service.RecoverAuthorization(ctx, f.owner.ID, r.ID, "recover-one", r.Generation, conn.ManagementGeneration, payload, &expires); err != nil {
		t.Fatal(err)
	}
	rows := f.client.StorageAuthVersion.Query().Where(storageauthversion.ConnectionIDEQ(conn.ID), storageauthversion.PurposeEQ(storageauthversion.PurposeRetirement)).AllX(ctx)
	if len(rows) != 1 {
		t.Fatalf("restricted recovery did not persist exactly one scoped credential: %d", len(rows))
	}
	restricted := rows[0]
	if restricted.RetirementID == nil || *restricted.RetirementID != r.ID || restricted.ExpiresAt == nil || len(restricted.Ciphertext) == 0 {
		t.Fatalf("restricted credential lost its task scope: %+v", restricted)
	}
	current := f.client.StorageConnection.GetX(ctx, conn.ID)
	if current.ActiveAuthGeneration != business.Generation {
		t.Fatal("restricted recovery activated itself as the business authorization")
	}
	if current.ExecutionGeneration <= conn.ExecutionGeneration {
		t.Fatal("restricted recovery did not fence the previous executors")
	}
	if _, _, err := f.connections.loadAuth(ctx, current, business.Generation, storageauthversion.StatusActive); err != nil {
		t.Fatalf("business authorization was replaced: %v", err)
	}
	entry := f.client.StorageRetirementEntry.Query().Where(storageretiremententry.RetirementIDEQ(r.ID), storageretiremententry.KindEQ(retirementRecoveryKind)).OnlyX(ctx)
	if entry.Status != storageretiremententry.StatusCompleted || entry.IdempotencyKey == nil || *entry.IdempotencyKey != "recover-one" {
		t.Fatalf("recovery acceptance not durably recorded: %+v", entry)
	}
	// 丢响应后的同键重放:返回原结果,不新建第二份密文。
	if _, err := f.service.RecoverAuthorization(ctx, f.owner.ID, r.ID, "recover-one", r.Generation, conn.ManagementGeneration, payload, &expires); err != nil {
		t.Fatal(err)
	}
	if n := f.client.StorageAuthVersion.Query().Where(storageauthversion.ConnectionIDEQ(conn.ID), storageauthversion.PurposeEQ(storageauthversion.PurposeRetirement)).CountX(ctx); n != 1 {
		t.Fatalf("idempotent replay created another credential: %d", n)
	}
	// 同键不同内容必须冲突。
	other := storageauth.S3Payload{Version: storageauth.PayloadVersion, AccessKeyID: "different", SecretAccessKey: "different"}
	if _, err := f.service.RecoverAuthorization(ctx, f.owner.ID, r.ID, "recover-one", r.Generation, conn.ManagementGeneration, other, &expires); !errors.Is(err, ErrStorageIdempotency) {
		t.Fatalf("same key with different input did not conflict: %v", err)
	}
	// 过去期限必须拒绝,且不写入任何密文。
	past := time.Now().UTC().Add(-time.Minute)
	if _, err := f.service.RecoverAuthorization(ctx, f.owner.ID, r.ID, "recover-past", r.Generation, conn.ManagementGeneration, payload, &past); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("past expiry accepted: %v", err)
	}
	if n := f.client.StorageAuthVersion.Query().Where(storageauthversion.ConnectionIDEQ(conn.ID), storageauthversion.PurposeEQ(storageauthversion.PurposeRetirement)).CountX(ctx); n != 1 {
		t.Fatalf("rejected recovery wrote a credential: %d", n)
	}
}

func TestStorageRetirementRecoverAuthorizationRejectsDeploymentAndWaitingConfiguration(t *testing.T) {
	ctx, client, resources, _, _, _ := storageLifecycleFixture(t)
	st := resources.storage
	st.cfg.Enabled = true
	cfg := st.cfg
	cfg.Backends = []config.StorageBackendConfig{{ID: "site-three", Driver: "s3", Endpoint: "https://s3.example", Region: "test", Bucket: "site-three-bucket", Prefix: "site", AccessKeyID: "access", SecretAccessKey: "secret"}}
	driver := &connectionTestDriver{objects: map[string][]byte{}}
	connections := NewStorageConnectionService(client, credentialTestKeyring(t, "one", "one"), cfg, func(context.Context, *ent.StorageConnection, *ent.StorageSpace, storageauth.S3Payload) (storage.Driver, error) {
		return driver, nil
	})
	admin := client.User.Create().SetUsername("site-admin-three").SetEmail("site-admin-three@example.test").SetPasswordHash("unused").SetRole(SystemRoleAdmin).SaveX(ctx)
	if _, err := connections.SetupSiteBackends(ctx); err != nil {
		t.Fatal(err)
	}
	service, err := NewStorageRetirementService(st, connections)
	if err != nil {
		t.Fatal(err)
	}
	conn := client.StorageConnection.Query().Where(storageconnection.BackendIDEQ("site-three")).OnlyX(ctx)
	sp := client.StorageSpace.Query().Where(storagespace.ConnectionIDEQ(conn.ID)).OnlyX(ctx)
	backend := cfg.Backends[0].ID
	scope, err := json.Marshal(RetirementTarget{TargetKind: "connection", TargetID: conn.ID, TargetIdentity: "site-three", Scope: "site", OwnerID: 0, ConfigurationSource: "deployment", BackendID: &backend, ManagementGeneration: conn.ManagementGeneration, ExecutionGeneration: conn.ExecutionGeneration, Spaces: []RetirementSpaceScope{{SpaceID: sp.ID, SpaceIdentity: sp.Identity, ConnectionID: conn.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	r := client.StorageRetirement.Create().SetTargetKind(storageretirement.TargetKindConnection).SetTargetID(conn.ID).
		SetTargetIdentity("site-three").SetOwnerKind(storageretirement.OwnerKindSite).SetOwnerID(0).SetActorID(admin.ID).
		SetConfirmedScope(scope).SetIdempotencyKey("site-three-recover").SetRequestHash("site-three").
		SetPhase(storageretirement.PhaseFinalizing).SetStatus(storageretirement.StatusRunning).SetGeneration(1).SaveX(ctx)
	client.StorageConnection.UpdateOneID(conn.ID).SetLifecycle(storageconnection.LifecycleRetiring).SetRetirementID(r.ID).ExecX(ctx)
	client.StorageSpace.UpdateOneID(sp.ID).SetLifecycle(storagespace.LifecycleRetiring).SetRetirementID(r.ID).ExecX(ctx)
	payload := storageauth.S3Payload{Version: storageauth.PayloadVersion, AccessKeyID: "access", SecretAccessKey: "secret"}
	// 部署来源永远不接受上传凭据,只给出部署配置恢复指引。
	if _, err = service.RecoverAuthorization(ctx, admin.ID, r.ID, "deploy-recover", r.Generation, conn.ManagementGeneration, payload); !errors.Is(err, ErrStorageRetirementConfigurationPending) {
		t.Fatalf("deployment recovery accepted uploaded credentials: %v", err)
	}
	if n := client.StorageAuthVersion.Query().Where(storageauthversion.PurposeEQ(storageauthversion.PurposeRetirement)).CountX(ctx); n != 0 {
		t.Fatal("deployment recovery wrote a stored credential")
	}
	// waiting_configuration 起不再接受凭据恢复。
	client.StorageRetirement.UpdateOneID(r.ID).SetPhase(storageretirement.PhaseWaitingConfiguration).ExecX(ctx)
	if _, err = service.RecoverAuthorization(ctx, admin.ID, r.ID, "waiting-recover", r.Generation, conn.ManagementGeneration, payload); !errors.Is(err, ErrStorageRetirementConflict) {
		t.Fatalf("waiting_configuration accepted credential recovery: %v", err)
	}
}
func TestStorageRetirementPurgeIsScopedToTargetKind(t *testing.T) {
	base := &connectionTestDriver{objects: map[string][]byte{}}
	f := newFinalizeFixture(t, base, true)
	ctx := f.ctx
	r := f.beginFinalizing(t, "space")
	conn := f.client.StorageConnection.GetX(ctx, f.connection.ID)
	business := f.client.StorageAuthVersion.Query().Where(storageauthversion.ConnectionIDEQ(conn.ID), storageauthversion.PurposeEQ(storageauthversion.PurposeBusiness)).OnlyX(ctx)
	scoped, err := f.client.StorageAuthVersion.Create().SetConnectionID(conn.ID).SetGeneration(business.Generation + 1).
		SetPayloadVersion(storageauth.PayloadVersion).SetAadVersion(storageauth.AADVersion).SetKeyID(business.KeyID).
		SetNonce([]byte("scoped-nonce")).SetCiphertext([]byte("scoped-ciphertext")).
		SetStatus(storageauthversion.StatusCandidate).SetPurpose(storageauthversion.PurposeRetirement).SetRetirementID(r.ID).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = withOrganizationTransaction(ctx, f.client, func(tx *ent.Client) error {
		return f.service.purgeRetirementCredentials(ctx, tx, r)
	}); err != nil {
		t.Fatal(err)
	}
	kept := f.client.StorageAuthVersion.GetX(ctx, business.ID)
	if kept.PurgedAt != nil || len(kept.Ciphertext) == 0 {
		t.Fatal("space retirement wiped the connection's shared business credential")
	}
	purged := f.client.StorageAuthVersion.GetX(ctx, scoped.ID)
	if purged.PurgedAt == nil || len(purged.Ciphertext) != 0 || len(purged.Nonce) != 0 {
		t.Fatalf("task-scoped credential was not reclaimed: %+v", purged)
	}
	if purged.Generation != scoped.Generation || purged.ConnectionID != conn.ID {
		t.Fatal("purge lost the non-secret generation audit")
	}
	// 整连接退出必须回收该连接所有代次的在线密文。
	connectionScope, err := json.Marshal(RetirementTarget{TargetKind: "connection", TargetID: conn.ID, TargetIdentity: "purge-connection", Scope: ScopeUser, OwnerID: f.owner.ID, ConfigurationSource: "stored"})
	if err != nil {
		t.Fatal(err)
	}
	connectionTarget := f.client.StorageRetirement.Create().SetTargetKind(storageretirement.TargetKindConnection).SetTargetID(conn.ID).
		SetTargetIdentity("purge-connection").SetOwnerKind(storageretirement.OwnerKindSite).SetOwnerID(f.owner.ID).SetActorID(f.owner.ID).
		SetConfirmedScope(connectionScope).SetIdempotencyKey("purge-connection").SetRequestHash("purge").
		SetPhase(storageretirement.PhaseFinalizing).SetStatus(storageretirement.StatusRunning).SaveX(ctx)
	if err = withOrganizationTransaction(ctx, f.client, func(tx *ent.Client) error {
		return f.service.purgeRetirementCredentials(ctx, tx, connectionTarget)
	}); err != nil {
		t.Fatal(err)
	}
	for _, row := range f.client.StorageAuthVersion.Query().Where(storageauthversion.ConnectionIDEQ(conn.ID)).AllX(ctx) {
		if row.PurgedAt == nil || len(row.Ciphertext) != 0 || len(row.Nonce) != 0 {
			t.Fatalf("connection purge left online ciphertext: %+v", row)
		}
	}
}

func TestStorageRetirementValidateCurrentKeysSkipsRemovedIdentity(t *testing.T) {
	base := &connectionTestDriver{objects: map[string][]byte{}}
	f := newFinalizeFixture(t, base, true)
	ctx := f.ctx
	conn := f.client.StorageConnection.GetX(ctx, f.connection.ID)
	auth := f.client.StorageAuthVersion.Query().Where(storageauthversion.ConnectionIDEQ(conn.ID)).OnlyX(ctx)
	// 模拟已完成移除的在线密文回收:只留非秘密代次审计。
	f.client.StorageAuthVersion.UpdateOneID(auth.ID).SetStatus(storageauthversion.StatusRetired).
		SetPurgedAt(time.Now().UTC()).ClearNonce().ClearCiphertext().ExecX(ctx)
	unavailable, err := f.connections.ValidateCurrentKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if unavailable != 1 {
		t.Fatalf("recycled credential was not isolated: %d", unavailable)
	}
	f.client.StorageConnection.UpdateOneID(conn.ID).SetLifecycle(storageconnection.LifecycleRemoved).ExecX(ctx)
	unavailable, err = f.connections.ValidateCurrentKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if unavailable != 0 {
		t.Fatalf("removed identity still counted as unavailable: %d", unavailable)
	}
}

func TestStorageRetirementCheckIsFencedBySealedConnection(t *testing.T) {
	base := &connectionTestDriver{objects: map[string][]byte{}}
	f := newFinalizeFixture(t, base, true)
	ctx := f.ctx
	r := f.beginFinalizing(t, "connection")
	conn := f.client.StorageConnection.GetX(ctx, f.connection.ID)
	puts, deletes := base.puts, base.deletes
	if _, err := f.connections.Check(ctx, f.owner.ID, conn.ID, false, conn.ManagementGeneration); !errors.Is(err, ErrStorageRetiring) {
		t.Fatalf("sealed connection accepted a check: %v", err)
	}
	if base.puts != puts || base.deletes != deletes {
		t.Fatal("sealed connection performed provider I/O")
	}
	if got := f.client.StorageRetirement.GetX(ctx, r.ID); got.Phase != storageretirement.PhaseFinalizing {
		t.Fatalf("check altered the retirement: %+v", got)
	}
}
