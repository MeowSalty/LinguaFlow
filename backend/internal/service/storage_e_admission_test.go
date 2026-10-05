package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/systemsetting"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

func TestStorageECapabilitiesEmptyAccountsAndOwnership(t *testing.T) {
	f := newOrganizationFixture(t)
	ctx := context.Background()
	cfg := config.DefaultStorageConfig()
	s := NewStorageConnectionService(f.client, nil, cfg, nil)
	f.client.User.UpdateOneID(f.other.ID).SetRole(SystemRoleAdmin).ExecX(ctx)
	for _, actor := range []int{f.owner.ID, f.admin.ID} {
		for _, scope := range []string{"user", "org"} {
			var organizationID *int
			wantOwner := actor
			if scope == "org" {
				organizationID, wantOwner = &f.org.ID, f.org.ID
			}
			got, err := s.Capabilities(ctx, actor, scope, organizationID)
			if err != nil || got.Scope != scope || got.OwnerID != wantOwner || got.Runtime.DeploymentEnabled || got.Runtime.Maintenance {
				t.Fatalf("empty %s capability: %+v, %v", scope, got, err)
			}
			assertEAvailability(t, got.ManagementActions.CreateConnection, false, "storage_deployment_disabled")
		}
	}
	for _, actor := range []int{f.member.ID, f.other.ID} {
		if _, err := s.Capabilities(ctx, actor, "org", &f.org.ID); !errors.Is(err, ErrForbidden) {
			t.Fatalf("actor %d acquired organization management: %v", actor, err)
		}
	}
	zero := 0
	for _, tc := range []struct {
		scope string
		org   *int
	}{{"site", nil}, {"user", &f.org.ID}, {"org", nil}, {"org", &zero}} {
		if _, err := s.Capabilities(ctx, f.owner.ID, tc.scope, tc.org); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid capability owner accepted: %+v %v", tc, err)
		}
	}
	f.client.User.UpdateOneID(f.owner.ID).SetActive(false).ExecX(ctx)
	if _, err := s.Capabilities(ctx, f.owner.ID, "user", nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("inactive account capability: %v", err)
	}
	if f.client.StorageConnection.Query().CountX(ctx) != 0 || f.client.StorageSpace.Query().CountX(ctx) != 0 {
		t.Fatal("capability discovery created storage metadata")
	}
}

func assertEAvailability(t *testing.T, got StorageActionAvailability, allowed bool, reason string) {
	t.Helper()
	if got.Allowed != allowed || (allowed && len(got.ReasonCodes) != 0) || (!allowed && !slices.Contains(got.ReasonCodes, reason)) {
		t.Fatalf("availability = %+v, want allowed=%v reason=%s", got, allowed, reason)
	}
	if slices.Contains(got.ReasonCodes, "byos_disabled") {
		t.Fatalf("new response emitted legacy reason: %+v", got)
	}
}

