package v015

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/catalog"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/snapshot"
	storagemigrate "github.com/MeowSalty/LinguaFlow/backend/internal/migration/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

type RunOptions struct {
	Directory string
	Offline   bool
	Rehearse  bool
	// Fault is used by acceptance tests at durable phase boundaries.
	Fault func(string) error
}

func Run(ctx context.Context, options RunOptions) (report *operation.Report, err error) {
	if !options.Offline {
		return nil, operation.Fail(operation.NeedsAction, "declare --offline after stopping all service writers, workers and GC")
	}
	p, j, err := operation.Load(options.Directory)
	if err != nil {
		return nil, err
	}
	if p.Kind != "upgrade" {
		return nil, operation.Fail(operation.Incompatible, "operation kind is not upgrade")
	}
	if p.Build != catalog.Build() || p.TargetSignature != catalog.TargetSignature() {
		return nil, operation.Fail(operation.Incompatible, "use the exact tool build and target schema bound by this plan")
	}
	unlock, err := operation.Lock(options.Directory)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	if options.Rehearse {
		return rehearse(ctx, options, p)
	}
	if j.Phase == "abandoned" || j.Phase == "rolled_back" {
		return nil, operation.Fail(operation.Incompatible, "abandoned operation cannot resume; retained material is for restoration")
	}
	resolved, sourceDB, err := OpenPlannedSource(ctx, p)
	if err != nil {
		return nil, err
	}
	defer sourceDB.Close()
	defer func() {
		if err != nil {
			j.Phase = "needs_action"
			var classified *operation.Error
			if errors.As(err, &classified) {
				j.ErrorCode = classified.Code
			} else {
				j.ErrorCode = operation.NeedsAction
			}
			_ = operation.SaveJournal(options.Directory, j)
		}
	}()
	if j.Phase == "completed" {
		return VerifyOperation(ctx, options.Directory)
	}
	backup, err := ensureBackup(ctx, sourceDB, resolved, p, j, options)
	if err != nil {
		return nil, err
	}
	if err := VerifyExternalBackup(ctx, sourceDB, resolved.Config, resolved.CredentialKeys, p, backup.Objects); err != nil {
		return nil, err
	}
	if err := fault(options, "backup"); err != nil {
		return nil, err
	}
	if p.Source.Driver == "postgres" {
		cfg, e := ResolveTarget(p, false)
		if e != nil {
			return nil, e
		}
		leaseDB, _, e := snapshot.Open(ctx, &cfg, false)
		if e != nil {
			return nil, e
		}
		defer leaseDB.Close()
		unlockDB, e := database.AcquireMigrationLock(ctx, leaseDB, p.Source.Driver)
		if e != nil {
			return nil, e
		}
		defer func() { err = errors.Join(err, unlockDB()) }()
	}
	targetCfg, err := prepareTarget(ctx, p, j, backup, options)
	if err != nil {
		return nil, err
	}
	targetDB, targetClient, err := snapshot.Open(ctx, &targetCfg, false)
	if err != nil {
		return nil, err
	}
	closed := false
	defer func() {
		if !closed {
			_ = targetDB.Close()
		}
	}()
	if err := verifyTargetReservation(ctx, targetDB, p); err != nil {
		return nil, err
	}
	if err := operation.Initialize(ctx, targetDB, p.Source.Driver, p); err != nil {
		return nil, err
	}
	steps, state, err := operation.Steps(ctx, targetDB, p)
	if err != nil {
		return nil, err
	}
	if state == "abandoned" {
		return nil, operation.Fail(operation.Incompatible, "target was abandoned")
	}
	j.Steps = steps
	if err := operation.SaveJournal(options.Directory, j); err != nil {
		return nil, err
	}
	for _, step := range []struct {
		name string
		run  func(context.Context, *sql.DB, *operation.Plan) error
	}{{"schema", ConvertSchema}, {"accounts", ConvertAccounts}, {"history", ConvertHistory}} {
		if contains(j.Steps, step.name) {
			continue
		}
		j.Phase = "converting"
		if err := operation.SaveJournal(options.Directory, j); err != nil {
			return nil, err
		}
		if err := fault(options, "before_"+step.name); err != nil {
			return nil, err
		}
		if err := step.run(ctx, targetDB, p); err != nil {
			return nil, err
		}
		if err := fault(options, step.name+"_commit"); err != nil {
			return nil, err
		}
		j.Steps = append(j.Steps, step.name)
		if err := operation.SaveJournal(options.Directory, j); err != nil {
			return nil, err
		}
	}
	if !contains(j.Steps, "storage") {
		if err := applyStorage(ctx, targetDB, targetClient, resolved.Config, p, options); err != nil {
			return nil, err
		}
		if err := operation.Step(ctx, targetDB, p.Source.Driver, p, "storage", func(c *ent.Client) error {
			return reconcileClaims(ctx, c, resolved.Config, filepath.Join(options.Directory, "work"))
		}); err != nil {
			return nil, err
		}
		if err := fault(options, "storage_commit"); err != nil {
			return nil, err
		}
		j.Steps = append(j.Steps, "storage")
		if err := operation.SaveJournal(options.Directory, j); err != nil {
			return nil, err
		}
	}
	j.Phase = "verifying"
	if err := operation.SaveJournal(options.Directory, j); err != nil {
		return nil, err
	}
	counts, err := VerifyPreserved(ctx, sourceDB, targetDB, p)
	if err != nil {
		return nil, err
	}
	if err := VerifyShape(ctx, targetDB, p); err != nil {
		return nil, err
	}
	if err := verifyStorage(ctx, targetDB, targetClient, resolved.Config, p); err != nil {
		return nil, err
	}
	if err := VerifySourceKeys(ctx, targetDB, resolved.CredentialKeys); err != nil {
		return nil, err
	}
	if err := verifyTargetFiles(ctx, p, j, backup); err != nil {
		return nil, err
	}
	if !contains(j.Steps, "ready") {
		if err := fault(options, "before_ready"); err != nil {
			return nil, err
		}
		if err := Complete(ctx, targetDB, p, func(c *ent.Client) error { return VerifyAccounts(ctx, c, p, resolved.CredentialKeys, true) }); err != nil {
			return nil, err
		}
		if err := fault(options, "ready_commit"); err != nil {
			return nil, err
		}
		j.Steps = append(j.Steps, "ready")
	}
	counts, err = VerifyPreserved(ctx, sourceDB, targetDB, p)
	if err != nil {
		return nil, err
	}
	current, err := Probe(ctx, targetDB, p.Source.Driver)
	if err != nil || current.Format != "current" {
		return nil, operation.Fail(operation.NeedsAction, "final target format verification failed")
	}
	j.TargetFingerprint = current.Fingerprint
	j.Phase = "ready"
	if err := operation.SaveJournal(options.Directory, j); err != nil {
		return nil, err
	}
	report = &operation.Report{Version: 1, ID: p.ID, Phase: "ready", Verified: true, SourceFingerprint: p.SourceFingerprint, Counts: counts, BackupTime: backup.CreatedAt, Next: []string{"Conversion verified. Select the generated maintenance deployment inputs and switch data directory or DSN explicitly.", "Use the original administrator password and verify projects, resources, task snapshots and credentials before enabling writers and GC."}}
	if err := fault(options, "before_report"); err != nil {
		return nil, err
	}
	if err := saveReport(options.Directory, report); err != nil {
		return nil, err
	}
	if err := targetDB.Close(); err != nil {
		return nil, err
	}
	closed = true
	j.Phase = "publishing"
	if err := operation.SaveJournal(options.Directory, j); err != nil {
		return nil, err
	}
	if err := fault(options, "before_publish"); err != nil {
		return nil, err
	}
	if p.Source.Driver == "sqlite" && j.Staging != p.Target.DataDir {
		if err := operation.PublishDirectory(j.Staging, p.Target.DataDir, j.StagingIdentity, p.Target.ParentIdentity); err != nil {
			return nil, err
		}
	}
	if err := fault(options, "publish"); err != nil {
		return nil, err
	}
	j.Phase = "completed"
	if err := operation.SaveJournal(options.Directory, j); err != nil {
		return nil, err
	}
	report.Phase = "completed"
	if err := saveReport(options.Directory, report); err != nil {
		return nil, err
	}
	return report, nil
}

