package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/catalog"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/snapshot"
	storagemigrate "github.com/MeowSalty/LinguaFlow/backend/internal/migration/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/upgrade/v015"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

type storagePlanOptions struct {
	Inputs                                           config.ServerInputs
	Directory, LegacyRoot, Manifest, ExternalReceipt string
}

func newStorageCommand() *cobra.Command {
	root := &cobra.Command{Use: "storage", Short: "Import only undeclared legacy references with durable resource receipts"}
	var flags databaseFlags
	var options storagePlanOptions
	plan := &cobra.Command{Use: "plan", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		options.Inputs = flags.inputs(cmd)
		p, err := planStorage(cmd.Context(), options)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), p)
	}}
	flags.bind(plan)
	plan.Flags().StringVar(&options.Directory, "operation-dir", "", "absolute new private operation directory")
	plan.Flags().StringVar(&options.LegacyRoot, "legacy-root", "", "absolute original legacy files root")
	plan.Flags().StringVar(&options.Manifest, "manifest", "", "explicit published ManifestVersion=1 file to take over read-only")
	plan.Flags().StringVar(&options.ExternalReceipt, "external-backup-receipt", "", "exact independent S3 recovery evidence")
	root.AddCommand(plan)
	var directory string
	var offline bool
	apply := &cobra.Command{Use: "apply", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		r, err := runStorageOperation(cmd.Context(), directory, "resume", offline, nil)
		if err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), r)
	}}
	operationFlags(apply, &directory, &offline)
	root.AddCommand(apply)
	return root
}

