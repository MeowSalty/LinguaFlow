package v015

import (
	"context"
	"errors"
	"fmt"
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
	backup, err := loadMaterials(ctx, directory, p, j)
	if err != nil {
		return nil, err
	}
	resolved, source, err := OpenPlannedSource(ctx, p)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	if err := verifyRouteProtection(ctx, source, resolved, p, backup); err != nil {
		return nil, err
	}
	cfg, err := executionConfig(p, j, resolved.Config)
	if err != nil {
		return nil, err
	}
	db, client, err := snapshot.Open(ctx, &cfg, true)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if _, state, err := operation.Steps(ctx, db, p); err != nil || state != "ready" {
		return nil, operation.Fail(operation.NeedsAction, "target is not verified ready")
	}
	result, err := operation.ResultDigest(ctx, db, p)
	if err != nil {
		return nil, err
	}
	actual, err := BusinessFingerprint(ctx, db, p.Source.Driver)
	if err != nil || actual != result || (j.TargetFingerprint != "" && result != j.TargetFingerprint) {
		return nil, operation.Fail(operation.BaselineChanged, "target changed after the recorded migration verification")
	}
	counts, err := VerifyBaseline(ctx, db, p, true)
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
	if err := VerifyShape(ctx, db, p); err != nil {
		return nil, err
	}
	if err := verifyExecutionFiles(ctx, p, j, backup); err != nil {
		return nil, err
	}
	if j.Phase != "completed" {
		j.Phase = "ready"
	}
	r := upgradeReport(p, j, backup, counts)
	r.Rehearsal = rehearsalStatus(directory, p)
	return r, nil
}

func executionConfig(p *operation.Plan, j *operation.Journal, source config.ServerConfig) (config.ServerConfig, error) {
	if p.Strategy == operation.Inplace {
		return source, nil
	}
	if p.Source.Driver == "postgres" {
		return ResolveTarget(p, false)
	}
	paths := []string{p.Target.DataDir, filepath.Join(filepath.Dir(p.Target.DataDir), ".linguaflow-stage-"+p.ID)}
	var selected string
	for _, path := range paths {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return source, err
		}
		var owner struct{ ID, Digest string }
		if err := operation.Read(filepath.Join(path, "operation.json"), &owner); err != nil || owner.ID != p.ID || owner.Digest != operation.Digest(p) {
			return source, operation.Fail(operation.Incompatible, "execution directory does not belong to this plan")
		}
		if selected != "" {
			return source, operation.Fail(operation.Incompatible, "both staging and published directories are occupied")
		}
		selected = path
	}
	if selected == "" {
		return source, operation.Fail(operation.NeedsAction, "conversion directory is unavailable")
	}
	id, err := operation.Identity(selected)
	if err != nil || (j.StagingIdentity != "" && id != j.StagingIdentity) {
		return source, operation.Fail(operation.BaselineChanged, "conversion directory identity changed")
	}
	j.Staging, j.StagingIdentity = selected, id
	return config.ServerConfig{DataDir: selected, Database: config.DatabaseConfig{Driver: "sqlite"}, Mode: modeConfig(p.Source.Mode)}, nil
}

func RollbackOperation(ctx context.Context, directory string, offline bool) (*operation.Report, error) {
	return RollbackOperationWithOptions(ctx, directory, RunOptions{Offline: offline})
}

