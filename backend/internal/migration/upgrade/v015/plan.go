package v015

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/catalog"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/snapshot"
	storagemigrate "github.com/MeowSalty/LinguaFlow/backend/internal/migration/storage"
)

type PlanOptions struct {
	Inputs                                                                                            config.ServerInputs
	Directory, TargetDataDir, TargetConfig, RehearsalConfig, MappingPath, LegacyRoot, ExternalReceipt string
}

func NewPlan(ctx context.Context, options PlanOptions) (*operation.Plan, error) {
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
	inspection, err := Probe(ctx, db, narrow.Database.Driver)
	if err != nil {
		return nil, err
	}
	if inspection.Format != "v015" {
		return nil, operation.Fail(operation.UnsupportedSource, "this route requires the complete v0.15 release source; current or empty databases need no account conversion")
	}
	if inspection.Mode != narrow.Mode {
		return nil, operation.Fail(operation.UnsupportedSource, "configured mode does not match original instance identity")
	}
	resolved, err := ResolveProbedInputs(options.Inputs, narrow)
	if err != nil {
		return nil, err
	}
	if err := compareNarrow(narrow, resolved); err != nil {
		return nil, err
	}
	mapping, err := ReadMapping(options.MappingPath)
	if err != nil {
		return nil, err
	}
	users, err := ReadUsers(ctx, db)
	if err != nil {
		return nil, err
	}
	localID := 0
	if inspection.Mode == "local" {
		if err := db.QueryRowContext(ctx, "SELECT local_user_id FROM instance_initializations WHERE id=1").Scan(&localID); err != nil {
			return nil, err
		}
	}
	accounts, err := MapAccounts(users, mapping, inspection.Mode, localID)
	if err != nil {
		return nil, err
	}
	id, err := operation.NewID()
	if err != nil {
		return nil, err
	}
	sourceIdentity, err := EndpointIdentity(ctx, db, resolved.Config)
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
		return nil, operation.Fail(operation.TargetNotEmpty, "operation directory must not exist")
	}
	p := &operation.Plan{Version: 1, ID: id, Kind: "upgrade", Route: "upgrade-v015-auth1", Build: catalog.Build(), CreatedAt: time.Now().UTC(), Source: operation.Endpoint{ConfigPath: resolved.ConfigPath, DataDir: resolved.Config.DataDir, Driver: narrow.Database.Driver, Mode: inspection.Mode, Identity: sourceIdentity, InputDigest: InputDigest(resolved)}, SourceFingerprint: inspection.Fingerprint, SourceSchema: inspection.SchemaDigest, TargetSignature: catalog.TargetSignature(), Accounts: accounts}
	if p.Source.Driver == "sqlite" {
		p.Source.DatabaseDSN = narrow.DatabaseDSN()
	}
	if options.LegacyRoot == "" {
		options.LegacyRoot = filepath.Join(resolved.Config.DataDir, "jobs")
	}
	if !filepath.IsAbs(options.LegacyRoot) {
		return nil, operation.Fail(operation.Incompatible, "legacy root must be absolute")
	}
	p.LegacyRoot = filepath.Clean(options.LegacyRoot)
	objects, err := SourceObjects(ctx, db, resolved.Config, p.LegacyRoot)
	if err != nil {
		return nil, err
	}
	p.ObjectsDigest = ObjectsDigest(objects)
	if p.Source.Driver == "sqlite" {
		if options.TargetConfig != "" || !filepath.IsAbs(options.TargetDataDir) {
			return nil, operation.Fail(operation.UnsupportedTarget, "SQLite requires a new absolute target data directory")
		}
		p.Target.DataDir = filepath.Clean(options.TargetDataDir)
		if err := operation.ValidatePath(filepath.Dir(p.Target.DataDir), true); err != nil {
			return nil, err
		}
		if _, err := os.Lstat(p.Target.DataDir); !errors.Is(err, os.ErrNotExist) {
			return nil, operation.Fail(operation.TargetNotEmpty, "SQLite target directory already exists")
		}
		p.Target.ParentIdentity, err = operation.Identity(filepath.Dir(p.Target.DataDir))
		if err != nil {
			return nil, err
		}
	} else {
		if options.TargetDataDir != "" || !filepath.IsAbs(options.TargetConfig) {
			return nil, operation.Fail(operation.UnsupportedTarget, "PostgreSQL requires a separate explicitly configured empty target database")
		}
		p.Target.ConfigPath = options.TargetConfig
		p.Target.RehearsalConfigPath = options.RehearsalConfig
		target, err := ResolveTarget(p, false)
		if err != nil {
			return nil, err
		}
		p.Target.DataDir, p.Target.InputDigest = target.DataDir, operation.Digest(target)
		if err := operation.ValidatePath(filepath.Dir(target.DataDir), true); err != nil {
			return nil, err
		}
		if _, err := os.Lstat(target.DataDir); !errors.Is(err, os.ErrNotExist) {
			return nil, operation.Fail(operation.TargetNotEmpty, "PostgreSQL target recovery directory must be new")
		}
		p.Target.ParentIdentity, err = operation.Identity(filepath.Dir(target.DataDir))
		if err != nil {
			return nil, err
		}
		tdb, _, err := snapshot.Open(ctx, &target, false)
		if err != nil {
			return nil, err
		}
		if err := snapshot.EmptyPostgres(ctx, tdb); err != nil {
			tdb.Close()
			return nil, err
		}
		p.Target.Identity, err = snapshot.PostgresIdentity(ctx, tdb)
		tdb.Close()
		if err != nil {
			return nil, err
		}
		if p.Target.Identity == p.Source.Identity {
			return nil, operation.Fail(operation.UnsupportedTarget, "source and target PostgreSQL database identities overlap")
		}
		if _, err := snapshot.InspectPostgresTools(ctx); err != nil {
			return nil, err
		}
		if options.RehearsalConfig != "" {
			if !filepath.IsAbs(options.RehearsalConfig) {
				return nil, operation.Fail(operation.UnsupportedTarget, "rehearsal configuration path must be absolute")
			}
			r, err := ResolveTarget(p, true)
			if err != nil {
				return nil, err
			}
			rdb, _, err := snapshot.Open(ctx, &r, true)
			if err != nil {
				return nil, err
			}
			emptyErr := snapshot.EmptyPostgres(ctx, rdb)
			identity, idErr := snapshot.PostgresIdentity(ctx, rdb)
			rdb.Close()
			if emptyErr != nil {
				return nil, emptyErr
			}
			if idErr != nil {
				return nil, idErr
			}
			if identity == p.Source.Identity || identity == p.Target.Identity || operation.Overlap(r.DataDir, p.Source.DataDir) || operation.Overlap(r.DataDir, p.Target.DataDir) || operation.Overlap(r.DataDir, options.Directory) {
				return nil, operation.Fail(operation.UnsupportedTarget, "rehearsal database and write roots must be independent")
			}
			if _, err := os.Lstat(r.DataDir); !errors.Is(err, os.ErrNotExist) {
				return nil, operation.Fail(operation.TargetNotEmpty, "rehearsal recovery directory must be new")
			}
			p.Target.RehearsalIdentity, p.Target.RehearsalDataDir, p.Target.RehearsalInputDigest = identity, r.DataDir, operation.Digest(r)
		}
	}
	for _, path := range []string{options.Directory, p.Target.DataDir} {
		if path != "" && operation.Overlap(resolved.Config.DataDir, path) {
			return nil, operation.Fail(operation.UnsupportedTarget, "source, target and operation write roots must be separate")
		}
	}
	for _, root := range []string{options.Directory, p.Target.DataDir, p.Target.RehearsalDataDir} {
		if root != "" && operation.Overlap(p.LegacyRoot, root) {
			return nil, operation.Fail(operation.UnsupportedTarget, "write root overlaps explicit legacy originals")
		}
	}
	if p.Target.DataDir != "" && operation.Overlap(options.Directory, p.Target.DataDir) {
		return nil, operation.Fail(operation.UnsupportedTarget, "operation and target directories must be separate")
	}
	if p.Source.Driver == "sqlite" {
		dbPath, err := snapshot.SQLitePath(resolved.Config.DatabaseDSN())
		if err != nil {
			return nil, err
		}
		for _, root := range []string{options.Directory, p.Target.DataDir} {
			if operation.Overlap(root, dbPath) {
				return nil, operation.Fail(operation.UnsupportedTarget, "write root overlaps source database")
			}
		}
	}
	if p.Target.DataDir != "" {
		p.LocalRoots, err = localRoots(resolved.Config, p.Target.DataDir)
		if err != nil {
			return nil, err
		}
	}
	for _, backend := range resolved.Config.Storage.Backends {
		if backend.Driver == "local" {
			for _, root := range []string{options.Directory, p.Target.DataDir} {
				if root != "" && operation.Overlap(backend.Root, root) {
					return nil, operation.Fail(operation.UnsupportedTarget, "write root overlaps source object storage")
				}
			}
		}
	}
	if options.ExternalReceipt != "" {
		if !filepath.IsAbs(options.ExternalReceipt) {
			return nil, operation.Fail(operation.BackupIncomplete, "external receipt path must be absolute")
		}
		proof, err := operation.Hash(ctx, options.ExternalReceipt)
		if err != nil {
			return nil, err
		}
		p.ExternalReceipt, p.ExternalDigest = options.ExternalReceipt, proof.SHA256
	}
	var manifest *storagemigrate.Manifest
	if inspection.LegacyResources > 0 {
		defaultRoot, backend, err := DefaultLocalRoot(resolved.Config)
		if err != nil {
			return nil, err
		}
		if p.Target.DataDir != "" {
			for _, root := range p.LocalRoots {
				if root.BackendID == backend {
					defaultRoot = root.Target
				}
			}
		}
		m, err := storagemigrate.New(db, client, p.Source.Driver, storagemigrate.Options{LegacyRoot: p.LegacyRoot, DefaultRoot: defaultRoot, DefaultBackendID: backend, MaxFileBytes: resolved.Config.Storage.Limits.MaxFileBytes, Local: p.Source.Mode == "local", CapacityBytes: resolved.Config.Storage.Initialization.CapacityBytes.Value, LogicalLimitBytes: resolved.Config.Storage.Initialization.LogicalLimitBytes.Value, CapacitySet: resolved.Config.Storage.Initialization.CapacityBytes.Set, LogicalSet: resolved.Config.Storage.Initialization.LogicalLimitBytes.Set})
		if err != nil {
			return nil, err
		}
		manifest, err = m.Inventory(ctx)
		if err != nil {
			return nil, err
		}
		p.StorageManifest = filepath.Join(options.Directory, "storage-plan.json")
		p.StorageDigest = operation.Digest(manifest)
	}
	if _, err := operation.Create(options.Directory, p); err != nil {
		return nil, err
	}
	if manifest != nil {
		if err := operation.Write(p.StorageManifest, manifest, true); err != nil {
			return nil, err
		}
	}
	return p, nil
}

