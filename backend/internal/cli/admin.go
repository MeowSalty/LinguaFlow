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