func RollbackOperationWithOptions(ctx context.Context, directory string, options RunOptions) (*operation.Report, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	options.Fault = operation.ContextCheckpoint(ctx, options.Fault)
	if !options.Offline {
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
	if p.Strategy == operation.Inplace {
		return rollbackInplace(ctx, directory, p, j, options)
	}
	backup, err := loadMaterials(ctx, directory, p, j)
	if err != nil {
		return nil, err
	}
	resolved, source, err := OpenPlannedSource(ctx, p)
	if err != nil {
		return nil, err
	}
	defer source.Close()
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
	r := upgradeReport(p, j, backup, nil)
	r.Restoration = "source_retained"
	r.Next = []string{"All source, target, database snapshot, keyring and object recovery materials are retained.", "Select the original data directory or original PostgreSQL DSN with the source release while all writers remain stopped."}
	return r, saveReport(directory, r)
}

func rehearse(ctx context.Context, options RunOptions, p *operation.Plan) (*operation.Report, error) {
	directory := filepath.Join(options.Directory, "rehearsal")
	if _, err := os.Lstat(directory); err == nil {
		r, err := Run(ctx, RunOptions{Directory: directory, Offline: true, Fault: options.Fault})
		if err != nil {
			return nil, err
		}
		return recordRehearsal(options.Directory, p, r)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	rehearsal := *p
	rehearsal.Baseline = nil
	rehearsal.Strategy, rehearsal.DatabaseBackup = operation.Clone, operation.BackupAuto
	id, err := operation.NewID()
	if err != nil {
		return nil, err
	}
	rehearsal.ID = id
	if p.Source.Driver == "postgres" && p.Target.RehearsalRestored {
		return rehearseRestoredPostgres(ctx, options, p, &rehearsal)
	}
	if p.Source.Driver == "sqlite" {
		rehearsal.Target.DataDir = filepath.Join(options.Directory, "rehearsal-target")
		rehearsal.Target.ParentIdentity, err = operation.Identity(options.Directory)
		if err != nil {
			return nil, err
		}
		rehearsal.LocalRoots = append([]operation.LocalRoot(nil), p.LocalRoots...)
		for i := range rehearsal.LocalRoots {
			r := &rehearsal.LocalRoots[i]
			r.Target = mapRehearsalRoot(p, *r, rehearsal.Target.DataDir, i)
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
			r.Target = mapRehearsalRoot(p, *r, cfg.DataDir, i)
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
		if p.Strategy == operation.Inplace {
			for _, root := range rehearsal.LocalRoots {
				if root.Source == manifest.DefaultRoot {
					manifest.DefaultRoot = root.Target
				}
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
	r, err := Run(ctx, RunOptions{Directory: directory, Offline: true, Fault: options.Fault})
	if err != nil {
		return nil, err
	}
	return recordRehearsal(options.Directory, p, r)
}

func mapRehearsalRoot(p *operation.Plan, root operation.LocalRoot, target string, index int) string {
	base, path := p.Target.DataDir, root.Target
	if root.Target == root.Source || base == "" {
		base, path = p.Source.DataDir, root.Source
	}
	rel, err := filepath.Rel(base, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		rel = filepath.Join("storage-roots", fmt.Sprintf("%06d", index))
	}
	return filepath.Join(target, rel)
}

type rehearsalEvidence struct {
	Version    int               `json:"version"`
	ID         string            `json:"operation_id"`
	PlanDigest string            `json:"plan_digest"`
	Status     string            `json:"status"`
	Report     *operation.Report `json:"report"`
}

func recordRehearsal(directory string, p *operation.Plan, r *operation.Report) (*operation.Report, error) {
	if !r.Verified || r.Phase != "completed" {
		return nil, operation.Fail(operation.NeedsAction, "independent rehearsal has not completed verification")
	}
	r.Rehearsal = "verified"
	e := &rehearsalEvidence{Version: operation.Version, ID: p.ID, PlanDigest: operation.Digest(p), Status: "verified", Report: r}
	path := filepath.Join(directory, "rehearsal-result.json")
	_, err := os.Lstat(path)
	if err := operation.Write(path, e, errors.Is(err, os.ErrNotExist)); err != nil {
		return nil, err
	}
	return r, nil
}

func rehearsalStatus(directory string, p *operation.Plan) string {
	var e rehearsalEvidence
	if err := operation.Read(filepath.Join(directory, "rehearsal-result.json"), &e); err != nil || e.Version != operation.Version || e.ID != p.ID || e.PlanDigest != operation.Digest(p) || e.Status != "verified" || e.Report == nil || !e.Report.Verified {
		return "not_run"
	}
	return "verified"
}

// A manually restored PostgreSQL copy is itself the independent rehearsal
// source. Its private operation uses an external declaration and never dumps or
// restores the original PostgreSQL database.
func rehearseRestoredPostgres(ctx context.Context, options RunOptions, p, rehearsal *operation.Plan) (*operation.Report, error) {
	cfg, err := ResolveTarget(p, true)
	if err != nil {
		return nil, err
	}
	db, _, err := snapshot.Open(ctx, &cfg, true)
	if err != nil {
		return nil, err
	}
	probe, probeErr := Probe(ctx, db, "postgres")
	id, idErr := snapshot.PostgresIdentity(ctx, db)
	closeErr := db.Close()
	if errors.Join(probeErr, idErr, closeErr) != nil || probe.Fingerprint != p.SourceFingerprint || probe.SchemaDigest != p.SourceSchema || probe.Format != "v015" || id != p.Target.RehearsalIdentity {
		return nil, operation.Fail(operation.BaselineChanged, "manually restored rehearsal no longer matches the original baseline")
	}
	resolved, source, err := OpenPlannedSource(ctx, p)
	if err != nil {
		return nil, err
	}
	source.Close()
	// Resolve implicit local roots before selecting the rehearsal data directory.
	// The independent database continues to reference the protected original files.
	if len(resolved.Config.Storage.Backends) == 0 {
		for _, root := range p.LocalRoots {
			resolved.Config.Storage.Backends = append(resolved.Config.Storage.Backends, config.StorageBackendConfig{ID: root.BackendID, Driver: "local", Root: root.Source})
		}
	}
	resolved.Config.DataDir, resolved.Config.Database = cfg.DataDir, cfg.Database
	rehearsal.Source = operation.Endpoint{ConfigPath: filepath.Join(options.Directory, "rehearsal-source.yaml"), DataDir: cfg.DataDir, Driver: "postgres", Mode: p.Source.Mode, Identity: id, InputDigest: InputDigest(resolved)}
	rehearsal.Strategy, rehearsal.DatabaseBackup = operation.Inplace, operation.BackupExternal
	rehearsal.Target = operation.Target{}
	rehearsal.LocalRoots = append([]operation.LocalRoot(nil), p.LocalRoots...)
	for i := range rehearsal.LocalRoots {
		rehearsal.LocalRoots[i].Target = rehearsal.LocalRoots[i].Source
	}
	data, err := config.EncodeServerDocument(resolved.Config, config.LogConfig{Level: "info", Format: "text"})
	if err != nil {
		return nil, err
	}
	if _, err := credential.PublishPrivateFile(rehearsal.Source.ConfigPath, data); err != nil {
		return nil, err
	}
	directory := filepath.Join(options.Directory, "rehearsal")
	var manifest *storagemigrate.Manifest
	if p.StorageManifest != "" {
		manifest, err = storagemigrate.ReadManifest(p.StorageManifest)
		if err != nil || operation.Digest(manifest) != p.StorageDigest {
			return nil, operation.Fail(operation.Incompatible, "rehearsal storage subplan changed")
		}
		manifest.OperationID = rehearsal.ID
		rehearsal.StorageManifest, rehearsal.StorageDigest = filepath.Join(directory, "storage-plan.json"), operation.Digest(manifest)
	}
	if _, err := operation.Create(directory, rehearsal); err != nil {
		return nil, err
	}
	if manifest != nil {
		if err := operation.Write(rehearsal.StorageManifest, manifest, true); err != nil {
			return nil, err
		}
	}
	r, err := Run(ctx, RunOptions{Directory: directory, Offline: true, BackupReference: "manual-rehearsal:" + p.SourceFingerprint, Fault: options.Fault})
	if err != nil {
		return nil, err
	}
	return recordRehearsal(options.Directory, p, r)
}
