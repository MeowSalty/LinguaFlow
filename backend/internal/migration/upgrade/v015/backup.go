package v015

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/snapshot"
	storagemigrate "github.com/MeowSalty/LinguaFlow/backend/internal/migration/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/localstore"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/storeutil"
)

type BackupObject struct {
	ResourceID     int    `json:"resource_id,omitempty"`
	LocationID     int    `json:"location_id,omitempty"`
	SpaceID        int    `json:"space_id,omitempty"`
	Key            string `json:"key"`
	Version        string `json:"provider_version,omitempty"`
	Driver         string `json:"driver"`
	Root           string `json:"root,omitempty"`
	Status         string `json:"status"`
	Size           int64  `json:"size"`
	SHA256         string `json:"sha256,omitempty"`
	Recovery       string `json:"recovery,omitempty"`
	ExpectedSHA256 string `json:"expected_sha256,omitempty"`
	ExpectedSize   *int64 `json:"expected_size,omitempty"`
	ObservedStatus string `json:"observed_status,omitempty"`
	ObservedSize   int64  `json:"observed_size,omitempty"`
	ObservedSHA256 string `json:"observed_sha256,omitempty"`
}

type BackupReceipt struct {
	Version           int                           `json:"version"`
	ID                string                        `json:"operation_id"`
	SourceFingerprint string                        `json:"source_fingerprint"`
	PlanDigest        string                        `json:"plan_digest"`
	CreatedAt         time.Time                     `json:"created_at"`
	DirectoryIdentity string                        `json:"directory_identity"`
	Database          operation.Artifact            `json:"database"`
	Keyring           operation.Artifact            `json:"keyring"`
	Deployment        operation.Artifact            `json:"deployment"`
	Objects           []BackupObject                `json:"objects"`
	Markers           map[string]operation.Artifact `json:"markers,omitempty"`
	Complete          bool                          `json:"complete"`
}

