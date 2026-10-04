package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageauthversion"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagespace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

func optionSpace(t *testing.T, ctx context.Context, client *ent.Client, scope string, owner int) *ent.StorageSpace {
	t.Helper()
	conn := client.StorageConnection.Create().SetName("private connection").SetDriver(storageconnection.DriverS3).
		SetOwnerKind(storageconnection.OwnerKind(scope)).SetOwnerID(owner).SetAuthSource(storageconnection.AuthSourceStored).
		SetEndpoint("https://secret-provider.example").SetActiveAuthGeneration(1).SaveX(ctx)
	client.StorageAuthVersion.Create().SetConnectionID(conn.ID).SetGeneration(1).SetKeyID("secret-key").
		SetNonce([]byte("nonce")).SetCiphertext([]byte("ciphertext")).SetStatus(storageauthversion.StatusActive).SaveX(ctx)
	return client.StorageSpace.Create().SetConnectionID(conn.ID).SetName("safe space").SetIdentity(generateUniqueID()).
		SetMarkerNonce(generateUniqueID()).SetBucket("secret-bucket").SetPrefix("secret-prefix").
		SetOwnerKind(storagespace.OwnerKind(scope)).SetOwnerID(owner).SetVerified(true).SaveX(ctx)
}

func TestStorageOptionsPolicyAndOwnershipMatrix(t *testing.T) {
	f := newOrganizationFixture(t)
	ctx := context.Background()
	projects := NewProjectService(f.client, f.svc)
	s, err := NewStorageService(f.client, projects, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	projects.storage = s
	site, err := s.InstallSiteSpace(ctx, "local", &connectionTestDriver{objects: map[string][]byte{}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultStorageConfig()
	cfg.Enabled = true
	s.Configure(cfg, "sqlite", site.ID)
	personal := optionSpace(t, ctx, f.client, "user", f.owner.ID)
	org := optionSpace(t, ctx, f.client, "org", f.org.ID)
	optionSpace(t, ctx, f.client, "user", f.other.ID)
	optionSpace(t, ctx, f.client, "org", f.org.ID+100)
	f.client.User.UpdateOneID(f.other.ID).SetRole(SystemRoleAdmin).ExecX(ctx)
	policy, err := s.Policy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"site_only", "both", "user_required"} {
		policy.Mode, policy.DefaultChoice = mode, "site"
		if mode == "user_required" {
			policy.DefaultChoice = "user"
		}
		policy, err = s.SetPolicy(ctx, f.other.ID, policy)
		if err != nil {
			t.Fatal(err)
		}
		for _, scope := range []string{"user", "org"} {
			owner, target := 0, personal.ID
			if scope == "org" {
				owner, target = f.org.ID, org.ID
			}
			options, err := s.Options(ctx, f.owner.ID, scope, owner)
			if err != nil || len(options.Items) != 2 {
				t.Fatalf("%s/%s leaked or lost candidates: %+v %v", mode, scope, options, err)
			}
			for _, item := range options.Items {
				want := (item.SpaceID == site.ID && mode != "user_required") || (item.SpaceID == target && mode != "site_only")
				if item.Selectable != want {
					t.Fatalf("%s/%s: %+v", mode, scope, item)
				}
				var createErr error
				if scope == "org" {
					_, createErr = projects.CreateOrgProject(ctx, f.owner.ID, owner, CreateProjectInput{Name: "option", StorageSpaceID: &item.SpaceID})
				} else {
					_, createErr = projects.CreateProject(ctx, f.owner.ID, CreateProjectInput{Name: "option", StorageSpaceID: &item.SpaceID})
				}
				if (createErr == nil) != want {
					t.Fatalf("options and creation disagree: %+v %v", item, createErr)
				}
			}
			if mode == "user_required" && (options.DefaultSpaceID != nil || options.DefaultUnavailableReason == nil || *options.DefaultUnavailableReason != "selection_required") {
				t.Fatalf("invented BYOS default: %+v", options)
			}
		}
	}
	for _, actor := range []int{f.member.ID, f.other.ID} {
		if _, err := s.Options(ctx, actor, "org", f.org.ID); !errors.Is(err, ErrForbidden) {
			t.Fatalf("actor %d got organization creation options: %v", actor, err)
		}
	}
	if _, err := s.Options(ctx, f.owner.ID, "user", f.other.ID); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("personal owner override: %v", err)
	}
	p, err := projects.CreateOrgProject(ctx, f.owner.ID, f.org.ID, CreateProjectInput{Name: "summary", StorageSpaceID: &org.ID})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := s.ProjectStorage(ctx, f.member.ID, p.ID)
	if err != nil || summary.Binding == nil {
		t.Fatalf("member summary: %+v %v", summary, err)
	}
	data, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"endpoint", "bucket", "prefix", "connection_id", "capacity_bytes", "reserved_bytes", "secret"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("unsafe summary: %s", data)
		}
	}
	if _, err := s.ProjectOptions(ctx, f.member.ID, p.ID, "bind", 0); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member got write options: %v", err)
	}
	if _, err := s.ProjectStorage(ctx, f.other.ID, p.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("platform administrator bypassed project authorization: %v", err)
	}
}

