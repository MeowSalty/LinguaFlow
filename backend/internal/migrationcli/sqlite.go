package migrationcli

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/v013"
)

func validateLocalMigrationEnvironment(environment map[string]string) error {
	for _, name := range []string{"LINGUAFLOW_SERVER_CONFIG", "LINGUAFLOW_CREDENTIALS_KEYRING_FILE", "LINGUAFLOW_CREDENTIALS_MASTER_KEY", "LINGUAFLOW_CREDENTIALS_MASTER_KEY_FILE", "LINGUAFLOW_JWT_SECRET", "LINGUAFLOW_JWT_SECRET_FILE"} {
		if _, present := environment[name]; present {
			return fmt.Errorf("local migration uses only keys inside --data-dir; unset %s", name)
		}
	}
	return nil
}

func newSQLiteCommand() *cobra.Command {
	var apply bool
	var dataDir, mode, keyringFile string
	cmd := &cobra.Command{
		Use: "sqlite", Args: cobra.NoArgs,
		Short: "迁移 SQLite 数据目录，显式选择 serve 或 local 模式",
		Long: `先停止旧服务，并保留独立备份。数据库须为 --data-dir 内的 linguaflow.db。
必须显式指定 --mode serve 或 --mode local；数据库位于本机不代表 local 模式。
serve 保留原账户、角色和服务端初始化语义，不要求名为 local 的管理员。
serve 支持环境变量中的凭据主密钥、keyring 和 JWT；不读取 .env 或服务配置文件。
local 沿用目录内密钥及原 local 管理员规则，等同于 v013 local。
默认仅在私有副本中预演，--apply 才发布并完整保留原目录备份。
中断恢复必须使用相同模式和密钥配置。`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(dataDir) == "" {
				return errors.New("--data-dir is required and must not be empty")
			}
			if mode != "serve" && mode != "local" {
				return errors.New("--mode must be serve or local; SQLite storage does not determine the runtime mode")
			}
			absolute, err := filepath.Abs(dataDir)
			if err != nil {
				return err
			}
			environment := captureEnvironment()
			options := v013.SQLiteOptions{DataDir: absolute, Apply: apply, Mode: config.ModeLocal}
			var jwtFile string
			if mode == "local" {
				if cmd.Flags().Changed("keyring-file") {
					return errors.New("--keyring-file applies only to --mode serve; local uses keys inside --data-dir")
				}
				if err := validateLocalMigrationEnvironment(environment); err != nil {
					return err
				}
			} else {
				options.Mode = config.ModeServer
				if driver, present := environment["LINGUAFLOW_DATABASE_DRIVER"]; present && driver != config.DatabaseDriverSQLite {
					return errors.New("SQLite migration requires LINGUAFLOW_DATABASE_DRIVER=sqlite or an unset driver")
				}
				for _, name := range []string{"LINGUAFLOW_SERVER_CONFIG", "LINGUAFLOW_DATABASE_DSN", "LINGUAFLOW_DATABASE_DSN_FILE"} {
					if _, present := environment[name]; present {
						return fmt.Errorf("SQLite migration uses --data-dir and explicit environment secrets; unset %s", name)
					}
				}
				workingDir, err := filepath.Abs(".")
				if err != nil {
					return err
				}
				resolveReadPath := func(path string) (string, error) {
					return v013.ResolveSQLiteInputPath(absolute, path)
				}
				for _, name := range []string{"LINGUAFLOW_CREDENTIALS_MASTER_KEY_FILE", "LINGUAFLOW_JWT_SECRET_FILE"} {
					if path, present := environment[name]; present && strings.TrimSpace(path) != "" {
						if !filepath.IsAbs(path) {
							path = filepath.Join(workingDir, path)
						}
						resolved, err := resolveReadPath(path)
						if err != nil {
							return fmt.Errorf("resolve %s for SQLite migration: %w", name, err)
						}
						environment[name] = resolved
					}
				}
				options.Keys, options.KeyringFile, options.PendingKeyring, err = serveMigrationKeyring(environment, workingDir, absolute, keyringFile, cmd.Flags().Changed("keyring-file"), resolveReadPath)
				if err != nil {
					return err
				}
				if options.KeyringFile != "" {
					options.KeyringFile = filepath.Clean(options.KeyringFile)
				}
				var explicitJWT bool
				options.JWTSecret, explicitJWT, err = config.EnvironmentSecret(environment, "LINGUAFLOW_JWT_SECRET", workingDir)
				if err != nil {
					return err
				}
				if explicitJWT && len(options.JWTSecret) < 32 {
					return errors.New("LINGUAFLOW_JWT_SECRET must contain at least 32 bytes")
				}
				if !explicitJWT {
					jwtFile = filepath.Join(absolute, "jwt-secret")
				}
			}
			report, err := v013.RunSQLite(cmd.Context(), options)
			if err != nil {
				return fmt.Errorf("SQLite %s migration did not complete; retain the same mode, credential key and any generated key files for retry: %w", mode, err)
			}
			if report.Recovery == "restored" {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "Interrupted directory switch restored. Run the migration again with the same mode and credential configuration.")
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
			if !report.Applied {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "Run the same command with --apply to publish. Continue using the same mode and credential configuration.")
				return err
			}
			if mode == "serve" {
				return printServeStartup(cmd, config.DatabaseDriverSQLite, absolute, options.KeyringFile, jwtFile)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Start local with LINGUAFLOW_DATA_DIR=%s\n", absolute)
			return err
		},
	}
	cmd.Flags().StringVar(&mode, "mode", "", "原运行模式：serve 或 local（必填，不自动猜测）")
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "原 SQLite 数据目录（必填）")
	cmd.Flags().StringVar(&keyringFile, "keyring-file", "", "serve 模式使用的 keyring；默认 data-dir/credentials-keyring.json")
	cmd.Flags().BoolVar(&apply, "apply", false, "发布迁移目录并保留原目录备份")
	_ = cmd.MarkFlagRequired("mode")
	_ = cmd.MarkFlagRequired("data-dir")
	return cmd
}
