// Package migrationcli assembles the separately distributed migration command.
// The normal service executable must not import this package.
package migrationcli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/v013"
)

// NewCommand constructs a fresh command tree. Environment values are captured
// only when a migration command runs, so help never opens a database or file.
func NewCommand() *cobra.Command {
	root := &cobra.Command{
		Use: "linguaflow-migrate", Short: "LinguaFlow 一次性旧版本迁移工具",
		SilenceUsage: true, SilenceErrors: true,
	}
	version := &cobra.Command{Use: "v013", Short: "迁移 v0.13.0 数据，默认预演"}
	version.AddCommand(newPostgresCommand(), newSQLiteCommand(), newLocalCommand())
	root.AddCommand(version)
	return root
}

func newPostgresCommand() *cobra.Command {
	var apply bool
	var dataDir, keyringFile string
	cmd := &cobra.Command{
		Use: "postgres", Args: cobra.NoArgs,
		Short: "迁移 PostgreSQL serve 实例，默认在事务中预演并回滚",
		Long: `先停止旧服务，并保留 PostgreSQL 备份或托管快照。
从 LINGUAFLOW_DATABASE_DSN 或 LINGUAFLOW_DATABASE_DSN_FILE 读取连接配置。
默认预演 schema、凭据、实例和历史任务转换，然后回滚；预演持锁且可能消耗序列值。
凭据支持 LINGUAFLOW_CREDENTIALS_MASTER_KEY 或其 _FILE，也可使用 keyring 文件。
显式 --apply 才提交；未提供主密钥时准备 keyring，缺少的密钥文件在提交前发布。
失败后保留已发布的密钥以便重试，不重置用户密码和角色。
不读取服务配置文件或 .env，只支持 v0.13.0 PostgreSQL serve 实例。`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			environment := captureEnvironment()
			if environment["LINGUAFLOW_SERVER_CONFIG"] != "" {
				return errors.New("migration uses explicit flags and environment variables; unset LINGUAFLOW_SERVER_CONFIG")
			}
			if driver := environment["LINGUAFLOW_DATABASE_DRIVER"]; driver != "" && driver != config.DatabaseDriverPostgres {
				return errors.New("this command only supports PostgreSQL; LINGUAFLOW_DATABASE_DRIVER must be postgres")
			}
			workingDir, err := os.Getwd()
			if err != nil {
				return err
			}
			dsn, _, err := config.EnvironmentSecret(environment, "LINGUAFLOW_DATABASE_DSN", workingDir)
			if err != nil {
				return err
			}
			if strings.TrimSpace(dsn) == "" {
				return errors.New("LINGUAFLOW_DATABASE_DSN or LINGUAFLOW_DATABASE_DSN_FILE is required")
			}
			jwtSecret, explicitJWT, err := config.EnvironmentSecret(environment, "LINGUAFLOW_JWT_SECRET", workingDir)
			if err != nil {
				return err
			}
			if explicitJWT && len(jwtSecret) < 32 {
				return errors.New("LINGUAFLOW_JWT_SECRET must contain at least 32 bytes")
			}
			if dataDir == "" {
				dataDir = environment["LINGUAFLOW_DATA_DIR"]
				if dataDir == "" {
					dataDir = "./data"
				}
			}
			dataDir, err = filepath.Abs(dataDir)
			if err != nil {
				return err
			}
			keys, keyringPath, pendingKeyring, err := postgresKeyring(environment, workingDir, dataDir, keyringFile, cmd.Flags().Changed("keyring-file"))
			if err != nil {
				return err
			}
			jwtFile := ""
			if !explicitJWT {
				jwtFile = filepath.Join(dataDir, "jwt-secret")
				if _, err := config.ReadLocalSecret(jwtFile); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
			cfg := config.DefaultServerConfig()
			cfg.Database = config.DatabaseConfig{Driver: config.DatabaseDriverPostgres, DSN: dsn, MaxOpenConns: 1, MaxIdleConns: 1}
			db, client, err := database.Open(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			defer client.Close()
			beforeCommit := func() error {
				if len(pendingKeyring) != 0 {
					published, err := credential.PublishPrivateFile(keyringPath, pendingKeyring)
					if err != nil {
						return fmt.Errorf("persist migration keyring: %w", err)
					}
					if !published {
						return errors.New("migration keyring appeared concurrently; rerun to load it")
					}
					if _, err := credential.LoadKeyring(keyringPath); err != nil {
						return fmt.Errorf("verify migration keyring: %w", err)
					}
				}
				if jwtFile != "" {
					if _, err := config.PrepareLocalSecret(jwtFile); err != nil {
						return err
					}
				}
				return nil
			}
			report, err := v013.RunPostgres(cmd.Context(), db, keys, apply, beforeCommit)
			if err != nil {
				return fmt.Errorf("v0.13.0 migration did not complete; retain the same credential key and any generated key files for retry: %w", err)
			}
			message := "Rehearsal completed; database changes rolled back. No key files created."
			if apply {
				message = "Migration committed. Retain the keyring with your database backups."
				if keyringPath == "" {
					message = "Migration committed. Retain the credential master key with your database backups."
				}
			}
			if err := printReport(cmd, message, report); err != nil {
				return err
			}
			if !apply {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "Run the same command with --apply to commit.")
				return err
			}
			return printPostgresStartup(cmd, dataDir, keyringPath, jwtFile)
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "提交迁移（默认预演并回滚）")
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "原数据目录；默认 LINGUAFLOW_DATA_DIR 或 ./data")
	cmd.Flags().StringVar(&keyringFile, "keyring-file", "", "复用或创建 keyring；默认 LINGUAFLOW_CREDENTIALS_KEYRING_FILE 或 data-dir/credentials-keyring.json")
	return cmd
}