// CaptureSourceBackup uses raw released DTOs and a low-level snapshot. It never
// creates a source policy, auth marker, pin, or format reservation.
func CaptureSourceBackup(ctx context.Context, db *sql.DB, resolved *config.ResolvedServer, p *operation.Plan, directory string) (*BackupReceipt, error) {
	if _, err := os.Lstat(directory); !errors.Is(err, os.ErrNotExist) {
		return nil, operation.Fail(operation.BackupIncomplete, "backup directory already exists without a complete receipt; retain it and use a new operation")
	}
	if err := operation.ValidatePath(filepath.Dir(directory), true); err != nil {
		return nil, err
	}
	if err := credential.CreatePrivateDirectory(directory); err != nil {
		return nil, err
	}
	identity, err := operation.Identity(directory)
	if err != nil {
		return nil, err
	}
	receipt := &BackupReceipt{Version: 1, ID: p.ID, SourceFingerprint: p.SourceFingerprint, PlanDigest: operation.Digest(p), CreatedAt: time.Now().UTC(), DirectoryIdentity: identity}
	name := "database.sqlite"
	if p.Source.Driver == "postgres" {
		name = "database.pgdump"
	}
	target := filepath.Join(directory, name)
	if p.Source.Driver == "sqlite" {
		err = snapshot.SQLite(ctx, db, target)
	} else {
		err = snapshot.Postgres(ctx, db, &resolved.Config, target)
	}
	if err != nil {
		return nil, err
	}
	receipt.Database, err = operation.Hash(ctx, target)
	if err != nil {
		return nil, err
	}
	if err := VerifySourceKeys(ctx, db, resolved.CredentialKeys); err != nil {
		return nil, err
	}
	if resolved.CredentialKeys != nil {
		data, err := credential.EncodeKeyring(resolved.CredentialKeys)
		if err != nil {
			return nil, err
		}
		if _, err := credential.PublishPrivateFile(filepath.Join(directory, "keyring.json"), data); err != nil {
			return nil, err
		}
		receipt.Keyring, err = operation.Hash(ctx, filepath.Join(directory, "keyring.json"))
		if err != nil {
			return nil, err
		}
	}
	deployment, err := json.MarshalIndent(resolved.Config, "", "  ")
	if err != nil {
		return nil, err
	}
	if _, err := credential.PublishPrivateFile(filepath.Join(directory, "deployment.json"), deployment); err != nil {
		return nil, err
	}
	receipt.Deployment, err = operation.Hash(ctx, filepath.Join(directory, "deployment.json"))
	if err != nil {
		return nil, err
	}
	objects, err := SourceObjects(ctx, db, resolved.Config, p.LegacyRoot)
	if err != nil {
		return nil, err
	}
	receipt.Objects = objects
	if p.ObjectsDigest != "" && ObjectsDigest(objects) != p.ObjectsDigest {
		return nil, operation.Fail(operation.BaselineChanged, "referenced object set changed before backup")
	}
	objectsDir := filepath.Join(directory, "objects")
	if err := credential.CreatePrivateDirectory(objectsDir); err != nil {
		return nil, err
	}
	for i := range receipt.Objects {
		object := &receipt.Objects[i]
		if object.Driver == "s3" {
			continue
		}
		if object.Root == "" {
			object.Status = "unavailable"
			continue
		}
		store, err := localstore.OpenExisting(object.Root)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, storage.ErrNotFound) {
				object.Status = "missing"
				continue
			}
			return nil, operation.Fail(operation.BackupIncomplete, "cannot open the referenced local storage root")
		}
		reader, err := store.Open(ctx, storage.Object{Key: object.Key, Version: object.Version})
		if err != nil {
			store.Close()
			if errors.Is(err, storage.ErrNotFound) {
				object.Status = "missing"
				continue
			}
			return nil, operation.Fail(operation.BackupIncomplete, "cannot read an exact referenced local object")
		}
		name := fmt.Sprintf("%06d.bin", i)
		proof, copyErr := operation.Publish(ctx, filepath.Join(objectsDir, name), io.LimitReader(reader, resolved.Config.Storage.Limits.MaxFileBytes+1))
		closeErr := errors.Join(reader.Close(), store.Close())
		if err := errors.Join(copyErr, closeErr); err != nil {
			return nil, err
		}
		if proof.Size > resolved.Config.Storage.Limits.MaxFileBytes {
			return nil, operation.Fail(operation.BackupIncomplete, "referenced object exceeds the explicit backup limit")
		}
		if object.ExpectedSize != nil && (*object.ExpectedSize != proof.Size || object.ExpectedSHA256 != proof.SHA256) {
			return nil, operation.Fail(operation.BackupIncomplete, "referenced object differs from its preserved digest")
		}
		object.Status = "copied"
		object.Size = proof.Size
		object.SHA256 = proof.SHA256
		object.Recovery = "objects/" + name
	}
	receipt.Markers = map[string]operation.Artifact{}
	for i, root := range p.LocalRoots {
		marker := filepath.Join(root.Source, ".linguaflow", "space.json")
		if root.MarkerDigest == "" {
			if _, err := os.Lstat(marker); err == nil {
				return nil, operation.Fail(operation.BaselineChanged, "storage marker appeared after planning")
			}
			continue
		}
		proof, err := operation.Copy(ctx, marker, filepath.Join(directory, fmt.Sprintf("marker-%06d.json", i)))
		if err != nil || proof.SHA256 != root.MarkerDigest {
			return nil, operation.Fail(operation.BackupIncomplete, "storage identity marker changed")
		}
		receipt.Markers[root.BackendID] = proof
	}
	if err := VerifyExternalBackup(ctx, db, resolved.Config, resolved.CredentialKeys, p, receipt.Objects); err != nil {
		return nil, err
	}
	after, err := Fingerprint(ctx, db, p.Source.Driver)
	if err != nil {
		return nil, err
	}
	if after != p.SourceFingerprint {
		return nil, operation.Fail(operation.BaselineChanged, "source changed during backup; preserve the snapshot and create a new plan")
	}
	receipt.Complete = true
	if err := operation.Write(filepath.Join(directory, "receipt.json"), receipt, true); err != nil {
		return nil, err
	}
	return receipt, VerifyBackupReceipt(ctx, directory, p, receipt)
}

