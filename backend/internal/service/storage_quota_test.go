package service

import (
	"context"
	"encoding/json"
	sqlschema "entgo.io/ent/dialect/sql/schema"
	"errors"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	entmigrate "github.com/MeowSalty/LinguaFlow/backend/internal/ent/migrate"
	"path/filepath"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/systemsetting"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

func storageTestQuota(n int64) *int64 { return &n }
func storageTestQuotaEqual(a, b *int64) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
func setStorageTestPolicy(t *testing.T, c *ent.Client, data string) {
	t.Helper()
	c.SystemSetting.Delete().Where(systemsetting.KeyEQ(storagePolicyKey)).ExecX(context.Background())
	c.SystemSetting.Create().SetKey(storagePolicyKey).SetValue(data).ExecX(context.Background())
}

func TestStorageNullableQuotaArithmetic(t *testing.T) {
	for _, limit := range []*int64{nil, storageTestQuota(200 << 30)} {
		sp := &ent.StorageSpace{CapacityBytes: limit, LiveBytes: 101 << 30}
		if err := storageQuotaAdmission(sp, 1); err != nil {
			t.Fatal(err)
		}
	}
	if err := storageQuotaAdmission(&ent.StorageSpace{CapacityBytes: storageTestQuota(1), LiveBytes: 2}, 1); !errors.Is(err, storage.ErrLimit) {
		t.Fatalf("over quota: %v", err)
	}
	for _, sp := range []*ent.StorageSpace{{LiveBytes: MaxStorageInteger, ReservedBytes: 1}, {LiveBytes: -1}, {CapacityBytes: storageTestQuota(0)}} {
		if err := storageQuotaAdmission(sp, 1); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("malformed ledger accepted: %v", err)
		}
	}
	if err := storageQuotaAdmission(&ent.StorageSpace{LiveBytes: MaxStorageInteger}, 1); !errors.Is(err, ErrInvalidInput) {
		t.Fatal(err)
	}
}