func verifyTargetReservation(ctx context.Context, db *sql.DB, p *operation.Plan) error {
	query := "SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='linguaflow_migration_state'"
	if p.Source.Driver == "postgres" {
		query = "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='linguaflow_migration_state'"
	}
	var present int
	if err := db.QueryRowContext(ctx, query).Scan(&present); err != nil {
		return err
	}
	if present > 0 {
		_, state, err := operation.Steps(ctx, db, p)
		if err != nil {
			return err
		}
		if state == "abandoned" {
			return operation.Fail(operation.Incompatible, "target was abandoned")
		}
		return nil
	}
	clone, err := Probe(ctx, db, p.Source.Driver)
	if err != nil || clone.Format != "v015" || clone.Fingerprint != p.SourceFingerprint || clone.SchemaDigest != p.SourceSchema {
		return operation.Fail(operation.BaselineChanged, "unreserved target is not the exact independent source snapshot")
	}
	return nil
}

func fault(options RunOptions, phase string) error {
	if options.Fault != nil {
		return options.Fault(phase)
	}
	return nil
}
func contains(values []string, needle string) bool {
	for _, v := range values {
		if v == needle {
			return true
		}
	}
	return false
}
func saveReport(directory string, r *operation.Report) error {
	path := filepath.Join(directory, "report.json")
	_, err := os.Lstat(path)
	return operation.Write(path, r, errors.Is(err, os.ErrNotExist))
}