func LoadBackupReceipt(ctx context.Context, directory string, p *operation.Plan) (*BackupReceipt, error) {
	r := new(BackupReceipt)
	if err := operation.Read(filepath.Join(directory, "receipt.json"), r); err != nil {
		return nil, operation.Fail(operation.BackupIncomplete, "complete backup receipt is missing")
	}
	if err := VerifyBackupReceipt(ctx, directory, p, r); err != nil {
		return nil, err
	}
	return r, nil
}

func VerifyBackupReceipt(ctx context.Context, directory string, p *operation.Plan, r *BackupReceipt) error {
	identity, err := operation.Identity(directory)
	if err != nil || identity != r.DirectoryIdentity {
		return operation.Fail(operation.BackupIncomplete, "backup directory identity changed")
	}
	if r.Version != 1 || !r.Complete || r.ID != p.ID || r.PlanDigest != operation.Digest(p) || r.SourceFingerprint != p.SourceFingerprint || r.CreatedAt.IsZero() {
		return operation.Fail(operation.BackupIncomplete, "receipt does not bind this operation and source")
	}
	if p.ObjectsDigest != "" && ObjectsDigest(r.Objects) != p.ObjectsDigest {
		return operation.Fail(operation.BackupIncomplete, "backup receipt omits or changes exact referenced objects")
	}
	for _, root := range p.LocalRoots {
		if root.MarkerDigest == "" {
			continue
		}
		proof, ok := r.Markers[root.BackendID]
		if !ok || proof.SHA256 != root.MarkerDigest || filepath.Base(proof.Name) != proof.Name {
			return operation.Fail(operation.BackupIncomplete, "storage marker recovery evidence is incomplete")
		}
		actual, err := operation.Hash(ctx, filepath.Join(directory, proof.Name))
		if err != nil || actual != proof {
			return operation.Fail(operation.BackupIncomplete, "storage marker recovery bytes changed")
		}
	}
	for _, expected := range []operation.Artifact{r.Database, r.Keyring, r.Deployment} {
		if expected.Name == "" {
			continue
		}
		if filepath.Base(expected.Name) != expected.Name || !operation.ValidHex(expected.SHA256, 64) || expected.Size <= 0 {
			return operation.Fail(operation.BackupIncomplete, "invalid backup artifact evidence")
		}
		actual, err := operation.Hash(ctx, filepath.Join(directory, expected.Name))
		if err != nil || actual.Size != expected.Size || actual.SHA256 != expected.SHA256 {
			return operation.Fail(operation.BackupIncomplete, "backup artifact bytes changed")
		}
	}
	if r.Database.Name == "" || r.Deployment.Name == "" {
		return operation.Fail(operation.BackupIncomplete, "database and deployment recovery artifacts are required")
	}
	var deployment config.ServerConfig
	if err := operation.Read(filepath.Join(directory, r.Deployment.Name), &deployment); err != nil {
		return operation.Fail(operation.BackupIncomplete, "deployment recovery inputs cannot be read")
	}
	var keys *credential.Keyring
	if r.Keyring.Name != "" {
		keys, err = credential.LoadKeyring(filepath.Join(directory, r.Keyring.Name))
		if err != nil {
			return operation.Fail(operation.BackupIncomplete, "recovery keyring cannot be read")
		}
	}
	if p.Source.InputDigest != "" && InputDigest(&config.ResolvedServer{Config: deployment, CredentialKeys: keys}) != p.Source.InputDigest {
		return operation.Fail(operation.BackupIncomplete, "deployment or keyring recovery inputs differ from the plan")
	}
	if p.Source.Driver == "sqlite" {
		cfg := config.ServerConfig{DataDir: directory, Database: config.DatabaseConfig{Driver: "sqlite", DSN: filepath.Join(directory, r.Database.Name)}}
		db, _, err := snapshot.Open(ctx, &cfg, true)
		if err != nil {
			return operation.Fail(operation.BackupIncomplete, "source database snapshot cannot be read")
		}
		manifestOperation := ""
		if p.Kind == "storage" && p.ImportedManifest != "" {
			m, err := storagemigrate.ReadManifest(p.StorageManifest)
			if err != nil {
				db.Close()
				return operation.Fail(operation.BackupIncomplete, "published manifest recovery plan unavailable")
			}
			manifestOperation = m.OperationID
		}
		probe, probeErr := ProbeOperation(ctx, db, "sqlite", nil, manifestOperation)
		keyErr := VerifySourceKeys(ctx, db, keys)
		db.Close()
		if probeErr != nil || keyErr != nil || probe.Fingerprint != p.SourceFingerprint || probe.SchemaDigest != p.SourceSchema {
			return operation.Fail(operation.BackupIncomplete, "database snapshot or decryption evidence differs from the source")
		}
	}
	for _, object := range r.Objects {
		if object.Driver == "s3" {
			continue
		}
		if object.Status == "missing" || object.Status == "unavailable" {
			if object.Status != object.ObservedStatus {
				return operation.Fail(operation.BackupIncomplete, "readable source bytes cannot be omitted from recovery")
			}
			continue
		}
		if object.ObservedStatus != "readable" || object.Size != object.ObservedSize || object.SHA256 != object.ObservedSHA256 {
			return operation.Fail(operation.BackupIncomplete, "local recovery differs from the object baseline")
		}
		if object.Status != "copied" || object.Recovery == "" || storeutil.ValidateKey(object.Recovery) != nil {
			return operation.Fail(operation.BackupIncomplete, "invalid local recovery location")
		}
		actual, err := operation.Hash(ctx, filepath.Join(directory, filepath.FromSlash(object.Recovery)))
		if err != nil || actual.Size != object.Size || actual.SHA256 != object.SHA256 {
			return operation.Fail(operation.BackupIncomplete, "independent local recovery bytes are missing or changed")
		}
	}
	return nil
}

