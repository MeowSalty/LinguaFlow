package v015

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/catalog"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/snapshot"
	storagemigrate "github.com/MeowSalty/LinguaFlow/backend/internal/migration/storage"
)

func restoreTargetFiles(ctx context.Context, p *operation.Plan, j *operation.Journal, backup *BackupReceipt) error {
	var deployment config.ServerConfig
	if err := operation.Read(filepath.Join(j.BackupDirectory, backup.Deployment.Name), &deployment); err != nil {
		return err
	}
	for _, root := range p.LocalRoots {
		if root.Identity != "" {
			identity, err := operation.Identity(root.Source)
			if err != nil || identity != root.Identity {
				return operation.Fail(operation.BaselineChanged, "source local storage directory identity changed")
			}
		}
		if root.Target == root.Source {
			continue
		}
		relative, err := filepath.Rel(p.Target.DataDir, root.Target)
		if err != nil || relative == ".." {
			return operation.Fail(operation.Incompatible, "local target root is outside the target directory")
		}
		target := filepath.Join(j.Staging, relative)
		if err := privateDirectories(j.Staging, target); err != nil {
			return err
		}
		for _, object := range backup.Objects {
			if object.Driver != "local" || object.Root != root.Source || object.Status != "copied" {
				continue
			}
			destination := filepath.Join(target, filepath.FromSlash(object.Key))
			if err := privateDirectories(j.Staging, filepath.Dir(destination)); err != nil {
				return err
			}
			if err := copyVerified(ctx, filepath.Join(j.BackupDirectory, filepath.FromSlash(object.Recovery)), destination, object.Size, object.SHA256); err != nil {
				return err
			}
		}
		// Restore the independent marker snapshot, never the live source bytes.
		if proof, ok := backup.Markers[root.BackendID]; ok {
			destination := filepath.Join(target, ".linguaflow", "space.json")
			if err := privateDirectories(j.Staging, filepath.Dir(destination)); err != nil {
				return err
			}
			if err := copyVerified(ctx, filepath.Join(j.BackupDirectory, proof.Name), destination, proof.Size, proof.SHA256); err != nil {
				return err
			}
		}
		for i := range deployment.Storage.Backends {
			if deployment.Storage.Backends[i].ID == root.BackendID {
				deployment.Storage.Backends[i].Root = root.Target
			}
		}
	}
	deployment.DataDir = p.Target.DataDir
	if p.Source.Driver == "postgres" {
		target, err := ResolveTarget(p, false)
		if err != nil {
			return err
		}
		deployment.Database = target.Database
	} else {
		deployment.Database.DSN = ""
	}
	deployment.AutoMigrate = false
	deployment.Storage.Maintenance = true
	deployment.Storage.WorkDir = filepath.Join(p.Target.DataDir, "work")
	deployment.Storage.CacheDir = filepath.Join(p.Target.DataDir, "cache")
	if backup.Keyring.Name != "" {
		destination := filepath.Join(j.Staging, "credentials-keyring.json")
		if err := copyVerified(ctx, filepath.Join(j.BackupDirectory, backup.Keyring.Name), destination, backup.Keyring.Size, backup.Keyring.SHA256); err != nil {
			return err
		}
		deployment.Credentials.KeyringFile = filepath.Join(p.Target.DataDir, "credentials-keyring.json")
	}
	if p.Source.Mode == "local" {
		if deployment.JWTSecret == "" {
			return operation.Fail(operation.BackupIncomplete, "source local instance secret is required")
		}
		path := filepath.Join(j.Staging, "instance-secret")
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			if _, err := credential.PublishPrivateFile(path, []byte(deployment.JWTSecret+"\n")); err != nil {
				return err
			}
		}
	}
	data, err := config.EncodeServerDocument(deployment, config.LogConfig{Level: "info", Format: "text"})
	if err != nil {
		return err
	}
	path := filepath.Join(j.Staging, "deployment.yaml")
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		if _, err := credential.PublishPrivateFile(path, data); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		actual, err := os.ReadFile(path)
		if err != nil || string(actual) != string(data) {
			return operation.Fail(operation.BaselineChanged, "target deployment inputs changed")
		}
	}
	return nil
}

