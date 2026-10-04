package storagemigrate

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/spf13/cobra"
)

// NewCommand 有意不挂接到常规的服务命令树上。
func NewCommand() *cobra.Command {
	var configPath, mode, manifestPath, legacyRoot string
	var offline, backup bool
	root := &cobra.Command{Use: "linguaflow-storage-migrate", Short: "Offline inventory and migration of legacy file references", SilenceErrors: true, SilenceUsage: true}
	root.PersistentFlags().StringVar(&configPath, "config", "", "server deployment configuration file")
	root.PersistentFlags().StringVar(&mode, "mode", "serve", "deployment mode: serve or local")
	root.PersistentFlags().StringVar(&manifestPath, "manifest", "", "operation manifest/checkpoint path")
	root.PersistentFlags().StringVar(&legacyRoot, "legacy-root", "", "legacy file root (default data_dir/jobs)")
	root.PersistentFlags().BoolVar(&offline, "offline", false, "confirm all service writers and workers are stopped")
	root.PersistentFlags().BoolVar(&backup, "backup-confirmed", false, "confirm an independent database and keyring backup exists")
	root.AddCommand(newBackupCommand(&configPath, &mode, &manifestPath, &offline))
	for _, name := range []string{"inventory", "dry-run", "apply", "resume", "rollback"} {
		name := name
		cmd := &cobra.Command{Use: name, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
			if manifestPath == "" {
				return errors.New("--manifest is required")
			}
			environment := map[string]string{}
			for _, entry := range os.Environ() {
				key, value, ok := strings.Cut(entry, "=")
				if ok {
					environment[key] = value
				}
			}
			inputs := config.ServerInputs{Mode: mode, Environment: environment}
			if cmd.Flags().Changed("config") {
				inputs.ConfigPath = &configPath
			}
			resolved, err := config.ResolveServerConfig(inputs)
			if err != nil {
				return err
			}
			cfg := resolved.Config
			oldRoot := legacyRoot
			if oldRoot == "" {
				oldRoot = filepath.Join(cfg.DataDir, "jobs")
			}
			defaultRoot := filepath.Join(cfg.DataDir, "objects")
			backendID := cfg.Storage.DefaultSiteSpace
			if backendID == "" {
				backendID = "local"
			}
			for _, backend := range cfg.Storage.Backends {
				if backend.ID == backendID {
					if backend.Driver != "local" {
						return errors.New("offline legacy import requires a local default space; import locally before explicit project migration to S3")
					}
					defaultRoot = backend.Root
				}
			}
			readOnly := name == "inventory" || name == "dry-run"
			if !readOnly && (!offline || !backup) {
				return errors.New("--offline and --backup-confirmed are required for mutation")
			}
			// Open 的自动标志只控制时间戳/schema 校验。本
			// 命令在 Apply 内部显式调用 Schema.Create，绝不会在 inventory 中调用。
			cfg.AutoMigrate = true
			if readOnly {
				cfg.Database.DSN, err = readOnlyDSN(cfg.Database.Driver, cfg.DatabaseDSN())
				if err != nil {
					return err
				}
			}
			db, client, err := database.Open(cmd.Context(), &cfg)
			if err != nil {
				return err
			}
			defer db.Close()
			migration, err := New(db, client, cfg.Database.Driver, Options{LegacyRoot: oldRoot, DefaultRoot: defaultRoot, DefaultBackendID: backendID, MaxFileBytes: cfg.Storage.Limits.MaxFileBytes, CapacityBytes: cfg.Storage.Limits.CapacityBytes, Offline: offline, BackupConfirmed: backup})
			if err != nil {
				return err
			}
			if readOnly {
				manifest, err := migration.Inventory(cmd.Context())
				if err != nil {
					return err
				}
				if err := SaveManifest(manifestPath, manifest, true); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Inventory saved: %d resources, %d observed bytes, %d objects with unknown size, %d legacy cleanup records requiring ownership review. Database and source files were not modified.\n", len(manifest.Entries), manifest.KnownBytes, manifest.UnknownObjects, len(manifest.LegacyCleanup))
				return nil
			}
			manifest, err := ReadManifest(manifestPath)
			if err != nil {
				return err
			}
			save := func() error { return SaveManifest(manifestPath, manifest, false) }
			if name == "rollback" {
				err = migration.Rollback(cmd.Context(), manifest, save)
			} else {
				err = migration.Apply(cmd.Context(), manifest, save)
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Storage metadata migration %s; source files and keyring remain in place. Configure backend legacy root as %s before starting the new service.\n", manifest.Phase, manifest.LegacyRoot)
			return nil
		}}
		root.AddCommand(cmd)
	}
	return root
}

func readOnlyDSN(driver, dsn string) (string, error) {
	if driver == config.DatabaseDriverSQLite {
		base, query, _ := strings.Cut(dsn, "?")
		if strings.Contains(base, ":memory:") {
			return "", errors.New("inventory requires an existing persistent database")
		}
		params, err := url.ParseQuery(query)
		if err != nil {
			return "", errors.New("invalid SQLite DSN")
		}
		params.Set("mode", "ro")
		// 只读诊断不得尝试持久化修改 journal 模式。
		pragmas := params["_pragma"]
		params.Del("_pragma")
		for _, pragma := range pragmas {
			if !strings.HasPrefix(strings.ToLower(pragma), "journal_mode") && !strings.HasPrefix(strings.ToLower(pragma), "synchronous") {
				params.Add("_pragma", pragma)
			}
		}
		if !strings.HasPrefix(base, "file:") {
			base = "file:" + filepath.ToSlash(base)
		}
		return base + "?" + params.Encode(), nil
	}
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			return "", errors.New("invalid PostgreSQL DSN")
		}
		query := parsed.Query()
		query.Set("default_transaction_read_only", "on")
		parsed.RawQuery = query.Encode()
		return parsed.String(), nil
	}
	return dsn + " default_transaction_read_only=on", nil
}