func TestStorageEPolicyDeploymentMatrixAndRestart(t *testing.T) {
	for _, mode := range []struct{ mode, choice string }{{"site_only", "site"}, {"both", "site"}, {"both", "user"}, {"user_required", "user"}} {
		for _, enabled := range []bool{false, true} {
			for _, maintenance := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/enabled=%v/maintenance=%v", mode.mode, mode.choice, enabled, maintenance), func(t *testing.T) {
					ctx, client, resources, project, owner, _ := storageLifecycleFixture(t)
					client.User.UpdateOneID(owner.ID).SetRole(SystemRoleAdmin).ExecX(ctx)
					s := resources.storage
					s.cfg.Enabled = true
					request := StoragePolicyRequest{Mode: mode.mode, DefaultChoice: mode.choice, LogicalLimitBytes: 12345}
					saved, err := s.SetPolicy(ctx, owner.ID, request)
					if err != nil {
						t.Fatal(err)
					}
					before := client.SystemSetting.Query().Where(systemsetting.KeyEQ(storagePolicyKey)).OnlyX(ctx).Value
					restarted, err := NewStorageService(client, s.projects, t.TempDir())
					if err != nil {
						t.Fatal(err)
					}
					cfg := config.DefaultStorageConfig()
					cfg.Enabled, cfg.Maintenance = enabled, maintenance
					restarted.Configure(cfg, "sqlite", *project.StorageSpaceID)
					got, err := restarted.Policy(ctx)
					if err != nil || got.Mode != saved.Mode || got.DefaultChoice != saved.DefaultChoice || got.Generation != saved.Generation || got.LogicalLimitBytes != saved.LogicalLimitBytes || got.ConfigurationNeedsUpdate {
						t.Fatalf("restart changed policy: %+v %v", got, err)
					}
					if got.Runtime != (StorageRuntime{DeploymentEnabled: enabled, Maintenance: maintenance}) {
						t.Fatalf("runtime does not reflect restart: %+v", got.Runtime)
					}
					wantModes := []string{"site_only"}
					if enabled {
						wantModes = append(wantModes, "both", "user_required")
					}
					if !reflect.DeepEqual(got.AllowedPolicyModes, wantModes) {
						t.Fatalf("allowed modes: %v, want %v", got.AllowedPolicyModes, wantModes)
					}
					wantRestrictions := []string{}
					if !enabled && mode.mode != "site_only" {
						wantRestrictions = append(wantRestrictions, "storage_deployment_disabled")
					}
					if !reflect.DeepEqual(got.PolicyRestrictionCodes, wantRestrictions) {
						t.Fatalf("policy restrictions: %v, want %v", got.PolicyRestrictionCodes, wantRestrictions)
					}
					options, err := restarted.Options(ctx, owner.ID, "user", 0)
					if err != nil || options.Runtime != got.Runtime {
						t.Fatalf("options runtime: %+v %v", options, err)
					}
					if mode.mode == "user_required" && !enabled {
						for _, item := range options.Items {
							if item.Selectable {
								t.Fatal("disabled user-required policy fell back to site storage")
							}
						}
					}
					if after := client.SystemSetting.Query().Where(systemsetting.KeyEQ(storagePolicyKey)).OnlyX(ctx).Value; after != before {
						t.Fatal("discovery persisted a runtime-derived policy")
					}
					bound := client.Project.GetX(ctx, project.ID)
					if bound.StorageGeneration != project.StorageGeneration || *bound.StorageSpaceID != *project.StorageSpaceID {
						t.Fatal("restart or discovery rebound a historical project")
					}
					request.Generation, request.LogicalLimitBytes = saved.Generation, 67890
					updated, err := restarted.SetPolicy(ctx, owner.ID, request)
					if !enabled && mode.mode != "site_only" {
						if !errors.Is(err, ErrStorageDeploymentDisabled) {
							t.Fatalf("unsupported policy save: %v", err)
						}
						if after := client.SystemSetting.Query().Where(systemsetting.KeyEQ(storagePolicyKey)).OnlyX(ctx).Value; after != before {
							t.Fatal("rejected policy partially changed quota or generation")
						}
						request.Mode, request.DefaultChoice = "site_only", "site"
						updated, err = restarted.SetPolicy(ctx, owner.ID, request)
					}
					if err != nil || updated.Generation != saved.Generation+1 || updated.LogicalLimitBytes != request.LogicalLimitBytes || updated.Runtime != got.Runtime {
						t.Fatalf("legal save: %+v %v", updated, err)
					}
					persisted := client.SystemSetting.Query().Where(systemsetting.KeyEQ(storagePolicyKey)).OnlyX(ctx).Value
					for _, field := range []string{"runtime", "allowed_policy_modes", "policy_restriction_codes", "management_actions"} {
						if strings.Contains(persisted, field) {
							t.Fatalf("response-only field persisted: %s", persisted)
						}
					}
				})
			}
		}
	}
}

