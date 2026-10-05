package storagebackup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credentialstore"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/blob"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/bloblocation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/localstore"
)

func TestConsistentBackupAndReadOnlyRestoreCheck(t *testing.T) {
	ctx := context.Background()
	cfg := config.DefaultServerConfig()
	cfg.DataDir = t.TempDir()
	cfg.AutoMigrate = true
	db, client, err := database.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	keys, _, err := credential.GenerateKeyring()
	if err != nil {
		t.Fatal(err)
	}
	user, err := client.User.Create().SetUsername("backup-owner").SetEmail("backup@test.invalid").SetPasswordHash("test").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := credentialstore.Create(ctx, client, keys, credentialstore.CreateInput{Scope: "user", OwnerID: user.ID, Provider: "openai", Secret: "test-llm-key"}); err != nil {
		t.Fatal(err)
	}
	project, err := client.Project.Create().SetName("backup project").SetOwnerUserID(user.ID).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := client.StorageConnection.Create().SetName("local").SetBackendID("local").SetDriver(storageconnection.DriverLocal).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	space, err := client.StorageSpace.Create().SetConnectionID(connection.ID).SetName("local").SetIdentity("space-identity").SetMarkerNonce("nonce").SetVerified(true).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(cfg.DataDir, "objects")
	driver, err := localstore.New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Close()
	payload := "original bytes"
	object, err := driver.PutNew(ctx, "source", strings.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(payload))
	b, err := client.Blob.Create().SetIdentity("blob").SetProjectID(project.ID).SetPurpose(blob.PurposeSource).SetSize(int64(len(payload))).SetSha256(hex.EncodeToString(digest[:])).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	location, err := client.BlobLocation.Create().SetBlobID(b.ID).SetSpaceID(space.ID).SetObjectKey(object.Key).SetSize(object.Size).SetStatus(bloblocation.StatusLive).SetIntegrity(bloblocation.IntegrityAvailable).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Blob.UpdateOneID(b.ID).SetActiveLocationID(location.ID).SetStatus(blob.StatusReady).SetLocationGeneration(1).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	resolver := func(context.Context, int) (storage.Driver, error) { return driver, nil }
	metadata, manifest, err := CaptureMetadata(ctx, client, time.Now().UTC().Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if string(metadata.Status) != "metadata_only" || manifest.Database.SHA256 != "" {
		t.Fatal("manifest claimed full backup")
	}
	if pins, err := client.BackupPin.Query().Count(ctx); err != nil || pins != 1 {
		t.Fatal("metadata did not pin exact location")
	}
	output := filepath.Join(t.TempDir(), "backup")
	manifest, err = CaptureOffline(ctx, db, client, resolver, OfflineOptions{Directory: output, ExpiresAt: time.Now().UTC().Add(24 * time.Hour), Offline: true, Config: *cfg, Keys: keys})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Status != "complete" {
		t.Fatalf("backup incomplete: %+v", manifest)
	}
	if manifest.Database.SHA256 == "" || manifest.Keyring.SHA256 == "" || manifest.Deployment.SHA256 == "" {
		t.Fatal("complete backup lacks proof")
	}
	backedKeys, err := credential.LoadKeyring(filepath.Join(output, manifest.Keyring.Name))
	if err != nil {
		t.Fatal(err)
	}
	if !backedKeys.HasKey(keys.ActiveKeyID()) {
		t.Fatal("keyring backup lost active key")
	}
	copyCfg := *cfg
	copyCfg.Database.DSN = filepath.Join(output, "database.sqlite")
	copyCfg.AutoMigrate = false
	copyDB, copyClient, err := database.Open(ctx, &copyCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer copyDB.Close()
	if ready, err := RestoreCheck(ctx, copyClient, resolver, output, manifest); err != nil || !ready {
		t.Fatalf("restore check: %v %v", ready, err)
	}
	for name, mutate := range map[string]func(*Manifest){
		"missing object": func(m *Manifest) { m.Objects = nil },
		"changed baseline": func(m *Manifest) {
			value := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			m.Objects[0].SHA256 = &value
		},
		"missing key IDs":  func(m *Manifest) { m.RequiredKeyIDs = nil },
		"missing database": func(m *Manifest) { m.Database = Artifact{} },
		"extended pins":    func(m *Manifest) { m.ExpiresAt = m.ExpiresAt.Add(time.Hour) },
	} {
		t.Run(name, func(t *testing.T) {
			encoded, _ := json.Marshal(manifest)
			var edited Manifest
			if err := json.Unmarshal(encoded, &edited); err != nil {
				t.Fatal(err)
			}
			mutate(&edited)
			if ready, err := RestoreCheck(ctx, copyClient, resolver, output, &edited); err == nil || ready {
				t.Fatalf("edited manifest accepted: %v %v", ready, err)
			}
		})
	}
	if err := client.Blob.UpdateOneID(b.ID).ClearSize().ClearSha256().Exec(ctx); err != nil {
		t.Fatal(err)
	}
	unknown, err := CaptureOffline(ctx, db, client, resolver, OfflineOptions{Directory: filepath.Join(t.TempDir(), "unknown"), ExpiresAt: time.Now().UTC().Add(time.Hour), Offline: true, Config: *cfg, Keys: keys})
	if err != nil {
		t.Fatal(err)
	}
	if unknown.Status != "incomplete" || unknown.Objects[0].Error != "legacy_unverified" {
		t.Fatal("unknown original baseline produced complete backup")
	}
	if err := client.Project.DeleteOneID(project.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if count, err := client.BackupPin.Query().Count(ctx); err != nil || count != 3 {
		t.Fatal("business deletion removed backup pins")
	}
	if err := os.WriteFile(filepath.Join(root, "source"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if ready, err := RestoreCheck(ctx, copyClient, resolver, output, manifest); err != nil || ready {
		t.Fatalf("corrupt original accepted: %v %v", ready, err)
	}
	if _, err := os.Stat(filepath.Join(output, "database.sqlite")); err != nil {
		t.Fatal(err)
	}
}

func TestWrongKeyCannotProduceCompleteBackup(t *testing.T) {
	ctx := context.Background()
	cfg := config.DefaultServerConfig()
	cfg.DataDir = t.TempDir()
	cfg.AutoMigrate = true
	db, client, err := database.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatal(err)
	}
	keys, _, err := credential.GenerateKeyring()
	if err != nil {
		t.Fatal(err)
	}
	owner, err := client.User.Create().SetUsername("key-owner").SetEmail("key@test.invalid").SetPasswordHash("test").Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := credentialstore.Create(ctx, client, keys, credentialstore.CreateInput{Scope: "user", OwnerID: owner.ID, Provider: "openai", Secret: "secret"}); err != nil {
		t.Fatal(err)
	}
	wrong, _, err := credential.GenerateKeyring()
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := CaptureOffline(ctx, db, client, func(context.Context, int) (storage.Driver, error) {
		t.Fatal("empty backup should not read objects")
		return nil, nil
	}, OfflineOptions{Directory: filepath.Join(t.TempDir(), "incomplete"), ExpiresAt: time.Now().UTC().Add(time.Hour), Offline: true, Config: *cfg, Keys: wrong})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Status != "incomplete" {
		t.Fatal("wrong keyring accepted")
	}
	if ready, err := RestoreCheck(ctx, client, nil, "", manifest); err == nil || ready {
		t.Fatal("incomplete backup accepted for restoration")
	}
}

func TestReadManifestRejectsTrailingDataAndMissingProof(t *testing.T) {
	for _, data := range []string{`{"version":1,"id":"test","status":"metadata_only"} {}`, `{"version":1,"id":"test","status":"complete"}`} {
		path := filepath.Join(t.TempDir(), "manifest.json")
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadManifest(path); err == nil {
			t.Fatal("invalid manifest accepted")
		}
	}
}