func OpenPlannedSource(ctx context.Context, p *operation.Plan) (*config.ResolvedServer, *sql.DB, error) {
	inputs := PlannedInputs(p)
	narrow, err := config.ResolveMigrationDatabase(inputs)
	if err != nil {
		return nil, nil, err
	}
	cfg := config.ServerConfig{DataDir: narrow.DataDir, Database: narrow.Database}
	db, _, err := snapshot.Open(ctx, &cfg, true)
	if err != nil {
		return nil, nil, err
	}
	ok := false
	defer func() {
		if !ok {
			db.Close()
		}
	}()
	probe, err := Probe(ctx, db, p.Source.Driver)
	if err != nil {
		return nil, nil, err
	}
	if probe.Fingerprint != p.SourceFingerprint || probe.SchemaDigest != p.SourceSchema || probe.Mode != p.Source.Mode {
		return nil, nil, operation.Fail(operation.BaselineChanged, "source baseline changed; retain existing materials and create a new plan")
	}
	if p.Source.Driver == "sqlite" && narrow.DatabaseDSN() != p.Source.DatabaseDSN {
		return nil, nil, operation.Fail(operation.BaselineChanged, "source database input changed")
	}
	resolved, err := ResolveProbedInputs(inputs, narrow)
	if err != nil {
		return nil, nil, err
	}
	if err := compareNarrow(narrow, resolved); err != nil {
		return nil, nil, err
	}
	identity, err := EndpointIdentity(ctx, db, resolved.Config)
	if err != nil {
		return nil, nil, err
	}
	if identity != p.Source.Identity || InputDigest(resolved) != p.Source.InputDigest {
		return nil, nil, operation.Fail(operation.BaselineChanged, "source path identity, deployment inputs or encryption keys changed")
	}
	ok = true
	return resolved, db, nil
}

func Environment() map[string]string {
	out := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			out[key] = value
		}
	}
	return out
}

