// Package storagebackup 捕获精确且固定的对象清单。它从不列出
// 或删除 bucket，也从不把单独的数据库转储视为完整备份。
package storagebackup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/backuppin"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/blob"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/bloblocation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/credentialversion"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageauthversion"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagebackup"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

type Manifest struct {
	Version        int       `json:"version"`
	ID             string    `json:"id"`
	CreatedAt      time.Time `json:"created_at"`
	ExpiresAt      time.Time `json:"expires_at"`
	Status         string    `json:"status"`
	Database       Artifact  `json:"database"`
	Keyring        Artifact  `json:"keyring"`
	Deployment     Artifact  `json:"deployment"`
	RequiredKeyIDs []string  `json:"required_key_ids"`
	Objects        []Object  `json:"objects"`
	Spaces         []Space   `json:"spaces"`
	Failures       []string  `json:"failures,omitempty"`
}

type Artifact struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type Object struct {
	BlobID     int     `json:"blob_id"`
	LocationID int     `json:"location_id"`
	SpaceID    int     `json:"space_id"`
	Key        string  `json:"key"`
	Version    string  `json:"version,omitempty"`
	Size       *int64  `json:"size,omitempty"`
	SHA256     *string `json:"sha256,omitempty"`
	Status     string  `json:"status"`
	Error      string  `json:"error,omitempty"`
}
type Space struct {
	ID           int    `json:"id"`
	Identity     string `json:"identity"`
	MarkerNonce  string `json:"marker_nonce"`
	ConnectionID int    `json:"connection_id"`
	Driver       string `json:"driver"`
	BackendID    string `json:"backend_id,omitempty"`
	Endpoint     string `json:"endpoint,omitempty"`
	Bucket       string `json:"bucket,omitempty"`
	Prefix       string `json:"prefix,omitempty"`
}
type Resolver func(context.Context, int) (storage.Driver, error)