func newLocalCommand() *cobra.Command {
	var apply bool
	var dataDir string
	cmd := &cobra.Command{
		Use: "local", Args: cobra.NoArgs,
		Short: "在副本中迁移 SQLite local 数据目录，保留原目录备份",
		Long: `先停止使用该目录的全部 LinguaFlow 进程，并保留独立备份。
必须显式指定 --data-dir；只支持其中的标准 SQLite 数据库和目录内密钥。
默认复制并验证后删除临时副本；--apply 将旧目录保留为备份并把迁移副本放回原路径。
切换需要同卷本地普通目录，拒绝链接、junction、外部资源路径和特殊文件。
中断后以相同参数重跑可识别已发布或可恢复状态；不会覆盖已有数据。`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(dataDir) == "" {
				return errors.New("--data-dir is required and must not be empty")
			}
			environment := captureEnvironment()
			if err := validateLocalMigrationEnvironment(environment); err != nil {
				return err
			}
			report, err := v013.RunLocal(cmd.Context(), v013.LocalOptions{DataDir: dataDir, Apply: apply})
			if err != nil {
				return err
			}
			if report.Recovery == "restored" {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "Interrupted directory switch restored. Run the migration again to create a new converted copy.")
				return err
			}
			message := "Rehearsal completed; original directory retained and temporary copy removed."
			if report.Applied {
				message = "Migration published at the original data directory."
			}
			if report.Recovery == "published" {
				message = "Previously published migration verified; no conversion repeated."
			}
			if err := printReport(cmd, message, report.Report); err != nil {
				return err
			}
			if report.BackupDir != "" {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Original directory backup: %s\n", report.BackupDir); err != nil {
					return err
				}
			}
			if report.Applied {
				absolute, err := filepath.Abs(dataDir)
				if err != nil {
					return err
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Start local with LINGUAFLOW_DATA_DIR=%s\n", absolute)
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Run the same command with --apply to publish the converted directory.")
			return err
		},
	}
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "原 local 数据目录（必填）")
	cmd.Flags().BoolVar(&apply, "apply", false, "发布迁移后的目录并保留旧目录备份")
	_ = cmd.MarkFlagRequired("data-dir")
	return cmd
}

func printReport(cmd *cobra.Command, message string, report v013.Report) error {
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\nBackends: %d; without secret: %d; profiles: %d; jobs: %d; encrypted credentials: %d; obsolete settings removed: %d.\n", message,
		report.BackendsMigrated, report.BackendsWithoutSecret, report.Profiles, report.Jobs, report.CredentialsCreated, report.SettingsRemoved)
	return err
}

func captureEnvironment() map[string]string {
	environment := make(map[string]string)
	for _, item := range os.Environ() {
		if key, value, ok := strings.Cut(item, "="); ok {
			environment[key] = value
		}
	}
	return environment
}