func ensureBackup(ctx context.Context, db *sql.DB, resolved *config.ResolvedServer, p *operation.Plan, j *operation.Journal, options RunOptions) (*BackupReceipt, error) {
	if j.BackupDirectory != "" {
		if r, err := LoadBackupReceipt(ctx, j.BackupDirectory, p); err == nil {
			return r, nil
		}
		if _, err := os.Stat(filepath.Join(j.BackupDirectory, "receipt.json")); err == nil {
			return nil, operation.Fail(operation.BackupIncomplete, "existing backup receipt failed verification")
		}
	}
	directory := filepath.Join(options.Directory, "backup")
	if _, err := os.Lstat(directory); !errors.Is(err, os.ErrNotExist) {
		id, err := operation.NewID()
		if err != nil {
			return nil, err
		}
		directory = filepath.Join(options.Directory, "backup-"+id)
	}
	j.BackupDirectory = directory
	j.Phase = "backing_up"
	if err := operation.SaveJournal(options.Directory, j); err != nil {
		return nil, err
	}
	return CaptureSourceBackup(ctx, db, resolved, p, directory)
}

func prepareTarget(ctx context.Context, p *operation.Plan, j *operation.Journal, backup *BackupReceipt, options RunOptions) (config.ServerConfig, error) {
	if p.Source.Driver == "postgres" {
		cfg, err := ResolveTarget(p, false)
		if err != nil {
			return cfg, err
		}
		db, _, err := snapshot.Open(ctx, &cfg, false)
		if err != nil {
			return cfg, err
		}
		identity, err := snapshot.PostgresIdentity(ctx, db)
		if err != nil || identity != p.Target.Identity {
			db.Close()
			return cfg, operation.Fail(operation.BaselineChanged, "target PostgreSQL database identity changed")
		}
		var present int
		err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='linguaflow_migration_state'").Scan(&present)
		if err != nil {
			db.Close()
			return cfg, err
		}
		if present == 0 {
			if emptyErr := snapshot.EmptyPostgres(ctx, db); emptyErr != nil {
				// Restore is atomic, but a process can stop after pg_restore
				// commits and before the reservation is recorded. Only the
				// already bound staging intent and exact source clone may resume.
				clone, probeErr := Probe(ctx, db, "postgres")
				db.Close()
				if j.RestoreIdentity != p.Target.Identity || probeErr != nil || clone.Format != "v015" || clone.Fingerprint != p.SourceFingerprint || clone.SchemaDigest != p.SourceSchema {
					return cfg, emptyErr
				}
				return cfg, preparePostgresFiles(ctx, p, j, backup, options.Directory)
			}
			db.Close()
			j.Phase = "staging"
			j.RestoreIdentity = p.Target.Identity
			if err := operation.SaveJournal(options.Directory, j); err != nil {
				return cfg, err
			}
			if err := snapshot.RestorePostgres(ctx, &cfg, filepath.Join(j.BackupDirectory, backup.Database.Name)); err != nil {
				return cfg, err
			}
			if err := fault(options, "restore_commit"); err != nil {
				return cfg, err
			}
		} else {
			db.Close()
		}
		return cfg, preparePostgresFiles(ctx, p, j, backup, options.Directory)
	}
	if j.Staging != "" {
		stage := j.Staging
		if _, err := os.Lstat(stage); errors.Is(err, os.ErrNotExist) {
			stage = p.Target.DataDir
		}
		if err := operation.ValidatePath(stage, true); err != nil {
			return config.ServerConfig{}, err
		}
		identity, err := operation.Identity(stage)
		if err != nil || identity != j.StagingIdentity {
			return config.ServerConfig{}, operation.Fail(operation.BaselineChanged, "staging or published target directory identity changed")
		}
		if stage == p.Target.DataDir {
			j.Staging = stage
		}
		if stage != p.Target.DataDir {
			if err := prepareSQLiteFiles(ctx, p, j, backup); err != nil {
				return config.ServerConfig{}, err
			}
		}
		return config.ServerConfig{DataDir: stage, Database: config.DatabaseConfig{Driver: "sqlite"}, Mode: modeConfig(p.Source.Mode)}, nil
	}
	if _, err := os.Lstat(p.Target.DataDir); !errors.Is(err, os.ErrNotExist) {
		return config.ServerConfig{}, operation.Fail(operation.TargetNotEmpty, "target path became occupied before staging")
	}
	parent, err := operation.Identity(filepath.Dir(p.Target.DataDir))
	if err != nil || parent != p.Target.ParentIdentity {
		return config.ServerConfig{}, operation.Fail(operation.BaselineChanged, "target parent identity changed")
	}
	stage := filepath.Join(filepath.Dir(p.Target.DataDir), ".linguaflow-stage-"+p.ID)
	if _, err := os.Lstat(stage); err == nil {
		var owner struct{ ID, Digest string }
		if err := operation.Read(filepath.Join(stage, "operation.json"), &owner); err != nil || owner.ID != p.ID || owner.Digest != operation.Digest(p) {
			return config.ServerConfig{}, operation.Fail(operation.TargetNotEmpty, "unowned staging path is occupied")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return config.ServerConfig{}, err
	} else {
		if err := credential.CreatePrivateDirectory(stage); err != nil {
			return config.ServerConfig{}, err
		}
		if err := operation.Write(filepath.Join(stage, "operation.json"), struct{ ID, Digest string }{p.ID, operation.Digest(p)}, true); err != nil {
			return config.ServerConfig{}, err
		}
	}
	j.Staging = stage
	j.StagingIdentity, err = operation.Identity(stage)
	if err != nil {
		return config.ServerConfig{}, err
	}
	j.Phase = "staging"
	if err := operation.SaveJournal(options.Directory, j); err != nil {
		return config.ServerConfig{}, err
	}
	if err := fault(options, "staging"); err != nil {
		return config.ServerConfig{}, err
	}
	if err := prepareSQLiteFiles(ctx, p, j, backup); err != nil {
		return config.ServerConfig{}, err
	}
	return config.ServerConfig{DataDir: stage, Database: config.DatabaseConfig{Driver: "sqlite"}, Mode: modeConfig(p.Source.Mode)}, nil
}

func prepareSQLiteFiles(ctx context.Context, p *operation.Plan, j *operation.Journal, backup *BackupReceipt) error {
	path := filepath.Join(j.Staging, "linguaflow.db")
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		if _, err := operation.Copy(ctx, filepath.Join(j.BackupDirectory, backup.Database.Name), path); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	return restoreTargetFiles(ctx, p, j, backup)
}

func preparePostgresFiles(ctx context.Context, p *operation.Plan, j *operation.Journal, backup *BackupReceipt, directory string) error {
	root := p.Target.DataDir
	if err := operation.ValidatePath(filepath.Dir(root), true); err != nil {
		return err
	}
	parent, err := operation.Identity(filepath.Dir(root))
	if err != nil || parent != p.Target.ParentIdentity {
		return operation.Fail(operation.BaselineChanged, "target recovery parent identity changed")
	}
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		if err := credential.CreatePrivateDirectory(root); err != nil {
			return err
		}
		if err := operation.Write(filepath.Join(root, "operation.json"), struct{ ID, Digest string }{p.ID, operation.Digest(p)}, true); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		var owner struct{ ID, Digest string }
		if err := operation.Read(filepath.Join(root, "operation.json"), &owner); err != nil || owner.ID != p.ID || owner.Digest != operation.Digest(p) {
			return operation.Fail(operation.TargetNotEmpty, "unowned PostgreSQL recovery directory")
		}
	}
	identity, err := operation.Identity(root)
	if err != nil {
		return err
	}
	if j.StagingIdentity != "" && j.StagingIdentity != identity {
		return operation.Fail(operation.BaselineChanged, "PostgreSQL recovery directory identity changed")
	}
	j.Staging, j.StagingIdentity = root, identity
	if err := operation.SaveJournal(directory, j); err != nil {
		return err
	}
	return restoreTargetFiles(ctx, p, j, backup)
}

