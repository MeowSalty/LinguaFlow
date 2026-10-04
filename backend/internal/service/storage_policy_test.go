package service

import (
	"context"
	"errors"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

func TestStoragePolicyRequiresDeploymentEnablement(t *testing.T) {
	ctx := context.Background()
	client := testClient(t)
	admin := client.User.Create().SetUsername("storage-policy-admin").SetEmail("storage-policy@example.test").SetPasswordHash("unused").SetRole(SystemRoleAdmin).SaveX(ctx)
	service, err := NewStorageService(client, NewProjectService(client, NewUserService(client, nil)), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	policy := StoragePolicy{Mode: "both", DefaultChoice: "site", LogicalLimitBytes: 100 << 30}
	for _, mode := range []string{"both", "user_required"} {
		policy.Mode = mode
		if mode == "user_required" {
			policy.DefaultChoice = "user"
		}
		if _, err := service.SetPolicy(ctx, admin.ID, policy); !errors.Is(err, ErrStoragePolicy) {
			t.Fatalf("disabled deployment accepted %s: %v", mode, err)
		}
	}
	stored, err := service.Policy(ctx)
	if err != nil || stored.Generation != 0 || stored.Mode != "site_only" {
		t.Fatalf("rejected policy was published: %+v, %v", stored, err)
	}
	policy.Mode = "site_only"
	policy.DefaultChoice = "site"
	policy, err = service.SetPolicy(ctx, admin.ID, policy)
	if err != nil || policy.Generation != 1 {
		t.Fatalf("disabled deployment cannot update site policy: %+v, %v", policy, err)
	}
	cfg := config.DefaultStorageConfig()
	cfg.Enabled = true
	service.Configure(cfg, "sqlite", 0)
	policy.Mode = "both"
	if _, err := service.SetPolicy(ctx, admin.ID, policy); err != nil {
		t.Fatalf("enabled deployment rejected BYOS policy: %v", err)
	}
}