func TestStorageEOptionsAgreeWithRegisteredDrivers(t *testing.T) {
	f := newOrganizationFixture(t)
	ctx := context.Background()
	projects := NewProjectService(f.client, f.svc)
	s, err := NewStorageService(f.client, projects, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	localDriver := &connectionTestDriver{objects: map[string][]byte{}}
	local, err := s.InstallSiteSpace(ctx, "local", localDriver)
	if err != nil {
		t.Fatal(err)
	}
	siteConnection := f.client.StorageConnection.Create().SetName("verified site S3").SetDriver(storageconnection.DriverS3).SetBackendID("site-s3").SaveX(ctx)
	site := f.client.StorageSpace.Create().SetConnectionID(siteConnection.ID).SetName("site-s3").SetIdentity("site-s3").SetMarkerNonce("site-s3").SetVerified(true).SaveX(ctx)
	personal := optionSpace(t, ctx, f.client, "user", f.owner.ID)
	organization := optionSpace(t, ctx, f.client, "org", f.org.ID)
	for _, sp := range []*ent.StorageSpace{site, personal, organization} {
		s.RegisterDriver(sp.ID, &connectionTestDriver{objects: map[string][]byte{}})
	}
	f.client.SystemSetting.Create().SetKey(storagePolicyKey).SetValue(`{"mode":"both","default_choice":"site","generation":7,"logical_limit_bytes":100000}`).ExecX(ctx)
	for _, enabled := range []bool{true, false} {
		for _, maintenance := range []bool{false, true} {
			cfg := config.DefaultStorageConfig()
			cfg.Enabled, cfg.Maintenance = enabled, maintenance
			s.Configure(cfg, "sqlite", site.ID)
			for _, scope := range []string{"user", "org"} {
				owner := 0
				if scope == "org" {
					owner = f.org.ID
				}
				options, err := s.Options(ctx, f.owner.ID, scope, owner)
				if err != nil || len(options.Items) != 3 {
					t.Fatalf("%s options: %+v %v", scope, options, err)
				}
				for _, option := range options.Items {
					want := !maintenance && (enabled || option.SpaceID == local.ID)
					if option.Selectable != want || slices.Contains(option.ReasonCodes, "byos_disabled") {
						t.Fatalf("%s enabled=%v maintenance=%v: %+v", scope, enabled, maintenance, option)
					}
					driver, err := s.driver(ctx, option.SpaceID, true)
					if (err == nil) != want {
						t.Fatalf("options and registered driver disagree: %+v %v", option, err)
					}
					if want {
						key := fmt.Sprintf("%s-%v-%v-%d", scope, enabled, maintenance, option.SpaceID)
						if _, err := driver.PutNew(ctx, key, strings.NewReader("x"), 1); err != nil {
							t.Fatal(err)
						}
					} else if maintenance && !errors.Is(err, ErrStorageMaintenance) || !maintenance && !errors.Is(err, ErrStorageDeploymentDisabled) {
						t.Fatalf("wrong deployment rejection: %v", err)
					}
					if _, err := s.driver(ctx, option.SpaceID, false); err != nil {
						t.Fatalf("deployment blocked existing reads: %v", err)
					}
				}
				if !enabled && !maintenance && (options.DefaultSpaceID != nil || options.DefaultUnavailableReason == nil || *options.DefaultUnavailableReason != "storage_deployment_disabled") {
					t.Fatalf("disabled S3 default fell back to Local: %+v", options)
				}
			}
		}
	}
}

func TestStorageEManagementReadonlyRecoveryAndWriteGate(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, maintenance := range []bool{false, true} {
			t.Run(fmt.Sprintf("enabled=%v/maintenance=%v", enabled, maintenance), func(t *testing.T) {
				ctx, client, s, owner, conn, space, driver := storageConnectionFixture(t)
				active, _, err := s.AuthorizeWithCheck(ctx, owner.ID, conn.ID, storageAuthorizeInput(conn.ManagementGeneration))
				if err != nil {
					t.Fatal(err)
				}
				s.cfg.Enabled, s.cfg.Maintenance = enabled, maintenance
				current, err := s.Get(ctx, owner.ID, conn.ID)
				if err != nil {
					t.Fatal(err)
				}
				reason := "storage_deployment_disabled"
				if maintenance {
					reason = "storage_maintenance"
				}
				assertEAvailability(t, current.ManagementActions.CreateSpace, enabled && !maintenance, reason)
				assertEAvailability(t, current.ManagementActions.AuthorizeRead, !maintenance, "storage_maintenance")
				assertEAvailability(t, current.ManagementActions.AuthorizeWrite, enabled && !maintenance, reason)
				assertEAvailability(t, current.ManagementActions.CheckRead, true, "")
				assertEAvailability(t, current.ManagementActions.CheckWrite, enabled && !maintenance, reason)
				assertEAvailability(t, current.ManagementActions.RevokeAuth, true, "")
				assertEAvailability(t, current.ManagementActions.SetStatus, true, "")
				puts, deletes := driver.puts, driver.deletes
				_, check, err := s.CheckWithResult(ctx, owner.ID, conn.ID, false, active.ManagementGeneration)
				if err != nil || check == nil || check.Status != "completed" {
					t.Fatalf("read check: %+v %v", check, err)
				}
				readAuth := storageAuthorizeInput(active.ManagementGeneration)
				readAuth.WriteCheck = false
				_, _, err = s.AuthorizeWithCheck(ctx, owner.ID, conn.ID, readAuth)
				if maintenance && !errors.Is(err, ErrStorageMaintenance) || !maintenance && err != nil {
					t.Fatalf("read authorization: %v", err)
				}
				if driver.puts != puts || driver.deletes != deletes {
					t.Fatalf("read operations wrote remotely: puts %d->%d deletes %d->%d", puts, driver.puts, deletes, driver.deletes)
				}
				if !enabled || maintenance {
					generation := client.StorageConnection.GetX(ctx, conn.ID).ManagementGeneration
					_, _, err = s.CheckWithResult(ctx, owner.ID, conn.ID, true, generation)
					want := ErrStorageDeploymentDisabled
					if maintenance {
						want = ErrStorageMaintenance
					}
					if !errors.Is(err, want) {
						t.Fatalf("explicit write check: %v, want %v", err, want)
					}
					_, _, err = s.AuthorizeWithCheck(ctx, owner.ID, conn.ID, storageAuthorizeInput(generation))
					if !errors.Is(err, want) || driver.puts != puts || driver.deletes != deletes {
						t.Fatalf("write authorization crossed gate: %v", err)
					}
				}
				client.StorageSpace.UpdateOneID(space.ID).SetVerified(false).ExecX(ctx)
				puts, deletes = driver.puts, driver.deletes
				generation := client.StorageConnection.GetX(ctx, conn.ID).ManagementGeneration
				_, _, _ = s.CheckWithResult(ctx, owner.ID, conn.ID, false, generation)
				if driver.puts != puts || driver.deletes != deletes {
					t.Fatal("readonly check repaired an unverified marker")
				}
			})
		}
	}
}

