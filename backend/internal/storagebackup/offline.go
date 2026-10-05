package storagebackup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"modernc.org/sqlite"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

type OfflineOptions struct {
	Directory string
	ExpiresAt time.Time
	Offline   bool
	Config    config.ServerConfig
	Keys      *credential.Keyring
}

// CaptureOffline 将 DB、所有已解析的密钥与部署输入保存到一个
// 私有备份目录中。它会固定源位置；它不会暗中将
// BYOS 对象复制到站点。外部云生命周期策略仍保持外部。
func CaptureOffline(ctx context.Context, db *sql.DB, client *ent.Client, resolve Resolver, options OfflineOptions) (*Manifest, error) {
	if !options.Offline {
		return nil, errors.New("full backup requires all service writers and GC to be stopped")
	}
	if strings.TrimSpace(options.Directory) == "" {
		return nil, errors.New("backup destination is required")
	}
	if _, err := os.Lstat(options.Directory); !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("backup destination must not already exist")
	}
	if err := credential.PreparePrivateDirectory(options.Directory); err != nil {
		return nil, err
	}
	row, manifest, err := CaptureMetadata(ctx, client, options.ExpiresAt)
	if err != nil {
		return nil, err
	}
	manifest.Status = "incomplete"
	if err := VerifyObjects(ctx, manifest, resolve); err != nil {
		manifest.Failures = append(manifest.Failures, "object_verification_interrupted")
	}
	if err := CheckReferences(ctx, client, manifest); err != nil {
		manifest.Failures = append(manifest.Failures, "database_references_changed")
	}
	for _, id := range manifest.RequiredKeyIDs {
		if !options.Keys.HasKey(id) {
			manifest.Failures = append(manifest.Failures, "required_key_unavailable:"+id)
		}
	}
	if err := VerifyKeys(ctx, client, options.Keys); err != nil {
		manifest.Failures = append(manifest.Failures, "credential_decryption_failed")
	}
	if options.Keys != nil {
		data, err := credential.EncodeKeyring(options.Keys)
		if err != nil {
			manifest.Failures = append(manifest.Failures, "keyring_encoding_failed")
		} else if manifest.Keyring, err = publish(options.Directory, "keyring.json", data); err != nil {
			manifest.Failures = append(manifest.Failures, "keyring_backup_failed")
		}
	}
	deployment, err := json.MarshalIndent(options.Config, "", "  ")
	if err != nil {
		manifest.Failures = append(manifest.Failures, "deployment_encoding_failed")
	} else if manifest.Deployment, err = publish(options.Directory, "deployment.json", deployment); err != nil {
		manifest.Failures = append(manifest.Failures, "deployment_backup_failed")
	}
	if err := Save(ctx, client, row.ID, manifest); err != nil {
		return manifest, err
	}
	databaseName := "database.sqlite"
	if options.Config.Database.Driver == config.DatabaseDriverPostgres {
		databaseName = "database.pgdump"
	}
	databasePath := filepath.Join(options.Directory, databaseName)
	if options.Config.Database.Driver == config.DatabaseDriverPostgres {
		err = snapshotPostgres(ctx, db, options.Config.DatabaseDSN(), databasePath)
	} else {
		err = snapshotSQLite(ctx, db, databasePath)
	}
	if err != nil {
		manifest.Failures = append(manifest.Failures, "database_backup_failed")
	} else if manifest.Database, err = hashArtifact(databasePath); err != nil {
		manifest.Failures = append(manifest.Failures, "database_backup_verification_failed")
	}
	complete := len(manifest.Failures) == 0 && manifest.Database.SHA256 != "" && manifest.Deployment.SHA256 != "" && (len(manifest.RequiredKeyIDs) == 0 || manifest.Keyring.SHA256 != "")
	for _, object := range manifest.Objects {
		if object.Status != "verified" {
			complete = false
		}
	}
	if complete {
		manifest.Status = "complete"
	}
	if err := Save(ctx, client, row.ID, manifest); err != nil {
		return manifest, err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return manifest, err
	}
	if _, err := publish(options.Directory, "manifest.json", data); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func publish(directory, name string, data []byte) (Artifact, error) {
	path := filepath.Join(directory, name)
	created, err := credential.PublishPrivateFile(path, data)
	if err != nil {
		return Artifact{}, err
	}
	if !created {
		return Artifact{}, errors.New("backup artifact already exists")
	}
	return hashArtifact(path)
}