func TestStorageOptionsDefaultUnavailableAndPolicyNormalization(t *testing.T) {
	ctx, client, resources, p, owner, _ := storageLifecycleFixture(t)
	s := resources.storage
	client.User.UpdateOneID(owner.ID).SetRole(SystemRoleAdmin).ExecX(ctx)
	client.SystemSetting.Create().SetKey(storagePolicyKey).SetValue(`{"mode":"site_only","default_choice":"user","generation":8,"logical_limit_bytes":1000}`).ExecX(ctx)
	policy, err := s.Policy(ctx)
	if err != nil || policy.DefaultChoice != "site" || !policy.ConfigurationNeedsUpdate {
		t.Fatalf("effective policy: %+v %v", policy, err)
	}
	policy.DefaultChoice = "user"
	if _, err := s.SetPolicy(ctx, owner.ID, policy); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("contradictory policy accepted: %v", err)
	}
	client.StorageSpace.UpdateOneID(*p.StorageSpaceID).SetStatus(storagespace.StatusReadOnly).ExecX(ctx)
	// Another healthy site is not an implicit fallback.
	conn := client.StorageConnection.Create().SetName("another").SetDriver(storageconnection.DriverLocal).SaveX(ctx)
	client.StorageSpace.Create().SetConnectionID(conn.ID).SetName("another").SetIdentity("another").SetMarkerNonce("another").SetVerified(true).ExecX(ctx)
	options, err := s.Options(ctx, owner.ID, "user", 0)
	if err != nil || options.DefaultSpaceID != nil || options.DefaultUnavailableReason == nil || *options.DefaultUnavailableReason != "space_read_only" {
		t.Fatalf("default fallback: %+v %v", options, err)
	}
	if _, err := resources.projects.CreateProject(ctx, owner.ID, CreateProjectInput{Name: "must fail"}); !errors.Is(err, ErrStorageMaintenance) {
		t.Fatalf("creation ignored default state: %v", err)
	}
	client.Project.UpdateOneID(p.ID).ClearStorageSpaceID().ExecX(ctx)
	summary, err := s.ProjectStorage(ctx, owner.ID, p.ID)
	if err != nil || summary.Binding != nil {
		t.Fatalf("legacy unbound summary: %+v %v", summary, err)
	}
}

func TestStorageOptionsRecheckAuthenticationCapacityAndDeployment(t *testing.T) {
	ctx, client, resources, p, owner, _ := storageLifecycleFixture(t)
	s := resources.storage
	s.cfg.Enabled = true
	client.SystemSetting.Create().SetKey(storagePolicyKey).SetValue(`{"mode":"both","default_choice":"site","generation":0,"logical_limit_bytes":1000}`).ExecX(ctx)
	sp := optionSpace(t, ctx, client, "user", owner.ID)
	for _, tc := range []struct {
		name   string
		change func()
		want   error
	}{
		{"authorization", func() { client.StorageAuthVersion.Update().SetExpiresAt(time.Now().Add(-time.Minute)).ExecX(ctx) }, storage.ErrAuthRequired},
		{"disabled", func() {
			client.StorageAuthVersion.Update().ClearExpiresAt().ExecX(ctx)
			client.StorageConnection.UpdateOneID(sp.ConnectionID).SetStatus(storageconnection.StatusDisabled).ExecX(ctx)
		}, storage.ErrPermission},
		{"capacity", func() {
			client.StorageConnection.UpdateOneID(sp.ConnectionID).SetStatus(storageconnection.StatusEnabled).ExecX(ctx)
			client.StorageSpace.UpdateOneID(sp.ID).SetCapacityBytes(1).SetPendingDeleteBytes(1).ExecX(ctx)
		}, storage.ErrLimit},
		{"deployment", func() {
			client.StorageSpace.UpdateOneID(sp.ID).SetPendingDeleteBytes(0).ExecX(ctx)
			s.cfg.Enabled = false
		}, ErrStoragePolicy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.change()
			options, err := s.Options(ctx, owner.ID, "user", 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range options.Items {
				if item.SpaceID == sp.ID && item.Selectable {
					t.Fatalf("unusable target selectable: %+v", item)
				}
			}
			if err := s.Bind(ctx, owner.ID, p.ID, sp.ID, p.StorageGeneration); !errors.Is(err, tc.want) {
				t.Fatalf("write recheck: %v want %v", err, tc.want)
			}
		})
	}
}

