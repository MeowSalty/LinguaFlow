package service

import (
	"errors"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

func TestStorageEUnregisteredS3DefaultReportsDeploymentBlock(t *testing.T) {
	for _, maintenance := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "maintenance"}[maintenance], func(t *testing.T) {
			ctx, client, resources, project, owner, driver := storageLifecycleFixture(t)
			s := resources.storage
			cfg := s.cfg
			cfg.Enabled, cfg.Maintenance = false, maintenance
			cfg.DefaultSiteSpace = "remote"
			cfg.Backends = []config.StorageBackendConfig{{ID: "remote", Driver: "s3"}}
			s.Configure(cfg, "sqlite", 0)
			connections, spaces := client.StorageConnection.Query().CountX(ctx), client.StorageSpace.Query().CountX(ctx)
			options, err := s.Options(ctx, owner.ID, ScopeUser, 0)
			if err != nil {
				t.Fatal(err)
			}
			want, cause := "storage_deployment_disabled", ErrStorageDeploymentDisabled
			if maintenance {
				want, cause = "storage_maintenance", ErrStorageMaintenance
			}
			if options.DefaultSpaceID != nil || options.DefaultUnavailableReason == nil || *options.DefaultUnavailableReason != want {
				t.Fatalf("unregistered default was substituted or unexplained: %+v", options)
			}
			if _, err = s.selectSpace(ctx, client, project, nil); !errors.Is(err, cause) {
				t.Fatalf("submission disagrees with discovery: %v, want %v", err, cause)
			}
			if client.StorageConnection.Query().CountX(ctx) != connections || client.StorageSpace.Query().CountX(ctx) != spaces || driver.puts != 0 {
				t.Fatal("discovery registered or wrote the missing deployment default")
			}
		})
	}
}
