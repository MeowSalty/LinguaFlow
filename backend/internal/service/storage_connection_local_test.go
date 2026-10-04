package service

import (
	"context"
	"errors"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagewrite"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

func storageLocalAdmissionFixture(t *testing.T) (context.Context, *ent.Client, *StorageConnectionService, *ent.StorageSpace, *connectionTestDriver) {
	t.Helper()
	ctx := context.Background()
	client := testClient(t)
	c := client.StorageConnection.Create().SetName("local").SetBackendID("local").SetDriver("local").SaveX(ctx)
	sp := client.StorageSpace.Create().SetConnectionID(c.ID).SetName("local").SetIdentity("local-identity").SetMarkerNonce("local-nonce").SetVerified(false).SaveX(ctx)
	svc := NewStorageConnectionService(client, nil, config.DefaultStorageConfig(), nil)
	return ctx, client, svc, sp, &connectionTestDriver{objects: map[string][]byte{}}
}

func TestStorageLocalAdmissionPersistsMarkerAndRejectsReplacement(t *testing.T) {
	ctx, client, svc, sp, driver := storageLocalAdmissionFixture(t)
	if err := svc.AdmitLocalSpace(ctx, sp.ID, driver); err != nil {
		t.Fatal(err)
	}
	after := client.StorageSpace.GetX(ctx, sp.ID)
	if !after.Verified || after.LiveBytes != int64(len(storageMarkerBytes(sp))) || after.ReservedBytes != 0 {
		t.Fatalf("marker admission ledger: %+v", after)
	}
	puts := driver.puts
	if err := svc.AdmitLocalSpace(ctx, sp.ID, driver); err != nil || driver.puts != puts {
		t.Fatalf("restart rewrote verified marker: %v, puts=%d", err, driver.puts)
	}
	driver.objects[storageMarkerKey] = []byte("another-space")
	if err := svc.AdmitLocalSpace(ctx, sp.ID, driver); !errors.Is(err, storage.ErrCorrupt) {
		t.Fatalf("foreign marker admitted: %v", err)
	}
	delete(driver.objects, storageMarkerKey)
	if err := svc.AdmitLocalSpace(ctx, sp.ID, driver); !errors.Is(err, storage.ErrNotFound) || driver.puts != puts {
		t.Fatalf("missing verified marker recreated: %v", err)
	}
}

func TestStorageLocalAdmissionRecoversUnknownResultsWithoutDoubleCharging(t *testing.T) {
	for _, failDelete := range []bool{false, true} {
		t.Run(map[bool]string{false: "write_response", true: "delete_response"}[failDelete], func(t *testing.T) {
			ctx, client, svc, sp, driver := storageLocalAdmissionFixture(t)
			driver.failPutResponse = !failDelete
			driver.failDeleteResponse = failDelete
			if err := svc.AdmitLocalSpace(ctx, sp.ID, driver); err == nil {
				t.Fatal("unknown result reported success")
			}
			if client.StorageSpace.GetX(ctx, sp.ID).Verified {
				t.Fatal("incomplete admission published verified space")
			}
			// 新建服务模拟重启后没有任何内存中的尝试状态。
			svc = NewStorageConnectionService(client, nil, config.DefaultStorageConfig(), nil)
			if err := svc.AdmitLocalSpace(ctx, sp.ID, driver); err != nil {
				t.Fatal(err)
			}
			after := client.StorageSpace.GetX(ctx, sp.ID)
			if !after.Verified || after.LiveBytes != int64(len(storageMarkerBytes(sp))) || after.ReservedBytes != 0 || len(driver.objects) != 1 {
				t.Fatalf("recovered marker/probe accounting: live=%d reserved=%d objects=%d", after.LiveBytes, after.ReservedBytes, len(driver.objects))
			}
			if client.StorageWrite.Query().Where(storagewrite.PhaseNotIn("committed", "cleaned")).CountX(ctx) != 0 {
				t.Fatal("setup attempts did not reconcile")
			}
		})
	}
}

func TestStorageLocalAdmissionMaintenanceDoesNotWrite(t *testing.T) {
	ctx, _, svc, sp, driver := storageLocalAdmissionFixture(t)
	svc.cfg.Maintenance = true
	if err := svc.AdmitLocalSpace(ctx, sp.ID, driver); !errors.Is(err, ErrStorageMaintenance) || driver.puts != 0 {
		t.Fatalf("maintenance admission wrote objects: %v", err)
	}
}