func modeConfig(mode string) string {
	if mode == "local" {
		return config.ModeLocal
	}
	return config.ModeServer
}

func applyStorage(ctx context.Context, db *sql.DB, client *ent.Client, cfg config.ServerConfig, p *operation.Plan, options RunOptions) error {
	if p.StorageManifest == "" {
		return nil
	}
	original := new(storagemigrate.Manifest)
	if err := operation.Read(p.StorageManifest, original); err != nil {
		return err
	}
	if operation.Digest(original) != p.StorageDigest {
		return operation.Fail(operation.Incompatible, "immutable storage subplan changed")
	}
	path := filepath.Join(options.Directory, "storage-journal.json")
	manifest := original
	if _, err := os.Lstat(path); err == nil {
		m, err := storagemigrate.ReadManifest(path)
		if err != nil {
			return err
		}
		if m.OperationID != original.OperationID || storagemigrate.Fingerprint(m) != storagemigrate.Fingerprint(original) {
			return operation.Fail(operation.Incompatible, "storage checkpoint does not bind the original subplan")
		}
		manifest = m
	} else if errors.Is(err, os.ErrNotExist) {
		if err := operation.Write(path, manifest, true); err != nil {
			return err
		}
	} else {
		return err
	}
	m, err := storagemigrate.New(db, client, p.Source.Driver, storagemigrate.Options{LegacyRoot: manifest.LegacyRoot, DefaultRoot: manifest.DefaultRoot, DefaultBackendID: manifest.DefaultBackendID, MaxFileBytes: cfg.Storage.Limits.MaxFileBytes, CapacityBytes: manifest.CapacityBytes, LogicalLimitBytes: manifest.LogicalLimitBytes, CapacitySet: true, LogicalSet: true, Local: p.Source.Mode == "local", Offline: true, BackupConfirmed: true, SchemaPrepared: true})
	if err != nil {
		return err
	}
	return m.Apply(ctx, manifest, func() error {
		if err := fault(options, "resource_commit"); err != nil {
			return err
		}
		return operation.Write(path, manifest, false)
	})
}

