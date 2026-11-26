package storagebackup

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/backuppin"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/blob"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/bloblocation"
)

func metadataBatchFixture(t *testing.T, size int) (*ent.Client, []*ent.BlobLocation, *map[string]int) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	counts := map[string]int{}
	driver := dialect.Debug(database.NewDriver(entsql.OpenDB(dialect.SQLite, db)), func(args ...any) {
		line := strings.ReplaceAll(strings.ReplaceAll(fmt.Sprint(args...), "`", ""), `"`, "")
		if strings.Contains(line, "query=") {
			counts["sql"]++
		}
		if strings.Contains(line, "INSERT INTO backup_pins") {
			counts["pin_insert"]++
		}
		if strings.Contains(line, "UPDATE storage_connections SET management_generation=management_generation") {
			counts["connection_lock"]++
		}
		if strings.Contains(line, "UPDATE storage_spaces SET management_generation=management_generation") {
			counts["space_lock"]++
		}
	})
	client := ent.NewClient(ent.Driver(driver))
	ctx := t.Context()
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	project := client.Project.Create().SetName("batch-backup").SaveX(ctx)
	conn := client.StorageConnection.Create().SetName("batch").SetDriver("local").SaveX(ctx)
	spaces := []*ent.StorageSpace{
		client.StorageSpace.Create().SetName("one").SetConnectionID(conn.ID).SetIdentity("one").SetMarkerNonce("one").SaveX(ctx),
		client.StorageSpace.Create().SetName("two").SetConnectionID(conn.ID).SetIdentity("two").SetMarkerNonce("two").SaveX(ctx),
	}
	locations := make([]*ent.BlobLocation, 0, size)
	for i := 0; i < size; i++ {
		object := client.Blob.Create().SetProjectID(project.ID).SetIdentity(fmt.Sprint("object-", i)).SetPurpose(blob.PurposeSource).SetSize(int64(i)).SetSha256(strings.Repeat("a", 64)).SaveX(ctx)
		status := bloblocation.StatusLive
		if i%2 != 0 {
			status = bloblocation.StatusRetired
		}
		location := client.BlobLocation.Create().SetBlobID(object.ID).SetSpaceID(spaces[i%2].ID).SetObjectKey(fmt.Sprint("object-", i)).SetProviderVersion(fmt.Sprint("version-", i)).SetStatus(status).SaveX(ctx)
		client.Blob.UpdateOneID(object.ID).SetActiveLocationID(location.ID).SetStatus(blob.StatusReady).Select(blob.FieldID).ExecX(ctx)
		locations = append(locations, location)
	}
	clear(counts)
	return client, locations, &counts
}

func TestMetadataCapturePinsPagesAndLocksEachIdentityOnce(t *testing.T) {
	client, locations, counts := metadataBatchFixture(t, metadataPageSize+1)
	ctx := t.Context()
	row, manifest, err := CaptureMetadata(ctx, client, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Objects) != len(locations) || len(manifest.Spaces) != 2 {
		t.Fatal("fixed manifest lost objects or spaces")
	}
	if (*counts)["pin_insert"] != 2 || (*counts)["connection_lock"] != 1 || (*counts)["space_lock"] != 2 || (*counts)["sql"] > 60 {
		t.Fatalf("capture calls grew per object: %v", *counts)
	}
	if len(row.Manifest) == 0 || client.BackupPin.Query().CountX(ctx) != len(locations) {
		t.Fatal("manifest and pins did not commit together")
	}
	if err := CheckReferences(ctx, client, manifest); err != nil {
		t.Fatal(err)
	}
	for i, location := range locations {
		if manifest.Objects[i].LocationID != location.ID || manifest.Objects[i].Version != location.ProviderVersion {
			t.Fatal("snapshot order or provider identity changed")
		}
		if client.BlobLocation.GetX(ctx, location.ID).Status != location.Status {
			t.Fatal("bulk pin changed location status")
		}
	}
	client.BackupPin.Delete().Where(backuppin.BackupIDEQ(manifest.ID), backuppin.LocationIDEQ(locations[len(locations)-1].ID)).ExecX(ctx)
	if err := CheckReferences(ctx, client, manifest); err == nil {
		t.Fatal("missing last-page durable pin accepted")
	}
}

func TestMetadataCaptureLastPageFailureRollsBackAllPins(t *testing.T) {
	client, locations, _ := metadataBatchFixture(t, metadataPageSize+1)
	ctx := t.Context()
	client.BlobLocation.UpdateOneID(locations[len(locations)-1].ID).SetStatus(bloblocation.StatusDeleting).ExecX(ctx)
	if _, _, err := CaptureMetadata(ctx, client, time.Now().Add(time.Hour)); err == nil {
		t.Fatal("deleting last-page location accepted")
	}
	if client.BackupPin.Query().CountX(ctx) != 0 || client.StorageBackup.Query().CountX(ctx) != 0 {
		t.Fatal("failure exposed partial pins or manifest")
	}
}