func SourceObjects(ctx context.Context, db *sql.DB, cfg config.ServerConfig, legacyRoot string) ([]BackupObject, error) {
	rows, err := db.QueryContext(ctx, "SELECT id,storage_path FROM resources WHERE current_source_revision_id IS NULL AND storage_path<>'' ORDER BY id")
	if err != nil {
		return nil, err
	}
	var objects []BackupObject
	for rows.Next() {
		object := BackupObject{Driver: "local", Root: legacyRoot, Status: "unverified"}
		if err := rows.Scan(&object.ResourceID, &object.Key); err != nil {
			rows.Close()
			return nil, err
		}
		object.Key = strings.ReplaceAll(object.Key, "\\", "/")
		if storeutil.ValidateKey(object.Key) != nil {
			object.Status = "unavailable"
			object.Root = ""
		}
		objects = append(objects, object)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	rows, err = db.QueryContext(ctx, `SELECT l.id,l.space_id,l.object_key,l.provider_version,l.size,c.driver,COALESCE(c.backend_id,''),COALESCE(b.sha256,'') FROM blob_locations l LEFT JOIN blobs b ON b.id=l.blob_id JOIN storage_spaces sp ON sp.id=l.space_id JOIN storage_connections c ON c.id=sp.connection_id WHERE l.status<>'deleted' AND l.delete_marker=false ORDER BY l.id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		object := BackupObject{Status: "unverified"}
		var backend string
		if err := rows.Scan(&object.LocationID, &object.SpaceID, &object.Key, &object.Version, &object.Size, &object.Driver, &backend, &object.ExpectedSHA256); err != nil {
			rows.Close()
			return nil, err
		}
		if object.ExpectedSHA256 != "" {
			size := object.Size
			object.ExpectedSize = &size
		}
		if object.Driver == "local" {
			for _, b := range cfg.Storage.Backends {
				if b.ID == backend && b.Driver == "local" {
					object.Root = b.Root
				}
			}
			if backend == "legacy" {
				object.Root = legacyRoot
			}
			if backend == "local" && len(cfg.Storage.Backends) == 0 {
				object.Root = filepath.Join(cfg.DataDir, "objects")
			}
		}
		objects = append(objects, object)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	for i := range objects {
		o := &objects[i]
		if o.Driver != "local" {
			continue
		}
		if o.Root == "" || storeutil.ValidateKey(o.Key) != nil {
			o.Root = ""
			o.ObservedStatus = "unavailable"
			continue
		}
		store, err := localstore.OpenExisting(o.Root)
		if errors.Is(err, storage.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			o.ObservedStatus = "missing"
			continue
		}
		if err != nil {
			return nil, operation.Fail(operation.BackupIncomplete, "local object root cannot be observed safely")
		}
		reader, err := store.Open(ctx, storage.Object{Key: o.Key, Version: o.Version})
		if errors.Is(err, storage.ErrNotFound) {
			store.Close()
			o.ObservedStatus = "missing"
			continue
		}
		if err != nil {
			store.Close()
			return nil, operation.Fail(operation.BackupIncomplete, "local object baseline cannot be read")
		}
		size, digest, readErr := hashReader(ctx, reader, cfg.Storage.Limits.MaxFileBytes)
		closeErr := errors.Join(reader.Close(), store.Close())
		if err := errors.Join(readErr, closeErr); err != nil {
			return nil, err
		}
		o.ObservedStatus, o.ObservedSize, o.ObservedSHA256 = "readable", size, digest
	}
	return objects, nil
}

// VerifySourceKeys reads only fields that exist in the frozen release.
func VerifySourceKeys(ctx context.Context, db *sql.DB, keys *credential.Keyring) error {
	rows, err := db.QueryContext(ctx, `SELECT v.credential_id,v.version,v.encryption_version,v.key_id,v.nonce,v.ciphertext,c.provider,c.endpoint,c.scope,c.owner_id FROM credential_versions v JOIN credentials c ON c.id=v.credential_id ORDER BY v.id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var aad credential.AssociatedData
		var encrypted credential.Ciphertext
		if err := rows.Scan(&aad.ID, &aad.Version, &encrypted.Version, &encrypted.KeyID, &encrypted.Nonce, &encrypted.Data, &aad.Provider, &aad.Endpoint, &aad.Scope, &aad.OwnerID); err != nil {
			rows.Close()
			return err
		}
		if keys == nil {
			rows.Close()
			return operation.Fail(operation.BackupIncomplete, "source encryption keys are unavailable")
		}
		if _, err := keys.Decrypt(encrypted, aad); err != nil {
			rows.Close()
			return operation.Fail(operation.BackupIncomplete, "source LLM credential cannot be recovered with these keys")
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	return verifySourceStorageKeys(ctx, db, keys)
}

func hashReader(ctx context.Context, r io.Reader, limit int64) (int64, string, error) {
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(&backupReader{ctx: ctx, r: r}, limit+1))
	if err != nil {
		return 0, "", err
	}
	if n > limit {
		return 0, "", operation.Fail(operation.BackupIncomplete, "object exceeds backup limit")
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}

type backupReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *backupReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(b)
}

func objectIdentity(o BackupObject) string {
	return operation.Digest([]any{o.ResourceID, o.LocationID, o.SpaceID, o.Key, o.Version, o.Driver, o.Root, o.ExpectedSHA256, o.ExpectedSize, o.ObservedStatus, o.ObservedSize, o.ObservedSHA256})
}

func ObjectsDigest(objects []BackupObject) string { return operation.Digest(sortedObjectIDs(objects)) }
func sortedObjectIDs(objects []BackupObject) []string {
	ids := []string{}
	for _, o := range objects {
		ids = append(ids, objectIdentity(o))
	}
	sort.Strings(ids)
	return ids
}