// An explicitly supplied master key is consumed directly and must never cause
// the migration to load or generate a second credential key in the data dir.
func postgresKeyring(environment map[string]string, workingDir, dataDir, keyringFlag string, flagSet bool) (*credential.Keyring, string, []byte, error) {
	return serveMigrationKeyring(environment, workingDir, dataDir, keyringFlag, flagSet, nil)
}

// resolveReadPath lets SQLite recover inputs from its validated staging copy
// while retaining the original configured path for publication and startup.
func serveMigrationKeyring(environment map[string]string, workingDir, dataDir, keyringFlag string, flagSet bool, resolveReadPath func(string) (string, error)) (*credential.Keyring, string, []byte, error) {
	keys, masterPresent, err := config.EnvironmentMasterKey(environment, workingDir)
	if err != nil {
		return nil, "", nil, err
	}
	environmentPath, environmentSet := environment["LINGUAFLOW_CREDENTIALS_KEYRING_FILE"]
	if masterPresent {
		if flagSet || environmentSet {
			return nil, "", nil, errors.New("credential master key conflicts with --keyring-file or LINGUAFLOW_CREDENTIALS_KEYRING_FILE; use only one credential key source")
		}
		return keys, "", nil, nil
	}
	path := filepath.Join(dataDir, "credentials-keyring.json")
	if flagSet {
		if strings.TrimSpace(keyringFlag) == "" {
			return nil, "", nil, errors.New("--keyring-file must not be empty")
		}
		path = keyringFlag
	} else if environmentSet {
		if strings.TrimSpace(environmentPath) == "" {
			return nil, "", nil, errors.New("LINGUAFLOW_CREDENTIALS_KEYRING_FILE must not be empty")
		}
		path = environmentPath
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(workingDir, path)
	}
	readPath := path
	if resolveReadPath != nil {
		readPath, err = resolveReadPath(path)
		if err != nil {
			return nil, "", nil, err
		}
	}
	keys, pending, err := migrationKeyring(readPath)
	return keys, path, pending, err
}

func printPostgresStartup(cmd *cobra.Command, dataDir, keyringFile, jwtFile string) error {
	return printServeStartup(cmd, config.DatabaseDriverPostgres, dataDir, keyringFile, jwtFile)
}

func printServeStartup(cmd *cobra.Command, driver, dataDir, keyringFile, jwtFile string) error {
	if _, err := fmt.Fprintln(cmd.OutOrStdout(), "Before the first serve startup, explicitly choose both LINGUAFLOW_STORAGE_INITIALIZATION_CAPACITY_BYTES and LINGUAFLOW_STORAGE_INITIALIZATION_LOGICAL_LIMIT_BYTES: each must be a positive integer in bytes (at most 9007199254740991), or the lowercase value null for unlimited. Missing choices fail startup; existing persisted quotas are preserved."); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Start serve with the same database connection and data directory:\nLINGUAFLOW_DATABASE_DRIVER=%s\nLINGUAFLOW_DATA_DIR=%s\n", driver, dataDir); err != nil {
		return err
	}
	if keyringFile == "" {
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), "Continue supplying the same LINGUAFLOW_CREDENTIALS_MASTER_KEY or LINGUAFLOW_CREDENTIALS_MASTER_KEY_FILE to serve. No credential keyring file was created."); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintf(cmd.OutOrStdout(), "LINGUAFLOW_CREDENTIALS_KEYRING_FILE=%s\n", keyringFile); err != nil {
		return err
	}
	if jwtFile != "" {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "LINGUAFLOW_JWT_SECRET_FILE=%s\n", jwtFile)
		return err
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), "Continue supplying the same LINGUAFLOW_JWT_SECRET or LINGUAFLOW_JWT_SECRET_FILE to serve.")
	return err
}

// Rehearsals use an ephemeral key; apply publishes these exact bytes only after
// all database conversion and validation has succeeded.
func migrationKeyring(path string) (*credential.Keyring, []byte, error) {
	keys, err := credential.LoadKeyring(path)
	if err == nil {
		return keys, nil, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("load migration keyring: %w", err)
	}
	return credential.GenerateKeyring()
}
