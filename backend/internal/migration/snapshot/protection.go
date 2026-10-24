package snapshot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"database/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
)

const Captured = "captured"
const OperatorDeclared = "operator_declared"

// ProtectionReceipt describes the actual protection, never provider restore proof.
type ProtectionReceipt struct {
	Version           int                          `json:"version"`
	ID                string                       `json:"operation_id"`
	SourceFingerprint string                       `json:"source_fingerprint"`
	SourceIdentity    string                       `json:"source_identity"`
	PlanDigest        string                       `json:"plan_digest"`
	BaselineDigest    string                       `json:"baseline_digest"`
	Mode              operation.DatabaseBackupMode `json:"database_backup"`
	State             string                       `json:"protection"`
	Reference         string                       `json:"backup_reference,omitempty"`
	CreatedAt         time.Time                    `json:"created_at"`
	DirectoryIdentity string                       `json:"directory_identity"`
	Database          operation.Artifact           `json:"database,omitempty"`
	Keyring           operation.Artifact           `json:"keyring,omitempty"`
	Deployment        operation.Artifact           `json:"deployment"`
}

func InputDigest(resolved *config.ResolvedServer) string {
	var keys []byte
	if resolved.CredentialKeys != nil {
		keys, _ = credential.EncodeKeyring(resolved.CredentialKeys)
	}
	return operation.Digest(struct {
		Config config.ServerConfig
		Keys   []byte
	}{resolved.Config, keys})
}

// CaptureProtection only writes operation-owned artifacts. Inplace routes pass
// no object writers: originals remain references, not additional backup claims.
func CaptureProtection(ctx context.Context, db *sql.DB, resolved *config.ResolvedServer, p *operation.Plan, directory, reference string) (*ProtectionReceipt, error) {
	if p.Baseline == nil || p.Baseline.Bind(p) != nil {
		return nil, operation.Fail(operation.BackupIncomplete, "capture requires a durable original baseline")
	}
	if p.DatabaseBackup == operation.BackupExternal && strings.TrimSpace(reference) == "" {
		return nil, operation.Fail(operation.BackupIncomplete, "provide --backup-reference for the stopped PostgreSQL source")
	}
	if p.DatabaseBackup == operation.BackupExternal && strings.TrimSpace(reference) != p.Baseline.Facts["backup_reference"] {
		return nil, operation.Fail(operation.Incompatible, "backup declaration differs from the bound original baseline")
	}
	if p.DatabaseBackup == operation.BackupAuto && strings.TrimSpace(reference) != "" {
		return nil, operation.Fail(operation.Incompatible, "backup-reference only applies to external database protection")
	}
	if p.DatabaseBackup == operation.BackupAuto && p.Source.Driver == "postgres" {
		if _, err := InspectPostgresDump(ctx); err != nil {
			return nil, err
		}
	}
	if _, err := os.Lstat(directory); !errors.Is(err, os.ErrNotExist) {
		return nil, operation.Fail(operation.BackupIncomplete, "protection directory is occupied; retain incomplete materials")
	}
	if err := operation.ValidatePath(filepath.Dir(directory), true); err != nil {
		return nil, err
	}
	if err := credential.CreatePrivateDirectory(directory); err != nil {
		return nil, err
	}
	id, err := operation.Identity(directory)
	if err != nil {
		return nil, err
	}
	r := &ProtectionReceipt{Version: operation.Version, ID: p.ID, SourceFingerprint: p.SourceFingerprint, SourceIdentity: p.Source.Identity, PlanDigest: operation.Digest(p), BaselineDigest: operation.BaselineDigest(p), Mode: p.DatabaseBackup, CreatedAt: time.Now().UTC(), DirectoryIdentity: id}
	if p.DatabaseBackup == operation.BackupExternal {
		r.State, r.Reference = OperatorDeclared, strings.TrimSpace(reference)
		r.CreatedAt = p.Baseline.ConversionTime
	} else {
		r.State = Captured
		name := "database.sqlite"
		if p.Source.Driver == "postgres" {
			name = "database.pgdump"
		}
		path := filepath.Join(directory, name)
		if p.Source.Driver == "sqlite" {
			err = SQLite(ctx, db, path)
		} else {
			err = Postgres(ctx, db, &resolved.Config, path)
		}
		if err != nil {
			return nil, err
		}
		r.Database, err = operation.Hash(ctx, path)
		if err != nil {
			return nil, err
		}
	}
	if resolved.CredentialKeys != nil {
		data, e := credential.EncodeKeyring(resolved.CredentialKeys)
		if e != nil {
			return nil, e
		}
		if _, err := credential.PublishPrivateFile(filepath.Join(directory, "keyring.json"), data); err != nil {
			return nil, err
		}
		r.Keyring, err = operation.Hash(ctx, filepath.Join(directory, "keyring.json"))
		if err != nil {
			return nil, err
		}
	}
	data, err := json.MarshalIndent(resolved.Config, "", "  ")
	if err != nil {
		return nil, err
	}
	if _, err := credential.PublishPrivateFile(filepath.Join(directory, "deployment.json"), data); err != nil {
		return nil, err
	}
	r.Deployment, err = operation.Hash(ctx, filepath.Join(directory, "deployment.json"))
	if err != nil {
		return nil, err
	}
	if err := VerifyProtection(ctx, directory, p, r); err != nil {
		return nil, err
	}
	if err := operation.Write(filepath.Join(directory, "protection.json"), r, true); err != nil {
		return nil, err
	}
	return r, nil
}