func compareNarrow(n *config.MigrationDatabaseInputs, r *config.ResolvedServer) error {
	if n.DataDir != r.Config.DataDir || n.Database.Driver != r.Config.Database.Driver {
		return operation.Fail(operation.BaselineChanged, "full deployment inputs differ from the read-only probe")
	}
	// DSN query normalization may add enforced read settings; disk identity is
	// checked again after full resolution rather than exposing either DSN.
	if n.Database.Driver == "sqlite" {
		a, err := snapshot.SQLitePath(n.DatabaseDSN())
		if err != nil {
			return err
		}
		b, err := snapshot.SQLitePath(r.Config.DatabaseDSN())
		if err != nil {
			return err
		}
		if a != b {
			return operation.Fail(operation.BaselineChanged, "database path changed after the format probe")
		}
	}
	return nil
}

func ResolveProbedInputs(inputs config.ServerInputs, n *config.MigrationDatabaseInputs) (*config.ResolvedServer, error) {
	r, err := config.ResolveServerConfig(inputs)
	if err != nil {
		return nil, err
	}
	if r.Config.Database.Driver == "sqlite" {
		r.Config.Database.DSN = n.DatabaseDSN()
	}
	return r, compareNarrow(n, r)
}

func PlannedInputs(p *operation.Plan) config.ServerInputs {
	in := config.ServerInputs{Mode: p.Source.Mode, Environment: Environment(), Overrides: map[string]any{"server.data_dir": p.Source.DataDir}}
	if p.Source.ConfigPath != "" {
		in.ConfigPath = &p.Source.ConfigPath
	}
	return in
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

func EndpointIdentity(ctx context.Context, db *sql.DB, cfg config.ServerConfig) (string, error) {
	if cfg.Database.Driver == "postgres" {
		return snapshot.PostgresIdentity(ctx, db)
	}
	path, err := snapshot.SQLitePath(cfg.DatabaseDSN())
	if err != nil {
		return "", err
	}
	if err := operation.ValidatePath(cfg.DataDir, true); err != nil {
		return "", err
	}
	dirID, err := operation.Identity(cfg.DataDir)
	if err != nil {
		return "", err
	}
	dbID, err := operation.Identity(path)
	if err != nil {
		return "", err
	}
	return operation.Digest([]string{cfg.DataDir, path, dirID, dbID}), nil
}

func DefaultLocalRoot(cfg config.ServerConfig) (string, string, error) {
	backend := cfg.Storage.DefaultSiteSpace
	if backend == "" {
		backend = "local"
	}
	root := filepath.Join(cfg.DataDir, "objects")
	for _, b := range cfg.Storage.Backends {
		if b.ID == backend {
			if b.Driver != "local" {
				return "", "", operation.Fail(operation.UnsupportedTarget, "legacy import requires a local default backend before an explicit business move to S3")
			}
			root = b.Root
		}
	}
	return root, backend, nil
}

func localRoots(cfg config.ServerConfig, target string) ([]operation.LocalRoot, error) {
	backends := cfg.Storage.Backends
	if len(backends) == 0 {
		backends = []config.StorageBackendConfig{{ID: "local", Driver: "local", Root: filepath.Join(cfg.DataDir, "objects")}}
	}
	var roots []operation.LocalRoot
	for _, b := range backends {
		if b.Driver != "local" || b.ID == "legacy" {
			continue
		}
		destination := b.Root
		if operation.Overlap(cfg.DataDir, b.Root) {
			relative, err := filepath.Rel(cfg.DataDir, b.Root)
			if err != nil || relative == "." || strings.HasPrefix(relative, "..") {
				return nil, operation.Fail(operation.UnsupportedTarget, "local backend root must be a separate source subdirectory")
			}
			destination = filepath.Join(target, relative)
		}
		root := operation.LocalRoot{BackendID: b.ID, Source: b.Root, Target: destination}
		if _, err := os.Lstat(b.Root); err == nil {
			if err := operation.ValidatePath(b.Root, true); err != nil {
				return nil, err
			}
			root.Identity, err = operation.Identity(b.Root)
			if err != nil {
				return nil, err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		marker := filepath.Join(b.Root, ".linguaflow", "space.json")
		if _, err := os.Lstat(marker); err == nil {
			proof, err := operation.Hash(context.Background(), marker)
			if err != nil {
				return nil, err
			}
			root.MarkerDigest = proof.SHA256
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		roots = append(roots, root)
	}
	return roots, nil
}

func SourceLocalRoots(cfg config.ServerConfig) ([]operation.LocalRoot, error) {
	return localRoots(cfg, cfg.DataDir)
}

func ResolveTarget(p *operation.Plan, rehearse bool) (config.ServerConfig, error) {
	path := p.Target.ConfigPath
	if rehearse {
		path = p.Target.RehearsalConfigPath
	}
	if path == "" {
		return config.ServerConfig{}, operation.Fail(operation.UnsupportedTarget, "PostgreSQL rehearsal requires its own independent configured database")
	}
	environment := map[string]string{}
	for _, v := range os.Environ() {
		k, value, ok := strings.Cut(v, "=")
		if ok {
			environment[k] = value
		}
	}
	n, err := config.ResolveMigrationDatabase(config.ServerInputs{Mode: "serve", ConfigPath: &path, Environment: environment})
	if err != nil {
		return config.ServerConfig{}, err
	}
	if n.Database.Driver != "postgres" {
		return config.ServerConfig{}, operation.Fail(operation.UnsupportedTarget, "independent target must use PostgreSQL")
	}
	cfg := config.ServerConfig{DataDir: n.DataDir, Database: n.Database, Mode: config.ModeServer}
	digest := p.Target.InputDigest
	if rehearse {
		digest = p.Target.RehearsalInputDigest
	}
	if digest != "" && operation.Digest(cfg) != digest {
		return config.ServerConfig{}, operation.Fail(operation.BaselineChanged, "target database inputs changed")
	}
	return cfg, nil
}
