package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/api"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
)

// BootOptions 描述服务器引导的共享参数。
type BootOptions struct {
	Logger    *slog.Logger
	Overrides func(cfg *config.ServerConfig)
	Mode      string // "server" | "local"
}

// bootstrapServer 加载配置、打开数据库、运行迁移、创建监听器，
// 返回准备就绪的 *api.Server 和监听器。调用方负责调用 server.Run(ctx, ln)。
func bootstrapServer(ctx context.Context, opts BootOptions) (*api.Server, net.Listener, func() error, error) {
	cfg, err := config.LoadServerConfig(opts.Mode)
	if err != nil {
		return nil, nil, nil, err
	}

	if opts.Overrides != nil {
		opts.Overrides(cfg)
	}
	if err := config.ValidateServerConfig(cfg); err != nil {
		return nil, nil, nil, err
	}

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, nil, nil, fmt.Errorf("create server data dir %s: %w", cfg.DataDir, err)
	}

	db, client, err := database.Open(ctx, cfg)
	if err != nil {
		return nil, nil, nil, err
	}
	cleanup := func() error {
		clientErr := client.Close()
		dbErr := db.Close()
		if clientErr != nil {
			return clientErr
		}
		return dbErr
	}

	if cfg.AutoMigrate {
		unlockMigration, err := database.AcquireMigrationLock(ctx, db, cfg.Database.Driver)
		if err != nil {
			_ = cleanup()
			return nil, nil, nil, err
		}
		migrationErr := client.Schema.Create(ctx)
		unlockErr := unlockMigration()
		if err := errors.Join(migrationErr, unlockErr); err != nil {
			_ = cleanup()
			return nil, nil, nil, fmt.Errorf("run ent schema migration: %w", err)
		}
	}

	var localUser *ent.User
	if cfg.IsLocal() {
		localUser, err = ensureLocalUser(ctx, client)
		if err != nil {
			_ = cleanup()
			return nil, nil, nil, fmt.Errorf("ensure local user: %w", err)
		}
	}

	if err := ensureAdminUser(ctx, client, opts.Logger); err != nil {
		_ = cleanup()
		return nil, nil, nil, fmt.Errorf("ensure admin user: %w", err)
	}

	ln, err := net.Listen("tcp", cfg.Address())
	if err != nil {
		_ = cleanup()
		return nil, nil, nil, fmt.Errorf("listen on %s: %w", cfg.Address(), err)
	}

	// 回写实际端口（当端口为 0 时由 OS 分配）。
	actualPort := ln.Addr().(*net.TCPAddr).Port
	cfg.Port = actualPort

	logArgs := []any{
		"mode", cfg.Mode,
		"addr", ln.Addr().String(),
		"database_driver", cfg.Database.Driver,
		"database_max_open_conns", cfg.Database.MaxOpenConns,
		"database_max_idle_conns", cfg.Database.MaxIdleConns,
		"database_conn_max_lifetime", cfg.Database.ConnMaxLifetime,
		"auto_migrate", cfg.AutoMigrate,
	}
	if cfg.Database.Driver == config.DatabaseDriverSQLite && cfg.Database.DSN == "" {
		logArgs = append(logArgs, "database_path", cfg.DatabasePath())
	}
	opts.Logger.Info("server bootstrapped", logArgs...)

	server, err := api.NewServer(cfg, opts.Logger, db, client, cfg.Mode, localUser)
	if err != nil {
		_ = ln.Close()
		_ = cleanup()
		return nil, nil, nil, err
	}

	return server, ln, cleanup, nil
}

// ensureLocalUser 创建本地用户（如果不存在）。
// 本地用户使用随机 bcrypt 哈希作为密码，确保在 serve 模式下无法通过密码登录。
func ensureLocalUser(ctx context.Context, client *ent.Client) (*ent.User, error) {
	existing, err := client.User.Query().Where(user.UsernameEQ("local")).Only(ctx)
	if err == nil {
		return existing, nil
	}
	if !ent.IsNotFound(err) {
		return nil, fmt.Errorf("query local user: %w", err)
	}

	randomHash, err := randomBcryptHash()
	if err != nil {
		return nil, fmt.Errorf("generate random password hash: %w", err)
	}

	u, err := client.User.Create().
		SetUsername("local").
		SetPasswordHash(randomHash).
		SetEmail("local@linguaflow.local").
		SetRole("admin").
		SetActive(true).
		Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			// 竞态条件：另一个进程先创建了该用户。
			existing, err2 := client.User.Query().Where(user.UsernameEQ("local")).Only(ctx)
			if err2 != nil {
				return nil, fmt.Errorf("query local user after constraint error: %w", err2)
			}
			return existing, nil
		}
		return nil, fmt.Errorf("create local user: %w", err)
	}
	return u, nil
}

// randomBcryptHash 从随机字节生成 bcrypt 哈希。
// 生成的哈希无法被任何明文密码匹配。
func randomBcryptHash() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	randomStr := hex.EncodeToString(buf)
	hash, err := bcrypt.GenerateFromPassword([]byte(randomStr), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// ensureAdminUser 根据环境变量创建或确保管理员用户。
// 优先级：LINGUAFLOW_ADMIN_USERNAME 环境变量方案。
// 用户已存在时不会修改密码，只会确保 role 为 admin。
func ensureAdminUser(ctx context.Context, client *ent.Client, logger *slog.Logger) error {
	adminUsername := os.Getenv("LINGUAFLOW_ADMIN_USERNAME")
	if adminUsername == "" {
		return nil
	}

	adminUsername = strings.ToLower(strings.TrimSpace(adminUsername))

	existing, err := client.User.Query().Where(user.UsernameEQ(adminUsername)).Only(ctx)
	if err == nil {
		if existing.Role != "admin" {
			if err := client.User.UpdateOneID(existing.ID).SetRole("admin").Exec(ctx); err != nil {
				return fmt.Errorf("ensure admin role: %w", err)
			}
			logger.Info("upgraded user role to admin", "username", adminUsername)
		}
		return nil
	}
	if !ent.IsNotFound(err) {
		return fmt.Errorf("query admin user: %w", err)
	}

	adminPassword := os.Getenv("LINGUAFLOW_ADMIN_PASSWORD")
	if adminPassword == "" {
		logger.Warn("LINGUAFLOW_ADMIN_USERNAME is set but user does not exist and LINGUAFLOW_ADMIN_PASSWORD is not set, skipping admin creation",
			"username", adminUsername)
		return nil
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(adminPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash admin password: %w", err)
	}

	u, err := client.User.Create().
		SetUsername(adminUsername).
		SetPasswordHash(string(passwordHash)).
		SetEmail(adminUsername + "@admin.local").
		SetRole("admin").
		SetActive(true).
		Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			existing, err2 := client.User.Query().Where(user.UsernameEQ(adminUsername)).Only(ctx)
			if err2 != nil {
				return fmt.Errorf("query admin user after constraint error: %w", err2)
			}
			if existing.Role != "admin" {
				if err := client.User.UpdateOneID(existing.ID).SetRole("admin").Exec(ctx); err != nil {
					return fmt.Errorf("ensure admin role: %w", err)
				}
			}
			return nil
		}
		return fmt.Errorf("create admin user: %w", err)
	}

	logger.Info("created admin user from environment variables", "username", u.Username)
	return nil
}
