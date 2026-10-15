package service

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/blob"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/bloblocation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageretiremententry"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagespace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/systemsetting"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storageauth"
)

type retirementPreviewFixture struct {
	ctx         context.Context
	client      *ent.Client
	resources   *ResourceService
	storage     *StorageService
	service     *StorageRetirementService
	connections *StorageConnectionService
	owner       *ent.User
	project     *ent.Project
	connection  int
	space       int
	driver      *connectionTestDriver
}

// newRetirementPreviewFixture 建立个人 S3 连接与空间,用于只读预检与依赖扫描测试。
func newRetirementPreviewFixture(t *testing.T) *retirementPreviewFixture {
	t.Helper()
	ctx, client, resources, project, owner, _ := storageLifecycleFixture(t)
	st := resources.storage
	st.cfg.Enabled = true
	st.cfg.ReconcileInterval = time.Millisecond
	policy, err := json.Marshal(StoragePolicyRequest{Mode: "both", DefaultChoice: "site", Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	client.SystemSetting.Update().Where(systemsetting.KeyEQ(storagePolicyKey)).SetValue(string(policy)).ExecX(ctx)
	driver := &connectionTestDriver{objects: map[string][]byte{}}
	connections := NewStorageConnectionService(client, credentialTestKeyring(t, "one", "one"), st.cfg, func(context.Context, *ent.StorageConnection, *ent.StorageSpace, storageauth.S3Payload) (storage.Driver, error) {
		return driver, nil
	})
	conn, err := connections.Create(ctx, owner.ID, CreateStorageConnectionInput{Name: "preview", Scope: "user", OwnerID: owner.ID, Endpoint: "https://s3.example", Region: "test", PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	space, err := connections.CreateSpace(ctx, owner.ID, conn.ID, CreateStorageSpaceInput{Name: "preview", Bucket: "preview-bucket", Prefix: "preview"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewStorageRetirementService(st, connections)
	if err != nil {
		t.Fatal(err)
	}
	return &retirementPreviewFixture{ctx: ctx, client: client, resources: resources, storage: st, service: service, connections: connections, owner: owner, project: project, connection: conn.ID, space: space.ID, driver: driver}
}

func (f *retirementPreviewFixture) bindProject(t *testing.T, projectID int) *ent.Project {
	t.Helper()
	row := f.client.Project.GetX(f.ctx, projectID)
	if err := f.storage.Bind(f.ctx, f.owner.ID, projectID, f.space, row.StorageGeneration); err != nil {
		t.Fatal(err)
	}
	return f.client.Project.GetX(f.ctx, projectID)
}

func (f *retirementPreviewFixture) preview(t *testing.T, in RetirementPreviewInput) *RetirementPreview {
	t.Helper()
	out, err := f.service.Preview(f.ctx, f.owner.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestStorageRetirementPreviewIsReadOnlyAndReportsUnusedIdentity(t *testing.T) {
	f := newRetirementPreviewFixture(t)
	space := f.client.StorageSpace.GetX(f.ctx, f.space)
	conn := f.client.StorageConnection.GetX(f.ctx, f.connection)

	// 未使用身份:没有任何远端尝试,不要求在用退出能力。
	preview := f.preview(t, RetirementPreviewInput{TargetKind: "space", TargetID: f.space})
	if preview.ContractVersion != 1 || preview.Target.TargetKind != "space" || preview.Target.TargetID != f.space {
		t.Fatalf("normalized target: %+v", preview.Target)
	}
	if preview.Target.TargetIdentity != space.Identity || preview.Target.ConfigurationSource != "stored" || preview.Target.BackendID != nil {
		t.Fatalf("identity or configuration source: %+v", preview.Target)
	}
	if len(preview.Target.Spaces) != 1 || preview.Target.Spaces[0].SpaceID != f.space || preview.Target.Spaces[0].SpaceIdentity != space.Identity {
		t.Fatalf("scope: %+v", preview.Target.Spaces)
	}
	if preview.RequiredCapability != "unused_removal" || !preview.Availability.Allowed {
		t.Fatalf("unused identity must not require in-use retirement: %+v", preview)
	}
	if preview.Dependencies.HasMore || len(preview.Dependencies.Items) != 0 || preview.Impact.ProjectCount != 0 || preview.Impact.ConfigurationActionRequired {
		t.Fatalf("unused identity fabricated dependencies: %+v", preview)
	}
	if !preview.ExpiresAt.After(time.Now()) || preview.ExpiresAt.After(time.Now().Add(retirementPreviewLifetime+time.Minute)) {
		t.Fatalf("preview expiry: %v", preview.ExpiresAt)
	}
	if err := f.service.verifyPreview(preview.PreviewRevision, f.owner.ID, retirementPreviewFingerprint(previewFingerprintOf(t, f, "space", f.space), nil)); err != nil {
		t.Fatalf("revision must verify against the same snapshot: %v", err)
	}

	// 授权写检查后空间被验证并登记持久身份标记;此时绑定项目必须要求在用退出能力。
	if _, err := f.connections.Authorize(f.ctx, f.owner.ID, f.connection, storageAuthorizeInput(conn.ManagementGeneration)); err != nil {
		t.Fatal(err)
	}
	before := map[string]int{
		"retirements": f.client.StorageRetirement.Query().CountX(f.ctx),
		"entries":     f.client.StorageRetirementEntry.Query().CountX(f.ctx),
		"writes":      f.client.StorageWrite.Query().CountX(f.ctx),
		"tasks":       f.client.StorageTask.Query().CountX(f.ctx),
		"checks":      f.client.StorageCheck.Query().CountX(f.ctx),
		"claims":      f.client.StoragePathClaim.Query().CountX(f.ctx),
	}
	puts, deletes := f.driver.puts, f.driver.deletes
	space = f.client.StorageSpace.GetX(f.ctx, f.space)
	conn = f.client.StorageConnection.GetX(f.ctx, f.connection)
	f.bindProject(t, f.project.ID)
	bound := f.preview(t, RetirementPreviewInput{TargetKind: "space", TargetID: f.space})
	if bound.RequiredCapability != "in_use_retirement" || bound.Impact.ProjectCount != 1 || len(bound.Projects) != 1 {
		t.Fatalf("bound project not reported: %+v", bound)
	}
	if bound.Projects[0].ProjectID != f.project.ID || !bound.Projects[0].Permissions.Migrate.Allowed || bound.Projects[0].TargetCheck.Allowed {
		t.Fatalf("project assessment without destination: %+v", bound.Projects[0])
	}
	found := false
	for _, dep := range bound.Dependencies.Items {
		if dep.Kind == retirementDependencyProjectBinding && dep.ProjectID == f.project.ID && dep.Count >= 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("project binding dependency missing: %+v", bound.Dependencies.Items)
	}

	after := map[string]int{
		"retirements": f.client.StorageRetirement.Query().CountX(f.ctx),
		"entries":     f.client.StorageRetirementEntry.Query().CountX(f.ctx),
		"writes":      f.client.StorageWrite.Query().CountX(f.ctx),
		"tasks":       f.client.StorageTask.Query().CountX(f.ctx),
		"checks":      f.client.StorageCheck.Query().CountX(f.ctx),
		"claims":      f.client.StoragePathClaim.Query().CountX(f.ctx),
	}
	if !equalIntMaps(before, after) {
		t.Fatalf("preview mutated durable state: before=%v after=%v", before, after)
	}
	currentSpace := f.client.StorageSpace.GetX(f.ctx, f.space)
	currentConn := f.client.StorageConnection.GetX(f.ctx, f.connection)
	if currentSpace.ManagementGeneration != space.ManagementGeneration || currentSpace.ExecutionGeneration != space.ExecutionGeneration || !currentSpace.UpdatedAt.Equal(space.UpdatedAt) {
		t.Fatal("preview advanced the space identity")
	}
	if currentConn.ManagementGeneration != conn.ManagementGeneration || !currentConn.UpdatedAt.Equal(conn.UpdatedAt) {
		t.Fatal("preview advanced the connection identity")
	}
	if f.driver.puts != puts || f.driver.deletes != deletes {
		t.Fatal("preview performed remote work")
	}
}

// previewFingerprintOf 复算同一快照指纹,用于验证签名与事实绑定。
func previewFingerprintOf(t *testing.T, f *retirementPreviewFixture, kind string, id int) string {
	t.Helper()
	target, err := f.service.normalizeTarget(f.ctx, f.client, f.owner.ID, kind, id)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := f.service.inventory(f.ctx, f.client, target)
	if err != nil {
		t.Fatal(err)
	}
	return inv.Fingerprint
}

func equalIntMaps(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

func TestStorageRetirementPreviewRedactsHiddenProjects(t *testing.T) {
	f := newRetirementPreviewFixture(t)
	other := f.client.User.Create().SetUsername("preview-hidden").SetEmail("preview-hidden@example.test").SetPasswordHash("unused").SaveX(f.ctx)
	hidden, err := f.resources.projects.CreateProject(f.ctx, other.ID, CreateProjectInput{Name: "hidden-project"})
	if err != nil {
		t.Fatal(err)
	}
	f.client.Project.UpdateOneID(hidden.ID).SetStorageSpaceID(f.space).AddStorageGeneration(1).ExecX(f.ctx)
	object := f.client.Blob.Create().SetIdentity("hidden-object").SetProjectID(hidden.ID).SetPurpose("source").SetStatus(blob.StatusReady).SaveX(f.ctx)
	f.client.BlobLocation.Create().SetBlobID(object.ID).SetSpaceID(f.space).SetObjectKey("hidden/secret-object").SetSize(2048).SetStatus(bloblocation.StatusLive).SaveX(f.ctx)

	preview := f.preview(t, RetirementPreviewInput{TargetKind: "space", TargetID: f.space})
	if preview.Impact.ProjectCount != 1 || preview.Impact.HiddenProjectCount != 1 || len(preview.Projects) != 0 {
		t.Fatalf("hidden project leaked into the authorized projection: %+v", preview.Impact)
	}
	redacted := 0
	for _, dep := range preview.Dependencies.Items {
		if dep.ProjectID == hidden.ID {
			t.Fatalf("hidden project identity leaked: %+v", dep)
		}
		if dep.Redacted {
			redacted++
			if dep.ProjectID != 0 || dep.SpaceID != 0 || dep.MigrationTaskID != 0 || dep.ProtectedBytes != nil {
				t.Fatalf("redacted dependency leaked identity or bytes: %+v", dep)
			}
		}
	}
	if redacted == 0 {
		t.Fatalf("hidden dependency was not aggregated: %+v", preview.Dependencies.Items)
	}
	encoded, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"hidden-project", "hidden-object", "hidden/secret-object"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("preview response leaked %q", secret)
		}
	}
}

func TestStorageRetirementPreviewPagingKeepsSnapshotAndBlocking(t *testing.T) {
	f := newRetirementPreviewFixture(t)
	for i := 0; i < 3; i++ {
		row, err := f.resources.projects.CreateProject(f.ctx, f.owner.ID, CreateProjectInput{Name: "paged-" + string(rune('a'+i))})
		if err != nil {
			t.Fatal(err)
		}
		f.client.Project.UpdateOneID(row.ID).SetStorageSpaceID(f.space).AddStorageGeneration(1).ExecX(f.ctx)
	}
	object := f.client.Blob.Create().SetIdentity("paged-object").SetProjectID(f.project.ID).SetPurpose("source").SetStatus(blob.StatusReady).SaveX(f.ctx)
	f.client.BlobLocation.Create().SetBlobID(object.ID).SetSpaceID(f.space).SetObjectKey("paged/object").SetSize(4096).SetStatus(bloblocation.StatusLive).SaveX(f.ctx)

	target, err := f.service.normalizeTarget(f.ctx, f.client, f.owner.ID, "space", f.space)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := f.service.inventory(f.ctx, f.client, target)
	if err != nil {
		t.Fatal(err)
	}
	if !inv.Blocking || len(inv.Dependencies) != 4 {
		t.Fatalf("authoritative scan must be complete and blocking: blocking=%v deps=%+v", inv.Blocking, inv.Dependencies)
	}

	first := f.preview(t, RetirementPreviewInput{TargetKind: "space", TargetID: f.space, Limit: 2})
	if len(first.Dependencies.Items) != 2 || !first.Dependencies.HasMore || first.Dependencies.NextCursor == nil || *first.Dependencies.NextCursor != 2 {
		t.Fatalf("first page: %+v", first.Dependencies)
	}
	second := f.preview(t, RetirementPreviewInput{TargetKind: "space", TargetID: f.space, Cursor: 2, Limit: 2, PreviewRevision: first.PreviewRevision})
	if len(second.Dependencies.Items) != 2 || second.Dependencies.HasMore || second.Dependencies.NextCursor != nil {
		t.Fatalf("second page: %+v", second.Dependencies)
	}
	if second.PreviewRevision != first.PreviewRevision || !second.ExpiresAt.Equal(first.ExpiresAt) {
		t.Fatal("continuation must retain the same snapshot identity")
	}
	if _, err = f.service.Preview(f.ctx, f.owner.ID, RetirementPreviewInput{TargetKind: "space", TargetID: f.space, Cursor: 2, Limit: 2}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cursor without revision: %v", err)
	}
	if _, err = f.service.Preview(f.ctx, f.owner.ID, RetirementPreviewInput{TargetKind: "space", TargetID: f.space, Cursor: 2, Limit: 2, PreviewRevision: first.PreviewRevision + "x"}); !errors.Is(err, ErrStorageRetirementPreviewStale) {
		t.Fatalf("tampered revision: %v", err)
	}
	if err = f.service.verifyPreview(first.PreviewRevision, f.owner.ID+1, retirementPreviewFingerprint(inv.Fingerprint, nil)); !errors.Is(err, ErrStorageRetirementPreviewStale) {
		t.Fatalf("revision must be bound to its initiator: %v", err)
	}
	expired, err := f.service.issuePreviewRevision(f.owner.ID, inv.Fingerprint, time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.service.verifyPreview(expired, f.owner.ID, inv.Fingerprint); !errors.Is(err, ErrStorageRetirementPreviewStale) {
		t.Fatalf("expired revision: %v", err)
	}
}

func TestStorageRetirementPreviewRejectsLocalAndUnknownTargets(t *testing.T) {
	f := newRetirementPreviewFixture(t)
	if _, err := f.service.Preview(f.ctx, f.owner.ID, RetirementPreviewInput{TargetKind: "bucket", TargetID: f.space}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown kind: %v", err)
	}
	if _, err := f.service.Preview(f.ctx, f.owner.ID, RetirementPreviewInput{TargetKind: "space", TargetID: 0}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid id: %v", err)
	}
	if _, err := f.service.Preview(f.ctx, f.owner.ID, RetirementPreviewInput{TargetKind: "space", TargetID: 999999}); !errors.Is(err, storage.ErrUnavailable) {
		t.Fatalf("unknown space: %v", err)
	}
	if _, err := f.service.Preview(f.ctx, f.owner.ID, RetirementPreviewInput{TargetKind: "connection", TargetID: 999999}); !errors.Is(err, storage.ErrUnavailable) {
		t.Fatalf("unknown connection: %v", err)
	}
	f.service.local = true
	if _, err := f.service.Preview(f.ctx, f.owner.ID, RetirementPreviewInput{TargetKind: "space", TargetID: f.space}); !errors.Is(err, storage.ErrUnsupported) {
		t.Fatalf("local mode: %v", err)
	}
	f.service.local = false
	// 本地文件系统根不随本轮开放退出:管理员同样只得到能力不支持。
	f.client.User.UpdateOneID(f.owner.ID).SetRole(SystemRoleAdmin).ExecX(f.ctx)
	if _, err := f.service.Preview(f.ctx, f.owner.ID, RetirementPreviewInput{TargetKind: "space", TargetID: f.storage.defaultSpaceID}); !errors.Is(err, storage.ErrUnsupported) {
		t.Fatalf("local driver: %v", err)
	}
}

func TestStorageRetirementInventoryMarkerIsSingleListedAndDoesNotBlock(t *testing.T) {
	f := newRetirementPreviewFixture(t)
	target, err := f.service.normalizeTarget(f.ctx, f.client, f.owner.ID, "space", f.space)
	if err != nil {
		t.Fatal(err)
	}
	location := f.client.BlobLocation.Create().SetSpaceID(f.space).SetObjectKey(storageMarkerKey).SetSize(16).SaveX(f.ctx)
	task := f.client.StorageTask.Create().SetOperationID("marker-operation").SetIdempotencyKey("marker-operation").SetRequestHash("marker").SetKind("storage_marker").SetProjectID(0).SetStatus(storagetask.StatusRunning).SetTargetSpaceID(f.space).SaveX(f.ctx)
	write := f.client.StorageWrite.Create().SetTaskID(task.ID).SetSpaceID(f.space).SetLocationID(location.ID).SetAttemptID("marker-attempt").SetObjectKey(storageMarkerKey).SetMaxBytes(16).SetActualBytes(16).SetPhase("committed").SaveX(f.ctx)
	reservation := f.client.StorageReservation.Create().SetWriteID(write.ID).SetSpaceID(f.space).SetBytes(16).SetState("live").SaveX(f.ctx)
	f.client.BlobLocation.UpdateOneID(location.ID).SetStatus("live").ExecX(f.ctx)
	f.client.StorageSpace.UpdateOneID(f.space).SetLiveBytes(16).ExecX(f.ctx)

	inv, err := f.service.inventory(f.ctx, f.client, target)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Blocking {
		t.Fatalf("identity marker must not block finalizing: %+v", inv.Dependencies)
	}
	markers := 0
	for _, dep := range inv.Dependencies {
		if dep.Kind == retirementDependencyMarker {
			markers++
			if dep.SpaceID != f.space || dep.Status != "pending" {
				t.Fatalf("marker dependency: %+v", dep)
			}
		}
	}
	if markers != 1 {
		t.Fatalf("marker must be listed exactly once: %+v", inv.Dependencies)
	}
	if !inv.HasUsage {
		t.Fatal("a registered marker proves remote use")
	}
	// 账本条目必须与已登记标记写入逐对象一致:对象键不匹配的完成条目不构成回收证据。
	retirement := storageRetirementFixture(t, f.client, f.owner.ID, f.space, "marker-identity", "marker-ledger")
	f.client.StorageRetirementEntry.Create().SetRetirementID(retirement.ID).SetEntryKey("marker:" + strconv.Itoa(f.space)).SetKind("marker").SetSpaceID(f.space).SetObjectKey("spaces/other-marker").SetStatus(storageretiremententry.StatusCompleted).SaveX(f.ctx)
	inv, err = f.service.inventory(f.ctx, f.client, target)
	if err != nil {
		t.Fatal(err)
	}
	for _, dep := range inv.Dependencies {
		if dep.Kind == retirementDependencyMarker && dep.Status == "completed" {
			t.Fatalf("foreign marker entry must not reconcile the registered write: %+v", dep)
		}
	}
	f.client.StorageRetirementEntry.Delete().Where(storageretiremententry.RetirementIDEQ(retirement.ID), storageretiremententry.KindEQ("marker")).ExecX(f.ctx)
	f.client.StorageRetirementEntry.Create().SetRetirementID(retirement.ID).SetEntryKey("marker:" + strconv.Itoa(f.space)).SetKind("marker").SetSpaceID(f.space).SetObjectKey(storageMarkerKey).SetStatus(storageretiremententry.StatusCompleted).SaveX(f.ctx)
	inv, err = f.service.inventory(f.ctx, f.client, target)
	if err != nil {
		t.Fatal(err)
	}
	if !inv.Blocking {
		t.Fatal("completed marker evidence concealed an unsettled reservation")
	}
	f.client.StorageWrite.UpdateOneID(write.ID).SetPhase("cleaned").ExecX(f.ctx)
	f.client.StorageReservation.UpdateOneID(reservation.ID).SetState("freed").ExecX(f.ctx)
	f.client.StorageSpace.UpdateOneID(f.space).SetLiveBytes(0).ExecX(f.ctx)
	f.client.BlobLocation.UpdateOneID(location.ID).SetStatus("deleted").ExecX(f.ctx)
	inv, err = f.service.inventory(f.ctx, f.client, target)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Blocking {
		t.Fatalf("recovered marker must not block: %+v", inv.Dependencies)
	}
	for _, dep := range inv.Dependencies {
		if dep.Kind == retirementDependencyMarker && dep.Status != "completed" {
			t.Fatalf("marker ledger not reconciled: %+v", dep)
		}
	}
	// 未终结的普通写入仍是失败写残留,必须阻止封存。
	f.client.StorageWrite.UpdateOneID(write.ID).SetPhase("sending").SetOutcomeUnknown(true).ExecX(f.ctx)
	inv, err = f.service.inventory(f.ctx, f.client, target)
	if err != nil {
		t.Fatal(err)
	}
	if !inv.Blocking {
		t.Fatalf("unfinished marker write must block: %+v", inv.Dependencies)
	}
}

func TestStorageRetirementNormalizeSiteSpaceExpandsToConnection(t *testing.T) {
	f := newRetirementPreviewFixture(t)
	f.client.User.UpdateOneID(f.owner.ID).SetRole(SystemRoleAdmin).ExecX(f.ctx)
	conn := f.client.StorageConnection.Create().SetName("site").SetDriver(storageconnection.DriverS3).SetOwnerKind(storageconnection.OwnerKindSite).SetOwnerID(0).SetBackendID("site-backend").SetAuthSource(storageconnection.AuthSourceDeployment).SetEndpoint("https://s3.example").SetRegion("test").SetRemoteUseState(storageconnection.RemoteUseStateNever).SaveX(f.ctx)
	space := f.client.StorageSpace.Create().SetConnectionID(conn.ID).SetName("site").SetIdentity("site-identity").SetMarkerNonce("site-nonce").SetBucket("site-bucket").SetPrefix("site").SetOwnerKind(storagespace.OwnerKindSite).SetRemoteUseState(storagespace.RemoteUseStateNever).SaveX(f.ctx)

	fromSpace, err := f.service.normalizeTarget(f.ctx, f.client, f.owner.ID, "space", space.ID)
	if err != nil {
		t.Fatal(err)
	}
	fromConnection, err := f.service.normalizeTarget(f.ctx, f.client, f.owner.ID, "connection", conn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fromSpace.TargetKind != "connection" || fromSpace.TargetID != conn.ID || fromSpace.BackendID == nil || *fromSpace.BackendID != "site-backend" {
		t.Fatalf("site space must normalize to its backend connection: %+v", fromSpace)
	}
	if retirementHash(fromSpace) != retirementHash(fromConnection) {
		t.Fatal("both entry points must produce the same normalized target")
	}
	if len(fromSpace.Spaces) != 1 || fromSpace.Spaces[0].SpaceID != space.ID {
		t.Fatalf("site scope: %+v", fromSpace.Spaces)
	}
	preview := f.preview(t, RetirementPreviewInput{TargetKind: "space", TargetID: space.ID})
	if preview.RequiredCapability != "unused_removal" || !preview.Availability.Allowed || !preview.Impact.ConfigurationActionRequired {
		t.Fatalf("site configuration must be listed without blocking: %+v", preview)
	}
	if len(preview.Dependencies.Items) != 1 || preview.Dependencies.Items[0].Kind != retirementDependencyConfiguration {
		t.Fatalf("site configuration dependency: %+v", preview.Dependencies.Items)
	}
	// 未知的远端使用状态禁止快速路径:只有已登记事实才能证明从未尝试。
	f.client.StorageSpace.UpdateOneID(space.ID).SetRemoteUseState(storagespace.RemoteUseStateUnknown).ExecX(f.ctx)
	unknown := f.preview(t, RetirementPreviewInput{TargetKind: "space", TargetID: space.ID})
	if unknown.RequiredCapability != "in_use_retirement" {
		t.Fatalf("unknown remote use must not be treated as unused: %+v", unknown.RequiredCapability)
	}
	f.client.StorageSpace.UpdateOneID(space.ID).SetRemoteUseState(storagespace.RemoteUseStateNever).ExecX(f.ctx)

	// 与"一个 backend 一个有效子空间"的配置模型不符时必须先对账。
	f.client.StorageSpace.Create().SetConnectionID(conn.ID).SetName("site-extra").SetIdentity("site-extra-identity").SetMarkerNonce("site-extra-nonce").SetBucket("site-bucket-extra").SetPrefix("site-extra").SetOwnerKind(storagespace.OwnerKindSite).SaveX(f.ctx)
	if _, err = f.service.normalizeTarget(f.ctx, f.client, f.owner.ID, "space", space.ID); !errors.Is(err, ErrStorageRetirementConflict) {
		t.Fatalf("ambiguous site scope: %v", err)
	}
}

func TestStorageRetirementSpaceScopeExcludesSiblingSpaces(t *testing.T) {
	f := newRetirementPreviewFixture(t)
	sibling, err := f.connections.CreateSpace(f.ctx, f.owner.ID, f.connection, CreateStorageSpaceInput{Name: "sibling", Bucket: "sibling-bucket", Prefix: "sibling"})
	if err != nil {
		t.Fatal(err)
	}
	object := f.client.Blob.Create().SetIdentity("sibling-object").SetProjectID(f.project.ID).SetPurpose("source").SetStatus(blob.StatusReady).SaveX(f.ctx)
	f.client.BlobLocation.Create().SetBlobID(object.ID).SetSpaceID(sibling.ID).SetObjectKey("sibling/object").SetSize(1024).SetStatus(bloblocation.StatusLive).SaveX(f.ctx)

	// BYOS 单空间退出只覆盖自身:兄弟空间的真实对象不属于本次范围。
	target, err := f.service.normalizeTarget(f.ctx, f.client, f.owner.ID, "space", f.space)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := f.service.inventory(f.ctx, f.client, target)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Blocking || len(inv.Dependencies) != 0 || len(inv.Projects) != 0 {
		t.Fatalf("space retirement must not cover sibling spaces: %+v", inv.Dependencies)
	}
	// 连接级退出冻结全部未移除子空间,因此必须看到兄弟空间的对象。
	connection, err := f.service.normalizeTarget(f.ctx, f.client, f.owner.ID, "connection", f.connection)
	if err != nil {
		t.Fatal(err)
	}
	if len(connection.Spaces) != 2 {
		t.Fatalf("connection scope must freeze every child space: %+v", connection.Spaces)
	}
	connectionInv, err := f.service.inventory(f.ctx, f.client, connection)
	if err != nil {
		t.Fatal(err)
	}
	if !connectionInv.Blocking {
		t.Fatalf("connection retirement must cover child objects: %+v", connectionInv.Dependencies)
	}
}

// 空间目标的 TargetID 是空间 ID:availability 必须经归一化范围解析连接,
// 否则空间 ID 恰好撞上别的连接 ID 时会报出错误的安全结论。
func TestStorageRetirementAvailabilityResolvesSpaceConnection(t *testing.T) {
	f := newRetirementPreviewFixture(t)
	second, err := f.connections.Create(f.ctx, f.owner.ID, CreateStorageConnectionInput{Name: "second", Scope: "user", OwnerID: f.owner.ID, Endpoint: "https://s3.example", Region: "test", PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.connections.CreateSpace(f.ctx, f.owner.ID, second.ID, CreateStorageSpaceInput{Name: "second-a", Bucket: "second-a", Prefix: "a"}); err != nil {
		t.Fatal(err)
	}
	spaceB, err := f.connections.CreateSpace(f.ctx, f.owner.ID, second.ID, CreateStorageSpaceInput{Name: "second-b", Bucket: "second-b", Prefix: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if spaceB.ID == second.ID {
		t.Skip("IDs coincide; regression still covered by other cases")
	}
	preview := f.preview(t, RetirementPreviewInput{TargetKind: "space", TargetID: spaceB.ID})
	for _, reason := range preview.Availability.ReasonCodes {
		if reason == "storage_removed" {
			t.Fatalf("space target must not resolve availability from its space ID: %+v", preview.Availability)
		}
	}
}

// 迁入目标进入 cutover/cleanup 后与协调器共用同一事实:availability 必须报告迁移进行中。
func TestStorageRetirementAvailabilityReportsIncomingCutover(t *testing.T) {
	f := newRetirementPreviewFixture(t)
	task := f.client.StorageTask.Create().SetOperationID("migration-op").SetIdempotencyKey("migration-op").SetRequestHash("migration").SetKind("migration").SetProjectID(f.project.ID).SetTargetSpaceID(f.space).SetStatus(storagetask.StatusRunning).SetPhase("sending").SaveX(f.ctx)
	preview := f.preview(t, RetirementPreviewInput{TargetKind: "space", TargetID: f.space})
	for _, reason := range preview.Availability.ReasonCodes {
		if reason == "storage_operation_in_progress" {
			t.Fatalf("non-cutover migration must not block availability: %+v", preview.Availability)
		}
	}
	f.client.StorageTask.UpdateOneID(task.ID).SetPhase("cutover").ExecX(f.ctx)
	preview = f.preview(t, RetirementPreviewInput{TargetKind: "space", TargetID: f.space})
	if preview.Availability.Allowed {
		t.Fatalf("incoming cutover must block retirement: %+v", preview.Availability)
	}
	found := false
	for _, reason := range preview.Availability.ReasonCodes {
		if reason == "storage_operation_in_progress" {
			found = true
		}
	}
	if !found {
		t.Fatalf("incoming cutover must be reported: %+v", preview.Availability)
	}
	f.client.StorageTask.UpdateOneID(task.ID).SetPhase("cleanup").ExecX(f.ctx)
	preview = f.preview(t, RetirementPreviewInput{TargetKind: "space", TargetID: f.space})
	if preview.Availability.Allowed {
		t.Fatalf("incoming cleanup must block retirement: %+v", preview.Availability)
	}
}

// deleting 位置仍是未确认的远端清理事实,必须留在权威依赖里并阻止封存。
func TestStorageRetirementInventoryCountsDeletingLocation(t *testing.T) {
	f := newRetirementPreviewFixture(t)
	object := f.client.Blob.Create().SetIdentity("deleting-object").SetProjectID(f.project.ID).SetPurpose("source").SetStatus(blob.StatusReady).SaveX(f.ctx)
	f.client.BlobLocation.Create().SetBlobID(object.ID).SetSpaceID(f.space).SetObjectKey("deleting/object").SetSize(100).SetStatus(bloblocation.StatusDeleting).SaveX(f.ctx)
	target, err := f.service.normalizeTarget(f.ctx, f.client, f.owner.ID, "space", f.space)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := f.service.inventory(f.ctx, f.client, target)
	if err != nil {
		t.Fatal(err)
	}
	if !inv.Blocking {
		t.Fatalf("deleting location must block: %+v", inv.Dependencies)
	}
	found := false
	for _, dep := range inv.Dependencies {
		if dep.Kind == retirementDependencyActiveLocation && dep.SpaceID == f.space && dep.Count == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("deleting location missing from dependencies: %+v", inv.Dependencies)
	}
}

// BackupPin 的保护字节来自钉住的位置行,不能固定为 0。
func TestStorageRetirementBackupPinProtectedBytes(t *testing.T) {
	f := newRetirementPreviewFixture(t)
	object := f.client.Blob.Create().SetIdentity("pinned-object").SetProjectID(f.project.ID).SetPurpose("source").SetStatus(blob.StatusReady).SaveX(f.ctx)
	location := f.client.BlobLocation.Create().SetBlobID(object.ID).SetSpaceID(f.space).SetObjectKey("pinned/object").SetSize(1024).SetStatus(bloblocation.StatusLive).SaveX(f.ctx)
	f.client.BackupPin.Create().SetLocationID(location.ID).SetBackupID("backup-1").SetExpiresAt(time.Now().UTC().Add(24 * time.Hour)).SaveX(f.ctx)
	target, err := f.service.normalizeTarget(f.ctx, f.client, f.owner.ID, "space", f.space)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := f.service.inventory(f.ctx, f.client, target)
	if err != nil {
		t.Fatal(err)
	}
	var bytes *int64
	for _, dep := range inv.Dependencies {
		if dep.Kind == retirementDependencyBackupPin {
			bytes = dep.ProtectedBytes
		}
	}
	if bytes == nil || *bytes != 1024 {
		t.Fatalf("backup pin must protect the pinned object's bytes: %+v", inv.Dependencies)
	}
}

// 指纹必须区分同数量换对象:聚合计数与字节都相同时,对象身份变化必须改变指纹。
func TestStorageRetirementFingerprintDistinguishesSwappedObjects(t *testing.T) {
	f := newRetirementPreviewFixture(t)
	object := f.client.Blob.Create().SetIdentity("swap-object").SetProjectID(f.project.ID).SetPurpose("source").SetStatus(blob.StatusReady).SaveX(f.ctx)
	first := f.client.BlobLocation.Create().SetBlobID(object.ID).SetSpaceID(f.space).SetObjectKey("swap/first").SetSize(100).SetStatus(bloblocation.StatusLive).SaveX(f.ctx)
	target, err := f.service.normalizeTarget(f.ctx, f.client, f.owner.ID, "space", f.space)
	if err != nil {
		t.Fatal(err)
	}
	before, err := f.service.inventory(f.ctx, f.client, target)
	if err != nil {
		t.Fatal(err)
	}
	f.client.BlobLocation.UpdateOneID(first.ID).SetStatus(bloblocation.StatusDeleted).ExecX(f.ctx)
	f.client.BlobLocation.Create().SetBlobID(object.ID).SetSpaceID(f.space).SetObjectKey("swap/second").SetSize(100).SetStatus(bloblocation.StatusLive).SaveX(f.ctx)
	after, err := f.service.inventory(f.ctx, f.client, target)
	if err != nil {
		t.Fatal(err)
	}
	if before.Fingerprint == after.Fingerprint {
		t.Fatal("swapped object with identical aggregate must change the fingerprint")
	}
}
