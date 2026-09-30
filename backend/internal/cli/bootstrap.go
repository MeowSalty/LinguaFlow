package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/api"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/logging"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

// BootOptions supplies the completed, read-only resolution result. Tests may
// inject a logger; production always constructs it from the resolved log input.
type BootOptions struct {
	Resolved *config.ResolvedServer
	Logger   *slog.Logger
}

func bootstrapServer(ctx context.Context, opts BootOptions) (*api.Server, net.Listener, func() error, error) {
	if opts.Resolved == nil {
		return nil, nil, nil, errors.New("resolved deployment configuration is required")
	}
	resolved := opts.Resolved
	cfg := resolved.Config
	logger := opts.Logger
	if logger == nil {
		logger = logging.New(os.Stderr, resolved.Log.Level, resolved.Log.Format)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, nil, nil, fmt.Errorf("prepare data directory: %w", err)
	}
	if resolved.LocalSecretPath != "" {
		secret, err := prepareLocalSecret(resolved.LocalSecretPath)
		if err != nil {
			return nil, nil, nil, err
		}
		cfg.JWTSecret = secret
	}
	if err := config.ValidateServerConfig(&cfg); err != nil {
		return nil, nil, nil, err
	}
	db, client, cleanup, err := prepareDatabase(ctx, &cfg)
	if err != nil {
		return nil, nil, nil, err
	}
	initialization := service.NewInitializationService(client)
	allowCreateKeyring := false
	if cfg.IsLocal() {
		empty, err := initialization.IsEmpty(ctx)
		if err != nil {
			_ = cleanup()
			return nil, nil, nil, fmt.Errorf("read initialization state: %w", err)
		}
		allowCreateKeyring = empty
	}
	if _, err := credential.PrepareKeyring(cfg.Credentials.KeyringFile, allowCreateKeyring); err != nil {
		_ = cleanup()
		return nil, nil, nil, fmt.Errorf("prepare credential keyring: %w", err)
	}
	localUser, err := initialization.Initialize(ctx, cfg.Mode, resolved.Bootstrap)
	if err != nil {
		_ = cleanup()
		return nil, nil, nil, fmt.Errorf("initialize instance: %w", err)
	}
	ln, err := bindListener(ctx, &cfg, resolved.AllowNetwork)
	if err != nil {
		_ = cleanup()
		return nil, nil, nil, err
	}
	address := ln.Addr().(*net.TCPAddr)
	runtimeAddress := config.RuntimeAddress{Host: address.IP.String(), Port: address.Port}
	server, err := api.NewServer(&cfg, logger, db, client, cfg.Mode, localUser, runtimeAddress)
	if err != nil {
		_ = ln.Close()
		_ = cleanup()
		return nil, nil, nil, err
	}
	logger.Info("server bootstrapped", "mode", cfg.Mode, "requested_address", cfg.Address(), "bound_address", ln.Addr().String(), "database_driver", cfg.Database.Driver, "auto_migrate", cfg.AutoMigrate, "serve_ui", cfg.ServeUI)
	if cfg.IsLocal() && resolved.AllowNetwork {
		logger.Warn("local network access enabled: connecting clients have local administrator access")
	}
	return server, ln, cleanup, nil
}

// prepareDatabase is shared with offline administrator maintenance. It performs
// schema preparation only; callers explicitly choose initialization and secrets.
func prepareDatabase(ctx context.Context, cfg *config.ServerConfig) (*sql.DB, *ent.Client, func() error, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, nil, nil, fmt.Errorf("prepare data directory: %w", err)
	}
	db, client, err := database.Open(ctx, cfg)
	if err != nil {
		return nil, nil, nil, err
	}
	cleanup := func() error { return errors.Join(client.Close(), db.Close()) }
	if cfg.AutoMigrate {
		err = database.WithMigrationLock(ctx, db, cfg.Database.Driver, func(migrationClient *ent.Client) error {
			if err := migrationClient.Schema.Create(ctx); err != nil {
				return err
			}
			return service.MigrateHistoryVisibility(ctx, migrationClient)
		})
		if err != nil {
			_ = cleanup()
			return nil, nil, nil, fmt.Errorf("prepare database schema: %w", err)
		}
	}
	return db, client, cleanup, nil
}

func prepareLocalSecret(path string) (string, error) {
	return config.PrepareLocalSecret(path)
}

func bindListener(ctx context.Context, cfg *config.ServerConfig, allowNetwork bool) (net.Listener, error) {
	host := strings.Trim(cfg.Host, "[]")
	if cfg.IsLocal() && !allowNetwork && strings.EqualFold(host, "localhost") {
		addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("resolve local loopback address: %w", err)
		}
		host = ""
		for _, address := range addresses {
			if address.IP.IsLoopback() {
				host = address.IP.String()
				break
			}
		}
		if host == "" {
			return nil, errors.New("localhost did not resolve to a loopback address")
		}
	}
	attempts := 1
	if cfg.IsLocal() && cfg.Port != 0 {
		attempts = 10
	}
	for i := 0; i < attempts && cfg.Port+i <= 65535; i++ {
		addr := net.JoinHostPort(host, strconv.Itoa(cfg.Port+i))
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			if cfg.IsLocal() && !allowNetwork && !ln.Addr().(*net.TCPAddr).IP.IsLoopback() {
				_ = ln.Close()
				return nil, errors.New("local listener must use a loopback address")
			}
			return ln, nil
		}
		if !cfg.IsLocal() || !isAddressInUse(err) || i+1 == attempts || cfg.Port+i == 65535 {
			return nil, fmt.Errorf("listen on %s: %w", addr, err)
		}
	}
	return nil, errors.New("no available local listening port")
}