func LoadProtection(ctx context.Context, directory string, p *operation.Plan) (*ProtectionReceipt, error) {
	r := new(ProtectionReceipt)
	if err := operation.Read(filepath.Join(directory, "protection.json"), r); err != nil {
		return nil, operation.Fail(operation.BackupIncomplete, "original protection record is missing")
	}
	return r, VerifyProtection(ctx, directory, p, r)
}

func VerifyProtection(ctx context.Context, directory string, p *operation.Plan, r *ProtectionReceipt) error {
	id, err := operation.Identity(directory)
	if err != nil || id != r.DirectoryIdentity || r.Version != operation.Version || r.ID != p.ID || r.PlanDigest != operation.Digest(p) || r.SourceIdentity != p.Source.Identity || r.SourceFingerprint != p.SourceFingerprint || r.BaselineDigest != operation.BaselineDigest(p) || r.BaselineDigest == "" || r.Mode != p.DatabaseBackup || r.CreatedAt.IsZero() {
		return operation.Fail(operation.BackupIncomplete, "protection does not bind this operation and original baseline")
	}
	if r.Mode == operation.BackupExternal {
		if p.Source.Driver != "postgres" || p.Strategy != operation.Inplace || r.State != OperatorDeclared || strings.TrimSpace(r.Reference) == "" || r.Database.Name != "" || r.Reference != p.Baseline.Facts["backup_reference"] || !r.CreatedAt.Equal(p.Baseline.ConversionTime) {
			return operation.Fail(operation.BackupIncomplete, "invalid or changed external PostgreSQL declaration")
		}
	} else if r.State != Captured || r.Database.Name == "" || r.Reference != "" {
		return operation.Fail(operation.BackupIncomplete, "automatic database snapshot is incomplete")
	}
	if r.Deployment.Name == "" {
		return operation.Fail(operation.BackupIncomplete, "deployment recovery material is missing")
	}
	for _, a := range []operation.Artifact{r.Database, r.Keyring, r.Deployment} {
		if a.Name == "" {
			continue
		}
		if filepath.Base(a.Name) != a.Name || !operation.ValidHex(a.SHA256, 64) || a.Size <= 0 {
			return operation.Fail(operation.BackupIncomplete, "invalid protection artifact")
		}
		actual, err := operation.Hash(ctx, filepath.Join(directory, a.Name))
		if err != nil || actual != a {
			return operation.Fail(operation.BackupIncomplete, "protection artifact changed or is missing")
		}
	}
	var cfg config.ServerConfig
	if err := operation.Read(filepath.Join(directory, r.Deployment.Name), &cfg); err != nil {
		return err
	}
	var keys *credential.Keyring
	if r.Keyring.Name != "" {
		keys, err = credential.LoadKeyring(filepath.Join(directory, r.Keyring.Name))
		if err != nil {
			return err
		}
	}
	if InputDigest(&config.ResolvedServer{Config: cfg, CredentialKeys: keys}) != p.Source.InputDigest {
		return operation.Fail(operation.BackupIncomplete, "protected configuration or keyring differs from the plan")
	}
	if r.Mode == operation.BackupAuto && p.Source.Driver == "postgres" {
		f, err := os.Open(filepath.Join(directory, r.Database.Name))
		if err != nil {
			return err
		}
		var magic [5]byte
		_, readErr := io.ReadFull(f, magic[:])
		closeErr := f.Close()
		if errors.Join(readErr, closeErr) != nil || string(magic[:]) != "PGDMP" {
			return operation.Fail(operation.BackupIncomplete, "captured PostgreSQL custom archive is unreadable")
		}
	}
	return nil
}
