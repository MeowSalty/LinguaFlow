package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func newAdminCmd(rt *appCtx) *cobra.Command {
	group := &cobra.Command{Use: "admin", Short: "显式初始化及维护管理员账户（需要部署数据库访问权限）"}
	for _, action := range []string{"initialize", "create", "recover"} {
		var username, email, passwordFile string
		var passwordStdin bool
		cmd := &cobra.Command{
			Use: action, Args: cobra.NoArgs,
			Short: map[string]string{"initialize": "初始化空实例；已初始化实例只验证状态", "create": "为已初始化 serve 实例创建管理员", "recover": "恢复已有账户为活跃管理员并重置密码"}[action],
			RunE: func(cmd *cobra.Command, _ []string) error {
				resolved, err := resolveDeployment(cmd, rt, config.ModeServer)
				if err != nil {
					return err
				}
				var password string
				explicitIdentity := cmd.Flags().Changed("username") || cmd.Flags().Changed("email") || cmd.Flags().Changed("password-file") || cmd.Flags().Changed("password-stdin")
				if action != "initialize" || explicitIdentity {
					if username == "" {
						return errors.New("--username is required")
					}
					if action != "recover" && email == "" {
						return errors.New("--email is required")
					}
					password, err = readAdministratorPassword(cmd.InOrStdin(), passwordFile, passwordStdin)
					if err != nil {
						return err
					}
				}
				cfg := resolved.Config
				_, client, cleanup, err := prepareDatabase(cmd.Context(), &cfg)
				if err != nil {
					return err
				}
				defer cleanup()
				svc := service.NewInitializationService(client)
				if action == "initialize" {
					bootstrap := resolved.Bootstrap
					if explicitIdentity {
						bootstrap.Admin = &config.BootstrapAdmin{Username: username, Email: email, Password: password}
					}
					if _, err := svc.Initialize(cmd.Context(), config.ModeServer, bootstrap); err != nil {
						return err
					}
					_, err = fmt.Fprintln(cmd.OutOrStdout(), "Instance initialization verified.")
					return err
				}
				account, err := svc.MaintainAdministrator(cmd.Context(), service.AdminCreateUserInput{Username: username, Email: email, Password: password}, action == "recover")
				if err != nil {
					return err
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Administrator %s completed (user ID %d).\n", action, account.ID)
				return err
			},
		}
		cmd.Flags().StringVar(&username, "username", "", "管理员用户名")
		cmd.Flags().StringVar(&email, "email", "", "管理员邮箱（创建时必填）")
		cmd.Flags().StringVar(&passwordFile, "password-file", "", "从文件读取密码，移除一个末尾换行")
		cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "从标准输入读取密码")
		cmd.Flags().String("data-dir", "", "覆盖部署数据目录")
		cmd.Flags().Bool("auto-migrate", true, "是否准备数据库 schema")
		cmd.MarkFlagsMutuallyExclusive("password-file", "password-stdin")
		group.AddCommand(cmd)
	}
	group.AddCommand(newAdminCredentialsCmd(rt))
	return group
}

func newAdminCredentialsCmd(rt *appCtx) *cobra.Command {
	group := &cobra.Command{Use: "credentials", Short: "维护持久化凭据的部署加密"}
	mode := "serve"
	cmd := &cobra.Command{
		Use: "reencrypt", Args: cobra.NoArgs,
		Short: "将 LLM 与存储授权逐行重新加密到部署 keyring 的 active key",
		Long: `先把新 key 加入 keyring、切换 active_key_id，并重启使用该数据库的服务进程，再执行本命令。
命令使用相同部署 keyring，逐行提交重新加密，可重复执行或在失败后继续。
它不改变凭据版本、撤销状态或任务引用，不删除 key；请保留恢复当前数据和历史备份需要的旧 key。
本命令直接使用部署数据库权限，不启动 HTTP 服务。`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if mode != "serve" && mode != "local" {
				return errors.New("mode must be serve or local")
			}
			resolved, err := resolveDeployment(cmd, rt, mode)
			if err != nil {
				return err
			}
			cfg := resolved.Config
			_, client, cleanup, err := prepareDatabase(cmd.Context(), &cfg)
			if err != nil {
				return err
			}
			defer cleanup()
			if _, err := service.NewInitializationService(client).Validate(cmd.Context(), cfg.Mode); err != nil {
				return err
			}
			keys := resolved.CredentialKeys
			if keys == nil {
				return errors.New("existing credential keys are required for re-encryption")
			}
			credentials := service.NewCredentialService(client, keys, nil)
			if err := credentials.ValidateKeys(cmd.Context()); err != nil {
				return err
			}
			count, llmErr := credentials.Reencrypt(cmd.Context())
			storageCredentials := service.NewStorageConnectionService(client, keys, cfg.Storage, nil)
			storageCount, storageFailed, storageErr := storageCredentials.Reencrypt(cmd.Context())
			_, outputErr := fmt.Fprintf(cmd.OutOrStdout(), "Re-encrypted %d credential versions. Existing keys were retained.\nRe-encrypted %d storage authorization versions; %d unavailable or conflicted.\n", count, storageCount, storageFailed)
			if llmErr != nil {
				llmErr = fmt.Errorf("credential re-encryption stopped after %d committed versions: %w", count, llmErr)
			}
			if storageFailed > 0 {
				storageErr = errors.Join(storageErr, fmt.Errorf("storage re-encryption left %d versions requiring attention", storageFailed))
			}
			return errors.Join(llmErr, storageErr, outputErr)
		},
	}
	cmd.Flags().StringVar(&mode, "mode", "serve", "部署模式 serve 或 local")
	cmd.Flags().String("data-dir", "", "覆盖部署数据目录")
	cmd.Flags().Bool("auto-migrate", true, "是否准备数据库 schema")
	cmd.Flags().Bool("allow-network", false, "校验 local 非回环部署的显式许可；命令不监听网络")
	group.AddCommand(cmd)
	return group
}

func readAdministratorPassword(stdin io.Reader, filename string, fromStdin bool) (string, error) {
	if (filename != "") == fromStdin {
		return "", errors.New("provide exactly one of --password-file or --password-stdin")
	}
	var data []byte
	var err error
	if fromStdin {
		data, err = io.ReadAll(io.LimitReader(stdin, 1024))
	} else {
		data, err = os.ReadFile(filename)
	}
	if err != nil {
		return "", errors.New("administrator password input could not be read")
	}
	password := string(data)
	if strings.HasSuffix(password, "\r\n") {
		password = strings.TrimSuffix(password, "\r\n")
	} else {
		password = strings.TrimSuffix(password, "\n")
	}
	if password == "" {
		return "", errors.New("administrator password input is empty")
	}
	return password, nil
}