func TestStorageQuotaInitialization(t *testing.T) {
	ctx := context.Background()
	c := testClient(t)
	s, err := NewStorageService(c, NewProjectService(c, NewUserService(c, nil)), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.EnsureStoragePolicy(ctx, false, false, nil, true, nil); !errors.Is(err, ErrStoragePolicy) {
		t.Fatalf("missing serve quota: %v", err)
	}
	if err = s.EnsureStoragePolicy(ctx, true, false, nil, false, nil); err != nil {
		t.Fatal(err)
	}
	p, err := s.Policy(ctx)
	if err != nil || p.LogicalLimitBytes != nil || p.DefaultSpaceCapacityBytes != nil {
		t.Fatalf("local default: %+v %v", p, err)
	}
	if err = s.EnsureStoragePolicy(ctx, false, true, storageTestQuota(1), true, storageTestQuota(2)); err != nil {
		t.Fatal(err)
	}
	p, err = s.Policy(ctx)
	if err != nil || p.LogicalLimitBytes != nil || p.DefaultSpaceCapacityBytes != nil {
		t.Fatalf("restart overwrote policy: %+v %v", p, err)
	}
}

func TestStorageSpaceQuotaCASAndAudit(t *testing.T) {
	ctx, c, s, owner, _, sp, _ := storageConnectionFixture(t)
	c.StorageSpace.UpdateOneID(sp.ID).SetLiveBytes(200).ExecX(ctx)
	other := c.User.Create().SetUsername("quota-other").SetEmail("quota-other@example.test").SetPasswordHash("unused").SaveX(ctx)
	if _, err := s.SetSpaceQuota(ctx, other.ID, sp.ID, nil, sp.ManagementGeneration); !errors.Is(err, ErrForbidden) {
		t.Fatalf("ownership bypass: %v", err)
	}
	updated, err := s.SetSpaceQuota(ctx, owner.ID, sp.ID, storageTestQuota(1), sp.ManagementGeneration)
	if err != nil || updated.LiveBytes != 200 || updated.ManagementGeneration != sp.ManagementGeneration+1 {
		t.Fatalf("lower quota: %+v %v", updated, err)
	}
	if _, err = s.SetSpaceQuota(ctx, owner.ID, sp.ID, nil, sp.ManagementGeneration); !errors.Is(err, ErrStorageConflict) {
		t.Fatalf("stale edit: %v", err)
	}
	updated, err = s.SetSpaceQuota(ctx, owner.ID, sp.ID, nil, updated.ManagementGeneration)
	if err != nil || updated.CapacityBytes != nil || updated.LiveBytes != 200 {
		t.Fatalf("unlimited: %+v %v", updated, err)
	}
	if count := c.ActivityLog.Query().CountX(ctx); count != 2 {
		t.Fatalf("audit count: %d", count)
	}
}

func TestStorageQuotaAtomicInitializationAndLegacyPolicy(t *testing.T) {
	ctx := context.Background()
	c := testClient(t)
	s, err := NewStorageService(c, NewProjectService(c, NewUserService(c, nil)), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultStorageConfig()
	cfg.Enabled = true
	cfg.Backends = []config.StorageBackendConfig{{ID: "bad", Driver: "s3", Endpoint: "invalid"}}
	if err = s.InitializeSiteStorage(ctx, true, cfg, map[string]string{"local": "local-root:one"}); err == nil {
		t.Fatal("invalid backend accepted")
	}
	if c.SystemSetting.Query().Where(systemsetting.KeyEQ(storagePolicyKey)).ExistX(ctx) || c.StorageSpace.Query().ExistX(ctx) {
		t.Fatal("partial initialization committed")
	}
	setStorageTestPolicy(t, c, `{"mode":"both","default_choice":"user","generation":7,"logical_limit_bytes":12345}`)
	if err = s.EnsureStoragePolicy(ctx, false, false, nil, false, nil); !errors.Is(err, ErrStoragePolicy) {
		t.Fatalf("missing upgrade choice: %v", err)
	}
	if err = s.EnsureStoragePolicy(ctx, false, true, storageTestQuota(54321), false, nil); err != nil {
		t.Fatal(err)
	}
	p, err := s.Policy(ctx)
	if err != nil || p.Generation != 7 || p.Mode != "both" || p.DefaultChoice != "user" || *p.LogicalLimitBytes != 12345 || *p.DefaultSpaceCapacityBytes != 54321 {
		t.Fatalf("legacy policy altered: %+v %v", p, err)
	}
}

func TestStorageQuotaLayerCombinations(t *testing.T) {
	ctx, c, resources, p, _, _ := storageLifecycleFixture(t)
	for _, capacity := range []*int64{nil, storageTestQuota(120 << 30)} {
		for _, logical := range []*int64{nil, storageTestQuota(120 << 30)} {
			raw, err := json.Marshal(StoragePolicyRequest{Mode: "site_only", DefaultChoice: "site", LogicalLimitBytes: logical, DefaultSpaceCapacityBytes: capacity})
			if err != nil {
				t.Fatal(err)
			}
			setStorageTestPolicy(t, c, string(raw))
			if err = resources.storage.logicalAdmission(ctx, c, p, 101<<30); err != nil {
				t.Fatal(err)
			}
			if err = storageQuotaAdmission(&ent.StorageSpace{CapacityBytes: capacity}, 101<<30); err != nil {
				t.Fatal(err)
			}
		}
	}
	setStorageTestPolicy(t, c, `{"mode":"site_only","default_choice":"site","logical_limit_bytes":1,"default_space_capacity_bytes":null}`)
	if err := resources.storage.logicalAdmission(ctx, c, p, 2); !errors.Is(err, storage.ErrLimit) {
		t.Fatalf("unlimited space bypassed logical quota: %v", err)
	}
}

func TestStorageAtomicAddBounds(t *testing.T) {
	ctx, c, s, owner, _, sp, _ := storageConnectionFixture(t)
	if _, err := s.SetSpaceStatus(ctx, owner.ID, sp.ID, "active", MaxStorageInteger); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("generation bound: %v", err)
	}
	c.StorageSpace.UpdateOneID(sp.ID).SetManagementGeneration(MaxStorageInteger).ExecX(ctx)
	if err := c.StorageSpace.UpdateOneID(sp.ID).AddManagementGeneration(1).Exec(ctx); err == nil {
		t.Fatal("SQL Add bypassed generation bound")
	}
	c.StorageSpace.UpdateOneID(sp.ID).SetLiveBytes(MaxStorageInteger).ExecX(ctx)
	if err := c.StorageSpace.UpdateOneID(sp.ID).AddReservedBytes(1).Exec(ctx); err == nil {
		t.Fatal("SQL Add bypassed aggregate ledger bound")
	}
}

func TestStorageQuotaSchemaUpgradePreservesIdentity(t *testing.T) {
	ctx := context.Background()
	cfg := config.DefaultServerConfig()
	cfg.DataDir = t.TempDir()
	cfg.Database.DSN = filepath.Join(cfg.DataDir, "upgrade.db")
	cfg.AutoMigrate = true
	db, c, err := database.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tables := append([]*sqlschema.Table(nil), entmigrate.Tables...)
	for i, table := range tables {
		copied := sqlschema.Table{Name: table.Name, Schema: table.Schema, Columns: append([]*sqlschema.Column(nil), table.Columns...), Indexes: table.Indexes, PrimaryKey: table.PrimaryKey, ForeignKeys: table.ForeignKeys, Annotation: table.Annotation, Comment: table.Comment, View: table.View, Pos: table.Pos}
		if table.Annotation != nil {
			annotation := *table.Annotation
			annotation.Checks = nil
			copied.Annotation = &annotation
		}
		if table.Name == "storage_spaces" {
			copied.Columns = append([]*sqlschema.Column(nil), table.Columns...)
			for j, column := range copied.Columns {
				if column.Name == "capacity_bytes" {
					old := *column
					old.Nullable = false
					old.Default = int64(100 << 30)
					copied.Columns[j] = &old
				}
			}
		}
		tables[i] = &copied
	}
	if err = entmigrate.Create(ctx, c.Schema, tables); err != nil {
		t.Fatal(err)
	}
	conn := c.StorageConnection.Create().SetName("original").SetDriver("local").SetBackendID("local").SaveX(ctx)
	sp := c.StorageSpace.Create().SetConnectionID(conn.ID).SetName("original").SetIdentity("preserve-identity").SetMarkerNonce("preserve-nonce").SetCapacityBytes(100 << 30).SetLiveBytes(17).SaveX(ctx)
	if err = c.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	got := c.StorageSpace.GetX(ctx, sp.ID)
	if got.Identity != sp.Identity || got.MarkerNonce != sp.MarkerNonce || got.LiveBytes != 17 || got.CapacityBytes == nil || *got.CapacityBytes != 100<<30 {
		t.Fatalf("upgrade lost facts: %+v", got)
	}
	if err = c.StorageSpace.UpdateOneID(sp.ID).ClearCapacityBytes().Exec(ctx); err != nil {
		t.Fatalf("nullable migration: %v", err)
	}
	c.StorageSpace.UpdateOneID(sp.ID).SetManagementGeneration(MaxStorageInteger).ExecX(ctx)
	if err = c.StorageSpace.UpdateOneID(sp.ID).AddManagementGeneration(1).Exec(ctx); err == nil {
		t.Fatal("upgrade omitted CHECK")
	}
}