func planStorage(ctx context.Context, options storagePlanOptions) (*operation.Plan, error) {
	narrow, err := config.ResolveMigrationDatabase(options.Inputs)
	if err != nil {
		return nil, err
	}
	cfg := config.ServerConfig{DataDir: narrow.DataDir, Database: narrow.Database}
	db, client, err := snapshot.Open(ctx, &cfg, true)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var manifest *storagemigrate.Manifest
	if options.Manifest != "" {
		manifest, err = storagemigrate.ReadManifest(options.Manifest)
		if err != nil {
			return nil, err
		}
	}
	manifestID := ""
	if manifest != nil {
		manifestID = manifest.OperationID
	}
	inspection, err := v015.ProbeOperation(ctx, db, narrow.Database.Driver, nil, manifestID)
	if err != nil {
		return nil, err
	}
	if inspection.Format != "current" || inspection.Mode != narrow.Mode {
		return nil, operation.Fail(operation.UnsupportedSource, "storage import requires a complete current account and business format")
	}
	resolved, err := v015.ResolveProbedInputs(options.Inputs, narrow)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(options.Directory) {
		return nil, operation.Fail(operation.Incompatible, "operation directory must be an absolute new path")
	}
	if err := operation.ValidatePath(filepath.Dir(options.Directory), true); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(options.Directory); !errors.Is(err, os.ErrNotExist) {
		return nil, operation.Fail(operation.TargetNotEmpty, "operation directory already exists")
	}
	if options.LegacyRoot == "" {
		options.LegacyRoot = filepath.Join(cfg.DataDir, "jobs")
	}
	if manifest != nil {
		if options.LegacyRoot != filepath.Join(cfg.DataDir, "jobs") && options.LegacyRoot != manifest.LegacyRoot {
			return nil, operation.Fail(operation.Incompatible, "explicit legacy root differs from published manifest")
		}
		options.LegacyRoot = manifest.LegacyRoot
	}
	if !filepath.IsAbs(options.LegacyRoot) {
		return nil, operation.Fail(operation.Incompatible, "legacy root must be absolute")
	}
	defaultRoot, backend, err := v015.DefaultLocalRoot(resolved.Config)
	if err != nil {
		return nil, err
	}
	for _, root := range []string{resolved.Config.DataDir, options.LegacyRoot, defaultRoot} {
		if operation.Overlap(root, options.Directory) {
			return nil, operation.Fail(operation.UnsupportedTarget, "tool write root overlaps source database or referenced files")
		}
	}
	opts := storagemigrate.Options{LegacyRoot: options.LegacyRoot, DefaultRoot: defaultRoot, DefaultBackendID: backend, MaxFileBytes: resolved.Config.Storage.Limits.MaxFileBytes, Local: inspection.Mode == "local", CapacityBytes: resolved.Config.Storage.Initialization.CapacityBytes.Value, LogicalLimitBytes: resolved.Config.Storage.Initialization.LogicalLimitBytes.Value, CapacitySet: resolved.Config.Storage.Initialization.CapacityBytes.Set, LogicalSet: resolved.Config.Storage.Initialization.LogicalLimitBytes.Set, SchemaPrepared: true}
	migrator, err := storagemigrate.New(db, client, narrow.Database.Driver, opts)
	if err != nil {
		return nil, err
	}
	if manifest == nil {
		manifest, err = migrator.Inventory(ctx)
		if err != nil {
			return nil, err
		}
	} else if manifest.LegacyRoot != opts.LegacyRoot || manifest.DefaultRoot != opts.DefaultRoot || manifest.DefaultBackendID != opts.DefaultBackendID {
		return nil, operation.Fail(operation.Incompatible, "published manifest roots differ from explicit deployment")
	}
	id, err := operation.NewID()
	if err != nil {
		return nil, err
	}
	identity, err := v015.EndpointIdentity(ctx, db, resolved.Config)
	if err != nil {
		return nil, err
	}
	p := &operation.Plan{Version: 1, ID: id, Kind: "storage", Route: "storage-legacy-import", Build: catalog.Build(), CreatedAt: time.Now().UTC(), Source: operation.Endpoint{ConfigPath: resolved.ConfigPath, DataDir: resolved.Config.DataDir, Driver: narrow.Database.Driver, Mode: inspection.Mode, Identity: identity, InputDigest: v015.InputDigest(resolved)}, SourceFingerprint: inspection.Fingerprint, SourceSchema: inspection.SchemaDigest, TargetSignature: catalog.TargetSignature(), LegacyRoot: manifest.LegacyRoot, StorageManifest: filepath.Join(options.Directory, "storage-plan.json"), StorageDigest: operation.Digest(manifest)}
	if p.Source.Driver == "sqlite" {
		p.Source.DatabaseDSN = narrow.DatabaseDSN()
		path, err := snapshot.SQLitePath(narrow.DatabaseDSN())
		if err != nil {
			return nil, err
		}
		if operation.Overlap(path, options.Directory) {
			return nil, operation.Fail(operation.UnsupportedTarget, "operation directory overlaps the selected database")
		}
	}
	p.LocalRoots, err = v015.SourceLocalRoots(resolved.Config)
	if err != nil {
		return nil, err
	}
	for _, root := range p.LocalRoots {
		if operation.Overlap(root.Source, options.Directory) {
			return nil, operation.Fail(operation.UnsupportedTarget, "operation directory overlaps referenced storage")
		}
	}
	objects, err := v015.SourceObjects(ctx, db, resolved.Config, p.LegacyRoot)
	if err != nil {
		return nil, err
	}
	p.ObjectsDigest = v015.ObjectsDigest(objects)
	p.StorageBaseline, p.StorageMaxIDs, err = storagemigrate.Baseline(ctx, db, manifest, nil)
	if err != nil {
		return nil, err
	}
	if options.Manifest != "" {
		proof, err := operation.Hash(ctx, options.Manifest)
		if err != nil {
			return nil, err
		}
		p.ImportedManifest, p.ImportedDigest = options.Manifest, proof.SHA256
	}
	if options.ExternalReceipt != "" {
		proof, err := operation.Hash(ctx, options.ExternalReceipt)
		if err != nil {
			return nil, err
		}
		p.ExternalReceipt, p.ExternalDigest = options.ExternalReceipt, proof.SHA256
	}
	if _, err := operation.Create(options.Directory, p); err != nil {
		return nil, err
	}
	if err := operation.Write(p.StorageManifest, manifest, true); err != nil {
		return nil, err
	}
	return p, nil
}