func TestStorageMaintenanceDeletionAndDatabaseEditing(t *testing.T) {
	for _, state := range []string{"draining", "migrating", "legacy_migration", "legacy_rollback", "future_state"} {
		t.Run(state, func(t *testing.T) {
			ctx, client, resources, p, owner, _ := storageLifecycleFixture(t)
			r := client.Resource.Create().SetProjectID(p.ID).SetPath("old.txt").SetFormat("txt").SetStoragePath("old.txt").SaveX(ctx)
			empty, err := resources.projects.CreateProject(ctx, owner.ID, CreateProjectInput{Name: "empty"})
			if err != nil {
				t.Fatal(err)
			}
			client.Project.UpdateOneID(p.ID).SetStorageState(state).AddStorageGeneration(1).ExecX(ctx)
			client.Project.UpdateOneID(empty.ID).SetStorageState(state).AddStorageGeneration(1).ExecX(ctx)
			if err := resources.DeleteResource(ctx, owner.ID, p.ID, r.ID); !errors.Is(err, ErrStorageMaintenance) {
				t.Fatalf("resource deletion: %v", err)
			}
			for _, id := range []int{p.ID, empty.ID} {
				if _, err := resources.projects.DeleteProject(ctx, owner.ID, id); !errors.Is(err, ErrStorageMaintenance) {
					t.Fatalf("project deletion %d: %v", id, err)
				}
			}
			if _, err := resources.projects.UpdateProject(ctx, owner.ID, p.ID, UpdateProjectInput{Name: "DB edit allowed"}); err != nil {
				t.Fatalf("database-only edit blocked: %v", err)
			}
		})
	}
}

func TestStorageOptionsRepairUsesSelectedLocationAndHistoricalBinding(t *testing.T) {
	ctx, client, resources, p, owner, _ := storageLifecycleFixture(t)
	r := storageUpload(t, ctx, resources, p, owner, "original.txt", "original\n")
	s := resources.storage
	s.cfg.Enabled = true
	personal := optionSpace(t, ctx, client, "user", owner.ID)
	client.SystemSetting.Create().SetKey(storagePolicyKey).SetValue(`{"mode":"user_required","default_choice":"user","generation":1,"logical_limit_bytes":1000}`).ExecX(ctx)
	summary, err := s.ProjectStorage(ctx, owner.ID, p.ID)
	if err != nil || summary.Binding == nil || !summary.Binding.Historical {
		t.Fatalf("policy change lost historical binding: %+v %v", summary, err)
	}
	client.Project.UpdateOneID(p.ID).SetStorageSpaceID(personal.ID).AddStorageGeneration(1).ExecX(ctx)
	options, err := s.ProjectOptions(ctx, owner.ID, p.ID, "repair", *r.CurrentSourceRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range options.Items {
		if !item.Selectable {
			t.Fatalf("selected source's old site location or current legal target rejected: %+v", item)
		}
	}
	current := client.Project.GetX(ctx, p.ID)
	if err := s.allowedRepairTarget(ctx, client, current, *r.CurrentSourceRevisionID, *p.StorageSpaceID, 0); err != nil {
		t.Fatalf("same-location repair reapplied new-target policy: %v", err)
	}
	if err := s.allowedTarget(ctx, client, current, *p.StorageSpaceID); !errors.Is(err, ErrStoragePolicy) {
		t.Fatalf("new site target ignored policy: %v", err)
	}
	if _, err := s.ProjectOptions(ctx, owner.ID, p.ID, "repair", *r.CurrentSourceRevisionID+1000); err == nil {
		t.Fatal("foreign or nonexistent source revision accepted")
	}
	if _, err := s.ProjectOptions(ctx, owner.ID, p.ID, "bind", *r.CurrentSourceRevisionID); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("cross-purpose conditions accepted: %v", err)
	}
}