// CaptureMetadata 要求调用方的维护闸门在捕获期间暂停 GC 与文件
// 变更。由此产生的固定记录会存活过该维护窗口。
func CaptureMetadata(ctx context.Context, client *ent.Client, expires time.Time) (*ent.StorageBackup, *Manifest, error) {
	now := time.Now().UTC()
	if !expires.After(now) || expires.After(now.Add(366*24*time.Hour)) {
		return nil, nil, errors.New("backup retention must be finite and at most 366 days")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, nil, err
	}
	manifest := &Manifest{Version: 1, ID: hex.EncodeToString(random[:]), CreatedAt: now, ExpiresAt: expires.UTC(), Status: "metadata_only"}
	tx, err := client.Tx(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	row, err := tx.StorageBackup.Create().SetIdentity(manifest.ID).SetExpiresAt(expires.UTC()).Save(ctx)
	if err != nil {
		return nil, nil, err
	}
	objects, err := tx.Blob.Query().Where(blob.StatusEQ(blob.StatusReady)).Order(ent.Asc(blob.FieldID)).Limit(100001).All(ctx)
	if err != nil {
		return nil, nil, err
	}
	if len(objects) > 100000 {
		return nil, nil, storage.ErrLimit
	}
	spaceSeen := map[int]bool{}
	for _, object := range objects {
		if object.ActiveLocationID == nil {
			return nil, nil, errors.New("ready object is missing its active location")
		}
		location, err := tx.BlobLocation.Get(ctx, *object.ActiveLocationID)
		if err != nil {
			return nil, nil, err
		}
		if location.BlobID == nil || *location.BlobID != object.ID {
			return nil, nil, errors.New("active location belongs to another object")
		}
		count, err := tx.BlobLocation.Update().Where(bloblocation.IDEQ(location.ID), bloblocation.StatusIn(bloblocation.StatusLive, bloblocation.StatusRetired)).SetStatus(location.Status).Save(ctx)
		if err != nil {
			return nil, nil, err
		}
		if count != 1 {
			return nil, nil, errors.New("location is already being deleted")
		}
		if _, err := tx.BackupPin.Create().SetBackupID(manifest.ID).SetLocationID(location.ID).SetExpiresAt(expires.UTC()).Save(ctx); err != nil {
			return nil, nil, err
		}
		manifest.Objects = append(manifest.Objects, Object{BlobID: object.ID, LocationID: location.ID, SpaceID: location.SpaceID, Key: location.ObjectKey, Version: location.ProviderVersion, Size: object.Size, SHA256: object.Sha256, Status: "unverified"})
		if !spaceSeen[location.SpaceID] {
			space, err := tx.StorageSpace.Get(ctx, location.SpaceID)
			if err != nil {
				return nil, nil, err
			}
			connection, err := tx.StorageConnection.Get(ctx, space.ConnectionID)
			if err != nil {
				return nil, nil, err
			}
			manifest.Spaces = append(manifest.Spaces, Space{ID: space.ID, Identity: space.Identity, MarkerNonce: space.MarkerNonce, ConnectionID: connection.ID, Driver: string(connection.Driver), BackendID: connection.BackendID, Endpoint: connection.Endpoint, Bucket: space.Bucket, Prefix: space.Prefix})
			spaceSeen[space.ID] = true
		}
	}
	llmKeys, err := tx.CredentialVersion.Query().Select(credentialversion.FieldKeyID).Strings(ctx)
	if err != nil {
		return nil, nil, err
	}
	cloudKeys, err := tx.StorageAuthVersion.Query().Select(storageauthversion.FieldKeyID).Strings(ctx)
	if err != nil {
		return nil, nil, err
	}
	keySet := map[string]bool{}
	for _, id := range append(llmKeys, cloudKeys...) {
		keySet[id] = true
	}
	for id := range keySet {
		manifest.RequiredKeyIDs = append(manifest.RequiredKeyIDs, id)
	}
	sort.Strings(manifest.RequiredKeyIDs)
	if err := tx.StorageBackup.UpdateOneID(row.ID).SetManifest(toMap(manifest)).Exec(ctx); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	row, err = client.StorageBackup.Get(ctx, row.ID)
	return row, manifest, err
}

func toMap(value any) map[string]any {
	data, _ := json.Marshal(value)
	result := map[string]any{}
	_ = json.Unmarshal(data, &result)
	return result
}

// VerifyObjects 仅读取已注册的 key/版本，并将完整字节
// 与可信的 Blob 基线比对。未知的遗留身份保持未验证状态。
func VerifyObjects(ctx context.Context, manifest *Manifest, resolve Resolver) error {
	for i := range manifest.Objects {
		if err := ctx.Err(); err != nil {
			return err
		}
		object := &manifest.Objects[i]
		object.Status = "unverified"
		object.Error = ""
		if object.Size == nil || object.SHA256 == nil {
			object.Error = "legacy_unverified"
			continue
		}
		if *object.Size < 0 || *object.Size == int64(^uint64(0)>>1) || !validDigest(*object.SHA256) {
			object.Error = "invalid_object_baseline"
			continue
		}
		driver, err := resolve(ctx, object.SpaceID)
		if err != nil {
			object.Error = errorCode(err)
			continue
		}
		reader, err := driver.Open(ctx, storage.Object{Key: object.Key, Version: object.Version})
		if err != nil {
			object.Error = errorCode(err)
			continue
		}
		hash := sha256.New()
		size, copyErr := io.Copy(hash, io.LimitReader(&contextReader{ctx, reader}, *object.Size+1))
		closeErr := reader.Close()
		if copyErr != nil {
			object.Error = errorCode(copyErr)
			continue
		}
		if closeErr != nil {
			object.Error = errorCode(closeErr)
			continue
		}
		if size != *object.Size || hex.EncodeToString(hash.Sum(nil)) != *object.SHA256 {
			object.Error = storage.ErrCorrupt.Error()
			continue
		}
		object.Status = "verified"
	}
	return nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
func errorCode(err error) string {
	for _, code := range []error{storage.ErrNotFound, storage.ErrCorrupt, storage.ErrAuthRequired, storage.ErrPermission, storage.ErrUnavailable, storage.ErrLimit} {
		if errors.Is(err, code) {
			return code.Error()
		}
	}
	return "storage_unavailable"
}

func Save(ctx context.Context, client *ent.Client, rowID int, manifest *Manifest) error {
	return client.StorageBackup.UpdateOneID(rowID).SetStatus(storagebackup.Status(manifest.Status)).SetManifest(toMap(manifest)).Exec(ctx)
}

func CheckReferences(ctx context.Context, client *ent.Client, manifest *Manifest) error {
	backup, err := client.StorageBackup.Query().Where(storagebackup.IdentityEQ(manifest.ID)).Only(ctx)
	if err != nil {
		return err
	}
	if !manifest.ExpiresAt.Equal(backup.ExpiresAt) {
		return errors.New("backup pin retention mismatch")
	}
	objects, err := client.Blob.Query().Where(blob.StatusEQ(blob.StatusReady)).Limit(100001).All(ctx)
	if err != nil {
		return err
	}
	if len(objects) != len(manifest.Objects) || len(objects) > 100000 {
		return errors.New("backup does not cover every ready object")
	}
	ready := make(map[int]*ent.Blob, len(objects))
	for _, object := range objects {
		ready[object.ID] = object
	}
	spaces := make(map[int]bool, len(manifest.Spaces))
	for _, saved := range manifest.Spaces {
		if spaces[saved.ID] {
			return errors.New("duplicate backup space")
		}
		spaces[saved.ID] = true
		space, err := client.StorageSpace.Get(ctx, saved.ID)
		if err != nil {
			return err
		}
		if space.Identity != saved.Identity || space.MarkerNonce != saved.MarkerNonce || space.ConnectionID != saved.ConnectionID || space.Bucket != saved.Bucket || space.Prefix != saved.Prefix {
			return fmt.Errorf("backup space %d identity mismatch", saved.ID)
		}
		connection, err := client.StorageConnection.Get(ctx, space.ConnectionID)
		if err != nil {
			return err
		}
		if string(connection.Driver) != saved.Driver || connection.BackendID != saved.BackendID || connection.Endpoint != saved.Endpoint {
			return fmt.Errorf("backup connection %d identity mismatch", connection.ID)
		}
	}
	for _, saved := range manifest.Objects {
		object := ready[saved.BlobID]
		if object == nil || object.ActiveLocationID == nil || *object.ActiveLocationID != saved.LocationID || !equalBaseline(object.Size, object.Sha256, saved.Size, saved.SHA256) || !spaces[saved.SpaceID] {
			return fmt.Errorf("backup object %d baseline mismatch", saved.BlobID)
		}
		delete(ready, saved.BlobID)
		location, err := client.BlobLocation.Get(ctx, saved.LocationID)
		if err != nil {
			return err
		}
		if location.BlobID == nil || *location.BlobID != saved.BlobID || location.SpaceID != saved.SpaceID || location.ObjectKey != saved.Key || location.ProviderVersion != saved.Version || location.Status == bloblocation.StatusDeleted || location.Status == bloblocation.StatusDeleting {
			return fmt.Errorf("backup location %d reference mismatch", saved.LocationID)
		}
		pinned, err := client.BackupPin.Query().Where(backuppin.BackupIDEQ(manifest.ID), backuppin.LocationIDEQ(saved.LocationID), backuppin.ExpiresAtGTE(manifest.ExpiresAt)).Exist(ctx)
		if err != nil {
			return err
		}
		if !pinned {
			return errors.New("backup object is missing its durable pin")
		}
	}
	llmKeys, err := client.CredentialVersion.Query().Select(credentialversion.FieldKeyID).Strings(ctx)
	if err != nil {
		return err
	}
	cloudKeys, err := client.StorageAuthVersion.Query().Select(storageauthversion.FieldKeyID).Strings(ctx)
	if err != nil {
		return err
	}
	keySet := map[string]bool{}
	for _, id := range append(llmKeys, cloudKeys...) {
		keySet[id] = true
	}
	if len(keySet) != len(manifest.RequiredKeyIDs) {
		return errors.New("backup required key set mismatch")
	}
	for _, id := range manifest.RequiredKeyIDs {
		if !keySet[id] {
			return errors.New("backup required key set mismatch")
		}
		delete(keySet, id)
	}
	return nil
}

func equalBaseline(leftSize *int64, leftSHA *string, rightSize *int64, rightSHA *string) bool {
	return (leftSize == nil && rightSize == nil || leftSize != nil && rightSize != nil && *leftSize == *rightSize) &&
		(leftSHA == nil && rightSHA == nil || leftSHA != nil && rightSHA != nil && *leftSHA == *rightSHA)
}

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}