func loadStorageManifest(ctx context.Context, p *operation.Plan, directory string) (*storagemigrate.Manifest, error) {
	if p.ImportedManifest != "" {
		proof, err := operation.Hash(ctx, p.ImportedManifest)
		if err != nil || proof.SHA256 != p.ImportedDigest {
			return nil, operation.Fail(operation.Incompatible, "published manifest bytes changed; retain the original file")
		}
	}
	original, err := storagemigrate.ReadManifest(p.StorageManifest)
	if err != nil {
		return nil, err
	}
	if operation.Digest(original) != p.StorageDigest {
		return nil, operation.Fail(operation.Incompatible, "immutable storage plan changed")
	}
	path := filepath.Join(directory, "storage-journal.json")
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return original, nil
	} else if err != nil {
		return nil, err
	}
	m, err := storagemigrate.ReadManifest(path)
	if err != nil {
		return nil, err
	}
	if m.OperationID != original.OperationID || storagemigrate.Fingerprint(m) != storagemigrate.Fingerprint(original) {
		return nil, operation.Fail(operation.Incompatible, "storage checkpoint belongs to another plan")
	}
	return m, nil
}

func runStorageOperation(ctx context.Context, directory, action string, offline bool, fault func(string) error) (report *operation.Report, err error) {
	mutate := action != "verify"
	if mutate && !offline {
		return nil, operation.Fail(operation.NeedsAction, "declare --offline after stopping writers, workers and GC")
	}
	p, j, err := operation.Load(directory)
	if err != nil {
		return nil, err
	}
	if p.Kind != "storage" || p.Build != catalog.Build() || p.TargetSignature != catalog.TargetSignature() {
		return nil, operation.Fail(operation.Incompatible, "use this storage operation's exact tool build")
	}
	if mutate {
		unlock, e := operation.Lock(directory)
		if e != nil {
			return nil, e
		}
		defer func() { err = errors.Join(err, unlock()) }()
	}
	manifest, err := loadStorageManifest(ctx, p, directory)
	if err != nil {
		return nil, err
	}
	inputs := v015.PlannedInputs(p)
	narrow, err := config.ResolveMigrationDatabase(inputs)
	if err != nil {
		return nil, err
	}
	if p.Source.Driver == "sqlite" && narrow.DatabaseDSN() != p.Source.DatabaseDSN {
		return nil, operation.Fail(operation.BaselineChanged, "selected database input changed")
	}
	probeCfg := config.ServerConfig{DataDir: narrow.DataDir, Database: narrow.Database}
	if mutate && p.Source.Driver == "postgres" {
		leaseDB, _, e := snapshot.Open(ctx, &probeCfg, false)
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
	db, _, err := snapshot.Open(ctx, &probeCfg, true)
	if err != nil {
		return nil, err
	}
	inspection, err := v015.ProbeOperation(ctx, db, p.Source.Driver, p, manifest.OperationID)
	if action == "rollback" {
		inspection, err = v015.ProbeRollback(ctx, db, p.Source.Driver, p, manifest.OperationID)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	steps, state, reservationErr := operation.Steps(ctx, db, p)
	reserved := reservationErr == nil
	if !reserved && (inspection.Fingerprint != p.SourceFingerprint || inspection.SchemaDigest != p.SourceSchema) {
		db.Close()
		return nil, operation.Fail(operation.BaselineChanged, "storage source changed before reservation")
	}
	if mutate && action == "resume" && j.Phase == "completed" {
		db.Close()
		return runStorageOperation(ctx, directory, "verify", false, nil)
	}
	resolved, err := v015.ResolveProbedInputs(inputs, narrow)
	if err != nil {
		db.Close()
		return nil, err
	}
	identity, err := v015.EndpointIdentity(ctx, db, resolved.Config)
	if err != nil || identity != p.Source.Identity || v015.InputDigest(resolved) != p.Source.InputDigest {
		db.Close()
		return nil, operation.Fail(operation.BaselineChanged, "source path, configuration or encryption keys changed")
	}
	baseline, _, err := storagemigrate.Baseline(ctx, db, manifest, p.StorageMaxIDs)
	if err != nil || baseline != p.StorageBaseline {
		db.Close()
		return nil, operation.Fail(operation.BaselineChanged, "business rows changed outside the declared import")
	}
	var receipt *v015.BackupReceipt
	if j.BackupDirectory != "" {
		receipt, err = v015.LoadBackupReceipt(ctx, j.BackupDirectory, p)
		if err != nil {
			if _, e := os.Lstat(filepath.Join(j.BackupDirectory, "receipt.json")); e == nil {
				db.Close()
				return nil, err
			}
		}
	}
	if receipt == nil {
		if reserved || !mutate || action == "rollback" {
			db.Close()
			return nil, operation.Fail(operation.BackupIncomplete, "actual pre-import recovery receipt is required")
		}
		j.BackupDirectory = filepath.Join(directory, "backup")
		if _, e := os.Lstat(j.BackupDirectory); !errors.Is(e, os.ErrNotExist) {
			id, e := operation.NewID()
			if e != nil {
				db.Close()
				return nil, e
			}
			j.BackupDirectory = filepath.Join(directory, "backup-"+id)
		}
		j.Phase = "backing_up"
		if err := operation.SaveJournal(directory, j); err != nil {
			db.Close()
			return nil, err
		}
		receipt, err = v015.CaptureSourceBackup(ctx, db, resolved, p, j.BackupDirectory)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	if err := v015.VerifyExternalBackup(ctx, db, resolved.Config, resolved.CredentialKeys, p, receipt.Objects); err != nil {
		db.Close()
		return nil, err
	}
	db.Close()
	if fault != nil {
		if err := fault("backup"); err != nil {
			return nil, err
		}
	}
	db, client, err := snapshot.Open(ctx, &resolved.Config, !mutate)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	migrator, err := storagemigrate.New(db, client, p.Source.Driver, storagemigrate.Options{LegacyRoot: manifest.LegacyRoot, DefaultRoot: manifest.DefaultRoot, DefaultBackendID: manifest.DefaultBackendID, MaxFileBytes: resolved.Config.Storage.Limits.MaxFileBytes, Local: p.Source.Mode == "local", CapacityBytes: manifest.CapacityBytes, LogicalLimitBytes: manifest.LogicalLimitBytes, CapacitySet: true, LogicalSet: true, Offline: true, BackupConfirmed: true, SchemaPrepared: true})
	if err != nil {
		return nil, err
	}
	if action == "verify" {
		if !reserved || state != "ready" {
			return nil, operation.Fail(operation.NeedsAction, "storage operation is not ready")
		}
		if err := migrator.Verify(ctx, manifest); err != nil {
			return nil, err
		}
		fp, err := v015.Fingerprint(ctx, db, p.Source.Driver)
		if err != nil || j.TargetFingerprint != "" && fp != j.TargetFingerprint {
			return nil, operation.Fail(operation.BaselineChanged, "storage target changed after verification")
		}
		return storageReport(p, j, manifest, receipt, true), nil
	}
	defer func() {
		if err != nil {
			j.Phase = "needs_action"
			j.ErrorCode = operation.NeedsAction
			var classified *operation.Error
			if errors.As(err, &classified) {
				j.ErrorCode = classified.Code
			}
			_ = operation.SaveJournal(directory, j)
		}
	}()
	if action != "rollback" && (j.Phase == "rolled_back" || state == "abandoned") {
		return nil, operation.Fail(operation.Incompatible, "rolled back import cannot resume")
	}
	if !reserved {
		if err := operation.ReserveStorage(ctx, db, p); err != nil {
			return nil, err
		}
	}
	checkpoint := func() error {
		if fault != nil {
			if err := fault("resource_commit"); err != nil {
				return err
			}
		}
		path := filepath.Join(directory, "storage-journal.json")
		_, e := os.Lstat(path)
		return operation.Write(path, manifest, errors.Is(e, os.ErrNotExist))
	}
	if action == "rollback" {
		if err := migrator.Rollback(ctx, manifest, checkpoint); err != nil {
			return nil, err
		}
		if err := operation.Abandon(ctx, db, p.Source.Driver, p); err != nil {
			return nil, err
		}
		j.Phase = "rolled_back"
	} else {
		if !hasStep(steps, "storage") {
			j.Phase = "converting"
			if err := operation.SaveJournal(directory, j); err != nil {
				return nil, err
			}
			if err := migrator.Apply(ctx, manifest, checkpoint); err != nil {
				return nil, err
			}
			if err := migrator.Verify(ctx, manifest); err != nil {
				return nil, err
			}
			if err := operation.Step(ctx, db, p.Source.Driver, p, "storage", func(*ent.Client) error { return nil }); err != nil {
				return nil, err
			}
		}
		if !hasStep(steps, "ready") {
			if err := migrator.Verify(ctx, manifest); err != nil {
				return nil, err
			}
			if fault != nil {
				if err := fault("before_ready"); err != nil {
					return nil, err
				}
			}
			if err := operation.Step(ctx, db, p.Source.Driver, p, "ready", func(c *ent.Client) error {
				storage, err := service.NewStorageService(c, nil, filepath.Join(directory, "work"))
				if err != nil {
					return err
				}
				connections := service.NewStorageConnectionService(c, resolved.CredentialKeys, resolved.Config.Storage, nil)
				retirements, err := service.NewStorageRetirementService(storage, connections, p.Source.Mode == "local")
				if err != nil {
					return err
				}
				if err := retirements.ReconcilePathClaims(ctx); err != nil {
					return err
				}
				return operation.MarkReady(ctx, c, p)
			}); err != nil {
				return nil, err
			}
			if fault != nil {
				if err := fault("ready_commit"); err != nil {
					return nil, err
				}
			}
		}
		j.TargetFingerprint, err = v015.Fingerprint(ctx, db, p.Source.Driver)
		if err != nil {
			return nil, err
		}
		j.Steps, _, err = operation.Steps(ctx, db, p)
		if err != nil {
			return nil, err
		}
		j.Phase = "completed"
	}
	if err := operation.SaveJournal(directory, j); err != nil {
		return nil, err
	}
	report = storageReport(p, j, manifest, receipt, action != "rollback")
	path := filepath.Join(directory, "report.json")
	_, e := os.Lstat(path)
	return report, operation.Write(path, report, errors.Is(e, os.ErrNotExist))
}

func hasStep(steps []string, step string) bool {
	for _, s := range steps {
		if s == step {
			return true
		}
	}
	return false
}
func storageReport(p *operation.Plan, j *operation.Journal, m *storagemigrate.Manifest, r *v015.BackupReceipt, verified bool) *operation.Report {
	return &operation.Report{Version: 1, ID: p.ID, Phase: j.Phase, Verified: verified, SourceFingerprint: p.SourceFingerprint, BackupTime: r.CreatedAt, Counts: map[string]int64{"legacy_resources": int64(len(m.Entries)), "unknown_objects": int64(m.UnknownObjects)}, Next: []string{"Preserve the original legacy backend identity and exact file root; review missing or unproven source facts before allowing retries and GC.", "Rollback retains database/keyring/object recovery materials; later edits, job snapshots and pins can refuse metadata reversal."}}
}
