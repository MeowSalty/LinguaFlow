package storagemigrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/localstore"
)

func TestBackupResolverChecksMarkerWithoutCreatingIt(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	cfg := config.DefaultServerConfig()
	cfg.DataDir = filepath.Dir(f.root)
	space, err := f.migration.ensureSpace(ctx, f.client, "local", false)
	if err != nil {
		t.Fatal(err)
	}
	driver, err := localstore.New(f.migration.options.DefaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Close()
	connections := service.NewStorageConnectionService(f.client, nil, cfg.Storage, nil)
	if err := connections.AdmitLocalSpace(ctx, space.ID, driver); err != nil {
		t.Fatal(err)
	}
	resolve, closeDrivers := backupResolver(f.client, nil, *cfg)
	if _, err := resolve(ctx, space.ID); err != nil {
		t.Fatal(err)
	}
	closeDrivers()
	marker := filepath.Join(f.migration.options.DefaultRoot, ".linguaflow", "space.json")
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	before, err := f.client.StorageWrite.Query().Count(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resolve, closeDrivers = backupResolver(f.client, nil, *cfg)
	defer closeDrivers()
	if _, err := resolve(ctx, space.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("missing marker accepted: %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("backup recreated missing marker")
	}
	after, err := f.client.StorageWrite.Query().Count(ctx)
	if err != nil || after != before {
		t.Fatal("backup verification wrote probe state")
	}
}