func privateDirectories(root, path string) error {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return operation.Fail(operation.Incompatible, "target directory escapes owned staging")
	}
	if root == path {
		return operation.ValidatePath(root, true)
	}
	if _, err := os.Lstat(path); err == nil {
		return operation.ValidatePath(path, true)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := privateDirectories(root, filepath.Dir(path)); err != nil {
		return err
	}
	return credential.CreatePrivateDirectory(path)
}

func copyVerified(ctx context.Context, source, target string, size int64, digest string) error {
	if _, err := os.Lstat(target); err == nil {
		proof, err := operation.Hash(ctx, target)
		if err != nil || proof.Size != size || proof.SHA256 != digest {
			return operation.Fail(operation.BaselineChanged, "owned target file was replaced or changed")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	proof, err := operation.Copy(ctx, source, target)
	if err != nil {
		return err
	}
	if proof.Size != size || proof.SHA256 != digest {
		return operation.Fail(operation.BackupIncomplete, "recovery copy differs from verified backup")
	}
	return nil
}

func verifyTargetFiles(ctx context.Context, p *operation.Plan, j *operation.Journal, backup *BackupReceipt) error {
	for _, object := range backup.Objects {
		if object.Driver != "local" || object.Status != "copied" {
			continue
		}
		root := object.Root
		for _, mapping := range p.LocalRoots {
			if mapping.Source == root && mapping.Target != root {
				relative, err := filepath.Rel(p.Target.DataDir, mapping.Target)
				if err != nil {
					return err
				}
				root = filepath.Join(j.Staging, relative)
			}
		}
		proof, err := operation.Hash(ctx, filepath.Join(root, filepath.FromSlash(object.Key)))
		if err != nil || proof.Size != object.Size || proof.SHA256 != object.SHA256 {
			return operation.Fail(operation.NeedsAction, "target file bytes differ from the pre-conversion backup")
		}
	}
	return nil
}

func VerifyOperation(ctx context.Context, directory string) (*operation.Report, error) {
	p, j, err := operation.Load(directory)
	if err != nil {
		return nil, err
	}
	if p.Kind != "upgrade" || p.Build != catalog.Build() || p.TargetSignature != catalog.TargetSignature() {
		return nil, operation.Fail(operation.Incompatible, "use the exact upgrade build for this operation")
	}
	resolved, source, err := OpenPlannedSource(ctx, p)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	backup, err := LoadBackupReceipt(ctx, j.BackupDirectory, p)
	if err != nil {
		return nil, err
	}
	if err := VerifyExternalBackup(ctx, source, resolved.Config, resolved.CredentialKeys, p, backup.Objects); err != nil {
		return nil, err
	}
	var cfg config.ServerConfig
	if p.Source.Driver == "sqlite" {
		path := j.Staging
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			path = p.Target.DataDir
		}
		identity, err := operation.Identity(path)
		if err != nil || identity != j.StagingIdentity {
			return nil, operation.Fail(operation.BaselineChanged, "target directory identity changed")
		}
		cfg = config.ServerConfig{DataDir: path, Database: config.DatabaseConfig{Driver: "sqlite"}}
		j.Staging = path
	} else {
		cfg, err = ResolveTarget(p, false)
		if err != nil {
			return nil, err
		}
	}
	db, client, err := snapshot.Open(ctx, &cfg, true)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if _, state, err := operation.Steps(ctx, db, p); err != nil || state != "ready" {
		return nil, operation.Fail(operation.NeedsAction, "target is not verified ready")
	}
	probe, err := Probe(ctx, db, p.Source.Driver)
	if err != nil {
		return nil, err
	}
	if j.TargetFingerprint != "" && probe.Fingerprint != j.TargetFingerprint {
		return nil, operation.Fail(operation.BaselineChanged, "target changed after the recorded migration verification")
	}
	counts, err := VerifyPreserved(ctx, source, db, p)
	if err != nil {
		return nil, err
	}
	if err := VerifyAccounts(ctx, client, p, resolved.CredentialKeys, true); err != nil {
		return nil, err
	}
	if err := verifyStorage(ctx, db, client, resolved.Config, p); err != nil {
		return nil, err
	}
	if err := VerifySourceKeys(ctx, db, resolved.CredentialKeys); err != nil {
		return nil, err
	}
	if err := verifyTargetFiles(ctx, p, j, backup); err != nil {
		return nil, err
	}
	return &operation.Report{Version: 1, ID: p.ID, Phase: j.Phase, Verified: true, SourceFingerprint: p.SourceFingerprint, Counts: counts, BackupTime: backup.CreatedAt, Next: []string{"Switch deployment inputs explicitly, then verify the original administrator login and business views before enabling writers."}}, nil
}

func RollbackOperation(ctx context.Context, directory string, offline bool) (*operation.Report, error) {
	if !offline {
		return nil, operation.Fail(operation.NeedsAction, "version rollback requires --offline and all writers stopped")
	}
	p, j, err := operation.Load(directory)
	if err != nil {
		return nil, err
	}
	if p.Kind != "upgrade" || p.Build != catalog.Build() {
		return nil, operation.Fail(operation.Incompatible, "use the exact upgrade operation build")
	}
	unlock, err := operation.Lock(directory)
	if err != nil {
		return nil, err
	}
	defer unlock()
	resolved, source, err := OpenPlannedSource(ctx, p)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	backup, err := LoadBackupReceipt(ctx, j.BackupDirectory, p)
	if err != nil {
		return nil, err
	}
	if err := VerifyExternalBackup(ctx, source, resolved.Config, resolved.CredentialKeys, p, backup.Objects); err != nil {
		return nil, err
	}
	var cfg config.ServerConfig
	if p.Source.Driver == "sqlite" {
		stage := j.Staging
		if _, err := os.Lstat(stage); errors.Is(err, os.ErrNotExist) {
			stage = p.Target.DataDir
		}
		if stage != "" {
			identity, err := operation.Identity(stage)
			if err != nil || identity != j.StagingIdentity {
				return nil, operation.Fail(operation.BaselineChanged, "rollback target identity changed")
			}
			cfg = config.ServerConfig{DataDir: stage, Database: config.DatabaseConfig{Driver: "sqlite"}}
		}
	} else {
		cfg, err = ResolveTarget(p, false)
		if err != nil {
			return nil, err
		}
	}
	if cfg.DataDir != "" {
		db, _, err := snapshot.Open(ctx, &cfg, false)
		if err != nil {
			return nil, err
		}
		if j.Phase != "completed" && j.Phase != "rolled_back" {
			err = operation.Abandon(ctx, db, p.Source.Driver, p)
		}
		db.Close()
		if err != nil {
			return nil, err
		}
	}
	j.Phase = "rolled_back"
	if err := operation.SaveJournal(directory, j); err != nil {
		return nil, err
	}
	r := &operation.Report{Version: 1, ID: p.ID, Phase: "rolled_back", SourceFingerprint: p.SourceFingerprint, BackupTime: backup.CreatedAt, Next: []string{"All source, target, database snapshot, keyring and object recovery materials are retained.", "Select the original data directory or original PostgreSQL DSN with the source release while all writers remain stopped.", "Restoration returns to the recorded backup time; writes made in the new deployment are not copied back into the old schema."}}
	return r, saveReport(directory, r)
}

func rehearse(ctx context.Context, options RunOptions, p *operation.Plan) (*operation.Report, error) {
	directory := filepath.Join(options.Directory, "rehearsal")
	if _, err := os.Lstat(directory); err == nil {
		return Run(ctx, RunOptions{Directory: directory, Offline: true, Fault: options.Fault})
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	rehearsal := *p
	id, err := operation.NewID()
	if err != nil {
		return nil, err
	}
	rehearsal.ID = id
	if p.Source.Driver == "sqlite" {
		rehearsal.Target.DataDir = filepath.Join(options.Directory, "rehearsal-target")
		rehearsal.Target.ParentIdentity, err = operation.Identity(options.Directory)
		if err != nil {
			return nil, err
		}
		rehearsal.LocalRoots = append([]operation.LocalRoot(nil), p.LocalRoots...)
		for i := range rehearsal.LocalRoots {
			r := &rehearsal.LocalRoots[i]
			if r.Target != r.Source {
				relative, err := filepath.Rel(p.Target.DataDir, r.Target)
				if err != nil {
					return nil, err
				}
				r.Target = filepath.Join(rehearsal.Target.DataDir, relative)
			}
		}
	} else {
		cfg, err := ResolveTarget(p, true)
		if err != nil {
			return nil, err
		}
		db, _, err := snapshot.Open(ctx, &cfg, false)
		if err != nil {
			return nil, err
		}
		if err := snapshot.EmptyPostgres(ctx, db); err != nil {
			db.Close()
			return nil, err
		}
		identity, err := snapshot.PostgresIdentity(ctx, db)
		db.Close()
		if err != nil {
			return nil, err
		}
		if identity == p.Source.Identity || identity == p.Target.Identity {
			return nil, operation.Fail(operation.UnsupportedTarget, "rehearsal must use another independent PostgreSQL database")
		}
		if p.Target.RehearsalIdentity == "" || identity != p.Target.RehearsalIdentity {
			return nil, operation.Fail(operation.BaselineChanged, "rehearsal database identity changed")
		}
		rehearsal.Target.ConfigPath = p.Target.RehearsalConfigPath
		rehearsal.Target.Identity = identity
		rehearsal.Target.DataDir = p.Target.RehearsalDataDir
		rehearsal.Target.InputDigest = p.Target.RehearsalInputDigest
		rehearsal.Target.ParentIdentity, err = operation.Identity(filepath.Dir(cfg.DataDir))
		if err != nil {
			return nil, err
		}
		rehearsal.LocalRoots = append([]operation.LocalRoot(nil), p.LocalRoots...)
		for i := range rehearsal.LocalRoots {
			r := &rehearsal.LocalRoots[i]
			if r.Target != r.Source {
				rel, err := filepath.Rel(p.Target.DataDir, r.Target)
				if err != nil {
					return nil, err
				}
				r.Target = filepath.Join(cfg.DataDir, rel)
			}
		}
	}
	var manifest *storagemigrate.Manifest
	if p.StorageManifest != "" {
		manifest = new(storagemigrate.Manifest)
		if err := operation.Read(p.StorageManifest, manifest); err != nil {
			return nil, err
		}
		if operation.Digest(manifest) != p.StorageDigest {
			return nil, operation.Fail(operation.Incompatible, "original storage subplan changed")
		}
		manifest.OperationID = id
		if p.Target.DataDir != "" {
			relative, err := filepath.Rel(p.Target.DataDir, manifest.DefaultRoot)
			if err == nil && !strings.HasPrefix(relative, "..") {
				manifest.DefaultRoot = filepath.Join(rehearsal.Target.DataDir, relative)
			}
		}
		rehearsal.StorageManifest = filepath.Join(directory, "storage-plan.json")
		rehearsal.StorageDigest = operation.Digest(manifest)
	}
	if _, err := operation.Create(directory, &rehearsal); err != nil {
		return nil, err
	}
	if manifest != nil {
		if err := operation.Write(rehearsal.StorageManifest, manifest, true); err != nil {
			return nil, err
		}
	}
	return Run(ctx, RunOptions{Directory: directory, Offline: true, Fault: options.Fault})
}