func verifyStorage(ctx context.Context, db *sql.DB, client *ent.Client, cfg config.ServerConfig, p *operation.Plan) error {
	if p.StorageManifest == "" {
		return nil
	}
	manifest, err := storagemigrate.ReadManifest(p.StorageManifest)
	if err != nil || operation.Digest(manifest) != p.StorageDigest {
		return operation.Fail(operation.Incompatible, "immutable storage subplan changed")
	}
	m, err := storagemigrate.New(db, client, p.Source.Driver, storagemigrate.Options{LegacyRoot: manifest.LegacyRoot, DefaultRoot: manifest.DefaultRoot, DefaultBackendID: manifest.DefaultBackendID, MaxFileBytes: cfg.Storage.Limits.MaxFileBytes, CapacityBytes: manifest.CapacityBytes, LogicalLimitBytes: manifest.LogicalLimitBytes, CapacitySet: true, LogicalSet: true, Local: p.Source.Mode == "local", SchemaPrepared: true})
	if err != nil {
		return err
	}
	return m.Verify(ctx, manifest)
}

func reconcileClaims(ctx context.Context, client *ent.Client, cfg config.ServerConfig, work string) error {
	storage, err := service.NewStorageService(client, nil, work)
	if err != nil {
		return err
	}
	connections := service.NewStorageConnectionService(client, nil, cfg.Storage, nil)
	retirements, err := service.NewStorageRetirementService(storage, connections, cfg.IsLocal())
	if err != nil {
		return err
	}
	if err := retirements.ReconcilePathClaims(ctx); err != nil {
		return operation.Fail(operation.NeedsAction, "S3 path claims overlap; preserve target for review")
	}
	return nil
}