func hashArtifact(path string) (Artifact, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return Artifact{}, err
	}
	if !before.Mode().IsRegular() {
		return Artifact{}, errors.New("backup artifact must not be a link or special file")
	}
	f, err := os.Open(path)
	if err != nil {
		return Artifact{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) {
		return Artifact{}, errors.New("backup artifact must be a regular file")
	}
	hash := sha256.New()
	size, err := io.Copy(hash, f)
	if err != nil {
		return Artifact{}, err
	}
	return Artifact{Name: filepath.Base(path), Size: size, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

func snapshotSQLite(ctx context.Context, db *sql.DB, target string) (err error) {
	connection, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer connection.Close()
	err = connection.Raw(func(raw any) (err error) {
		provider, ok := raw.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		})
		if !ok {
			return errors.New("SQLite consistent backup unavailable")
		}
		path := filepath.ToSlash(target)
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		uri := url.URL{Scheme: "file", Path: path}
		query := url.Values{"mode": []string{"rwc"}}
		uri.RawQuery = query.Encode()
		backup, err := provider.NewBackup(uri.String())
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, backup.Finish()) }()
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			more, err := backup.Step(256)
			if err != nil || !more {
				return err
			}
		}
	})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(target, os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func snapshotPostgres(ctx context.Context, db *sql.DB, dsn, target string) error {
	settings, err := pgx.ParseConfig(dsn)
	if err != nil {
		return errors.New("invalid PostgreSQL backup configuration")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var snapshot string
	if err := tx.QueryRowContext(ctx, "SELECT pg_export_snapshot()").Scan(&snapshot); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, "pg_dump", "--format=custom", "--no-password", "--snapshot="+snapshot, "--file="+target)
	environment := []string{}
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "PG") {
			environment = append(environment, value)
		}
	}
	environment = append(environment, "PGHOST="+settings.Host, fmt.Sprintf("PGPORT=%d", settings.Port), "PGUSER="+settings.User, "PGPASSWORD="+settings.Password, "PGDATABASE="+settings.Database)
	// 从 URL/关键字输入中保留 libpq TLS 设置，避免将包含密钥的
	// DSN 写入 argv。pg_dump 会执行自身的证书检查。
	if parsed, parseErr := url.Parse(dsn); parseErr == nil && (parsed.Scheme == "postgres" || parsed.Scheme == "postgresql") {
		for key, env := range map[string]string{"sslmode": "PGSSLMODE", "sslrootcert": "PGSSLROOTCERT", "sslcert": "PGSSLCERT", "sslkey": "PGSSLKEY"} {
			if value := parsed.Query().Get(key); value != "" {
				environment = append(environment, env+"="+value)
			}
		}
	} else if settings.TLSConfig != nil {
		return errors.New("PostgreSQL backup with TLS requires a URL DSN preserving explicit libpq TLS options")
	}
	command.Env = environment
	if err := command.Run(); err != nil {
		return errors.New("pg_dump failed; database backup is incomplete")
	}
	return tx.Commit()
}

func ReadManifest(path string) (*Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 64<<20))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("backup manifest contains trailing data")
	}
	if manifest.Version != 1 || manifest.ID == "" {
		return nil, errors.New("unsupported backup manifest")
	}
	if manifest.Status == "complete" {
		if err := validateComplete(&manifest); err != nil {
			return nil, err
		}
	}
	return &manifest, nil
}

func validateComplete(manifest *Manifest) error {
	if manifest.Version != 1 || manifest.ID == "" || manifest.Status != "complete" || len(manifest.Failures) != 0 {
		return errors.New("backup is not complete")
	}
	if manifest.Database.Name == "" || manifest.Deployment.Name == "" || len(manifest.RequiredKeyIDs) > 0 && manifest.Keyring.Name == "" {
		return errors.New("complete backup is missing required artifacts")
	}
	seen := map[string]bool{}
	for _, proof := range []Artifact{manifest.Database, manifest.Deployment, manifest.Keyring} {
		if proof.Name == "" && proof.Size == 0 && proof.SHA256 == "" {
			continue
		}
		if proof.Name == "" || proof.Name == "." || proof.Name == ".." || filepath.Base(proof.Name) != proof.Name || strings.ContainsAny(proof.Name, "/\\:") || proof.Size <= 0 || !validDigest(proof.SHA256) || seen[proof.Name] {
			return errors.New("invalid backup artifact proof")
		}
		seen[proof.Name] = true
	}
	return nil
}

// RestoreCheck 有意设计为只读。请针对以维护模式配置的
// 已恢复数据库调用它；成功并不会启动 worker 或物理 GC。
func RestoreCheck(ctx context.Context, client *ent.Client, resolve Resolver, directory string, manifest *Manifest) (bool, error) {
	if err := validateComplete(manifest); err != nil {
		return false, err
	}
	if !manifest.ExpiresAt.After(time.Now().UTC()) {
		return false, errors.New("object backup pins have expired; recreate and verify a protected backup")
	}
	for _, expected := range []Artifact{manifest.Database, manifest.Deployment, manifest.Keyring} {
		if expected.Name == "" {
			if expected.SHA256 != "" {
				return false, errors.New("invalid backup artifact")
			}
			continue
		}
		if filepath.Base(expected.Name) != expected.Name || strings.ContainsAny(expected.Name, "/\\:") {
			return false, errors.New("invalid backup artifact path")
		}
		actual, err := hashArtifact(filepath.Join(directory, expected.Name))
		if err != nil {
			return false, err
		}
		if actual.Size != expected.Size || actual.SHA256 != expected.SHA256 {
			return false, errors.New("backup artifact checksum mismatch")
		}
	}
	if err := CheckReferences(ctx, client, manifest); err != nil {
		return false, err
	}
	if len(manifest.RequiredKeyIDs) > 0 {
		keys, err := credential.LoadKeyring(filepath.Join(directory, manifest.Keyring.Name))
		if err != nil {
			return false, err
		}
		for _, id := range manifest.RequiredKeyIDs {
			if !keys.HasKey(id) {
				return false, errors.New("restored keyring is incomplete")
			}
		}
		if err := VerifyKeys(ctx, client, keys); err != nil {
			return false, err
		}
	}
	if err := VerifyObjects(ctx, manifest, resolve); err != nil {
		return false, err
	}
	for _, object := range manifest.Objects {
		if object.Status != "verified" {
			return false, nil
		}
	}
	return true, nil
}
