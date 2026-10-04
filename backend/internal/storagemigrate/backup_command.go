package storagemigrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/spf13/cobra"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagespace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storageauth"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storagebackup"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storagenet"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/localstore"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/s3store"
)

func newBackupCommand(configPath, mode, manifestPath *string, offline *bool) *cobra.Command {
	var destination string
	var retention time.Duration
	root := &cobra.Command{Use: "backup", Short: "Pinned manifests, consistent offline backup and read-only restore checks"}
	root.PersistentFlags().StringVar(&destination, "destination", "", "new private directory for DB, keyring, deployment inputs and manifest")
	root.PersistentFlags().DurationVar(&retention, "retention", 30*24*time.Hour, "finite object pin retention, at most 366 days")
	for _, action := range []string{"manifest", "capture", "restore-check"} {
		action := action
		root.AddCommand(&cobra.Command{Use: action, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			if !*offline {
				return errors.New("backup commands require --offline: stop all writers and GC first")
			}
			environment := map[string]string{}
			for _, entry := range os.Environ() {
				key, value, ok := strings.Cut(entry, "=")
				if ok {
					environment[key] = value
				}
			}
			inputs := config.ServerInputs{Mode: *mode, Environment: environment}
			if cmd.Flags().Changed("config") {
				inputs.ConfigPath = configPath
			}
			resolved, err := config.ResolveServerConfig(inputs)
			if err != nil {
				return err
			}
			cfg := resolved.Config
			cfg.AutoMigrate = true
			if action == "restore-check" {
				if !cfg.Storage.Maintenance {
					return errors.New("restore-check requires server.storage.maintenance=true in the restored deployment")
				}
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
			resolve, closeDrivers := backupResolver(client, resolved.CredentialKeys, cfg)
			defer closeDrivers()
			switch action {
			case "manifest":
				if *manifestPath == "" {
					return errors.New("--manifest is required")
				}
				_, manifest, err := storagebackup.CaptureMetadata(cmd.Context(), client, time.Now().UTC().Add(retention))
				if err != nil {
					return err
				}
				data, err := json.MarshalIndent(manifest, "", "  ")
				if err != nil {
					return err
				}
				created, err := credential.PublishPrivateFile(*manifestPath, data)
				if err != nil {
					return err
				}
				if !created {
					return errors.New("backup manifest already exists")
				}
				fmt.Fprintln(cmd.OutOrStdout(), "metadata_only: exact object locations are pinned; no complete database/keyring backup was claimed.")
			case "capture":
				manifest, err := storagebackup.CaptureOffline(cmd.Context(), db, client, resolve, storagebackup.OfflineOptions{Directory: destination, ExpiresAt: time.Now().UTC().Add(retention), Offline: true, Config: cfg, Keys: resolved.CredentialKeys})
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Backup %s: %d objects; manifest at %s. Managed objects remain in their original spaces and are pinned until %s.\n", manifest.Status, len(manifest.Objects), filepath.Join(destination, "manifest.json"), manifest.ExpiresAt.Format(time.RFC3339))
				if manifest.Status != "complete" {
					return errors.New("backup is incomplete; inspect per-object status and retained pins in the manifest")
				}
			case "restore-check":
				if *manifestPath == "" {
					return errors.New("--manifest is required")
				}
				manifest, err := storagebackup.ReadManifest(*manifestPath)
				if err != nil {
					return err
				}
				ready, err := storagebackup.RestoreCheck(cmd.Context(), client, resolve, filepath.Dir(*manifestPath), manifest)
				if err != nil {
					return err
				}
				if !ready {
					return errors.New("restore verification found missing or corrupt objects; maintenance remains enabled")
				}
				fmt.Fprintln(cmd.OutOrStdout(), "Restored references, backup artifacts, keys and object bytes verified. Maintenance remains enabled; no writers, retries or GC were started.")
			}
			return nil
		}})
	}
	return root
}

func backupResolver(client *ent.Client, keys *credential.Keyring, cfg config.ServerConfig) (storagebackup.Resolver, func()) {
	locals := map[int]*localstore.Store{}
	verified := map[int]bool{}
	connections := service.NewStorageConnectionService(client, keys, cfg.Storage, func(ctx context.Context, c *ent.StorageConnection, sp *ent.StorageSpace, payload storageauth.S3Payload) (storage.Driver, error) {
		httpClient, err := storagenet.NewClient(c.Endpoint, cfg.Storage.NetworkPolicy(sp.Bucket))
		if err != nil {
			return nil, err
		}
		return s3store.New(s3store.Options{Endpoint: c.Endpoint, Region: c.Region, Bucket: sp.Bucket, Prefix: sp.Prefix, PathStyle: c.PathStyle, Credentials: credentials.NewStaticCredentialsProvider(payload.AccessKeyID, payload.SecretAccessKey, payload.SessionToken), HTTPClient: httpClient})
	})
	resolve := func(ctx context.Context, id int) (storage.Driver, error) {
		space, err := client.StorageSpace.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		connection, err := client.StorageConnection.Get(ctx, space.ConnectionID)
		if err != nil {
			return nil, err
		}
		if space.Status == storagespace.StatusDisabled || connection.Status != storageconnection.StatusEnabled {
			return nil, storage.ErrPermission
		}
		checkMarker := func(driver storage.Driver) (storage.Driver, error) {
			if connection.Driver == storageconnection.DriverLocal && connection.BackendID == "legacy" {
				return driver, nil
			}
			if !space.Verified {
				return nil, storage.ErrAuthRequired
			}
			if !verified[id] {
				if err := service.VerifyStorageSpaceMarker(ctx, driver, space); err != nil {
					return nil, err
				}
				verified[id] = true
			}
			return driver, nil
		}
		if connection.Driver != storageconnection.DriverLocal {
			driver, err := connections.ResolveDriver(ctx, id, false)
			if err != nil {
				return nil, err
			}
			return checkMarker(driver)
		}
		if driver := locals[id]; driver != nil {
			return driver, nil
		}
		root := ""
		for _, backend := range cfg.Storage.Backends {
			if backend.ID == connection.BackendID && backend.Driver == "local" {
				root = backend.Root
			}
		}
		if root == "" && connection.BackendID == "local" && len(cfg.Storage.Backends) == 0 {
			root = filepath.Join(cfg.DataDir, "objects")
		}
		if root == "" {
			return nil, storage.ErrUnavailable
		}
		driver, err := localstore.OpenExisting(root)
		if err != nil {
			return nil, err
		}
		if _, err := checkMarker(driver); err != nil {
			driver.Close()
			return nil, err
		}
		locals[id] = driver
		return driver, nil
	}
	return resolve, func() {
		for _, driver := range locals {
			driver.Close()
		}
	}
}
