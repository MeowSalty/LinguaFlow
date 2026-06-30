package cli

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"os"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"

	"github.com/MeowSalty/LinguaFlow/backend/internal/api"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
)

// BootOptions 描述服务器引导的共享参数。
type BootOptions struct {
	ConfigPath string
	Logger     *slog.Logger
	Overrides  func(cfg *config.Config)
	Mode       string // "server" | "local"
}

// bootstrapServer 加载配置、打开数据库、运行迁移、创建监听器，
// 返回准备就绪的 *api.Server 和监听器。调用方负责调用 server.Run(ctx, ln)。
func bootstrapServer(ctx context.Context, opts BootOptions) (*api.Server, net.Listener, func() error, error) {
	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		return nil, nil, nil, err
	}

	cfg.Server.Mode = opts.Mode
	if opts.Overrides != nil {
		opts.Overrides(cfg)
	}

	if err := os.MkdirAll(cfg.Server.DataDir, 0o755); err != nil {
		return nil, nil, nil, fmt.Errorf("create server data dir %s: %w", cfg.Server.DataDir, err)
	}

	dbPath := cfg.Server.DatabasePath()
	dbDSN := cfg.Server.DatabaseDSN()
	db, err := sql.Open("sqlite", dbDSN)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open sqlite database %s: %w", dbPath, err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, nil, nil, fmt.Errorf("ping sqlite database %s: %w", dbPath, err)
	}

	driver := entsql.OpenDB(dialect.SQLite, db)
	client := ent.NewClient(ent.Driver(driver))

	if cfg.Server.AutoMigrate {
		if err := client.Schema.Create(ctx); err != nil {
			_ = client.Close()
			_ = db.Close()
			return nil, nil, nil, fmt.Errorf("run ent schema migration: %w", err)
		}
	}

	var localUser *ent.User
	if cfg.Server.IsLocal() {
		localUser, err = ensureLocalUser(ctx, client)
		if err != nil {
			_ = client.Close()
			_ = db.Close()
			return nil, nil, nil, fmt.Errorf("ensure local user: %w", err)
		}
	}

	ln, err := net.Listen("tcp", cfg.Server.Address())
	if err != nil {
		_ = client.Close()
		_ = db.Close()
		return nil, nil, nil, fmt.Errorf("listen on %s: %w", cfg.Server.Address(), err)
	}

	// 回写实际端口（当端口为 0 时由 OS 分配）。
	actualPort := ln.Addr().(*net.TCPAddr).Port
	cfg.Server.Port = actualPort

	opts.Logger.Info("server bootstrapped",
		"mode", cfg.Server.Mode,
		"addr", ln.Addr().String(),
		"database_path", dbPath,
		"auto_migrate", cfg.Server.AutoMigrate)

	server, err := api.NewServer(cfg, opts.Logger, db, client, cfg.Server.Mode, localUser)
	if err != nil {
		_ = ln.Close()
		_ = client.Close()
		_ = db.Close()
		return nil, nil, nil, err
	}

	cleanup := func() error {
		_ = client.Close()
		return db.Close()
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