func TestStorageELocalExplicitChecksRemainUnsupported(t *testing.T) {
	ctx, client, _, project, owner, _ := storageLifecycleFixture(t)
	client.User.UpdateOneID(owner.ID).SetRole(SystemRoleAdmin).ExecX(ctx)
	space := client.StorageSpace.GetX(ctx, *project.StorageSpaceID)
	for _, enabled := range []bool{false, true} {
		cfg := config.DefaultStorageConfig()
		cfg.Enabled = enabled
		s := NewStorageConnectionService(client, nil, cfg, nil)
		conn, err := s.Get(ctx, owner.ID, space.ConnectionID)
		if err != nil {
			t.Fatal(err)
		}
		assertEAvailability(t, conn.ManagementActions.CheckRead, false, "storage_capability_unsupported")
		assertEAvailability(t, conn.ManagementActions.CheckWrite, false, "storage_capability_unsupported")
		spaces, err := s.Spaces(ctx, owner.ID, conn.ID)
		if err != nil || len(spaces) != 1 {
			t.Fatalf("site spaces: %+v %v", spaces, err)
		}
		assertEAvailability(t, spaces[0].ManagementActions.SetStatus, true, "")
		for _, write := range []bool{false, true} {
			_, _, err := s.CheckWithResult(ctx, owner.ID, conn.ID, write, conn.ManagementGeneration)
			want := storage.ErrUnsupported
			if !enabled && write {
				want = ErrStorageDeploymentDisabled
			}
			if !errors.Is(err, want) {
				t.Fatalf("local explicit check: %v, want %v", err, want)
			}
		}
	}
}

func TestStorageECachedDriverRechecksWritesButAllowsReadsAndExactDelete(t *testing.T) {
	ctx, client, resources, project, _, provider := storageLifecycleFixture(t)
	s := resources.storage
	space := client.StorageSpace.GetX(ctx, *project.StorageSpaceID)
	client.StorageConnection.UpdateOneID(space.ConnectionID).SetDriver(storageconnection.DriverS3).ExecX(ctx)
	s.cfg.Enabled = true
	cached, err := s.driver(ctx, space.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	object, err := cached.PutNew(ctx, "retained-object", strings.NewReader("one"), 3)
	if err != nil {
		t.Fatal(err)
	}
	s.cfg.Enabled = false
	puts, deletes := provider.puts, provider.deletes
	if _, err := cached.PutNew(ctx, "new-object", strings.NewReader("two"), 3); !errors.Is(err, ErrStorageDeploymentDisabled) || provider.puts != puts {
		t.Fatalf("cached read driver bypassed write admission: %v", err)
	}
	reader, err := cached.Open(ctx, object)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if closeErr := reader.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil || string(data) != "one" {
		t.Fatalf("deployment disabled existing object read: %q %v", data, err)
	}
	s.cfg.Maintenance, s.maintenance = true, true
	if err := cached.Delete(ctx, object); !errors.Is(err, ErrStorageMaintenance) || provider.deletes != deletes {
		t.Fatalf("maintenance allowed exact object delete: %v", err)
	}
	s.cfg.Maintenance, s.maintenance = false, false
	if err := cached.Delete(ctx, object); err != nil || provider.deletes != deletes+1 {
		t.Fatalf("deployment disabled exact registered deletion: %v", err)
	}
}
