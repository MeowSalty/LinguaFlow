package cli

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func newAuthenticationRecoveryCmd(rt *appCtx) *cobra.Command {
	group := &cobra.Command{Use: "authentication-recovery", Short: "管理受部署权限保护的本地管理员登录恢复"}
	group.AddCommand(newAuthenticationRecoveryIssueCmd(rt), newAuthenticationRecoveryRestoreCmd(rt), newAuthenticationRecoveryRevokeCmd(rt))
	return group
}

func newAuthenticationRecoveryIssueCmd(rt *appCtx) *cobra.Command {
	var username, output string
	cmd := &cobra.Command{
		Use: "issue", Args: cobra.NoArgs,
		Short: "为活跃管理员签发短期单次本地登录恢复凭据",
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolved, err := resolveDeployment(cmd, rt, config.ModeServer)
			if err != nil {
				return err
			}
			cfg := resolved.Config
			_, client, cleanup, err := prepareDatabase(cmd.Context(), &cfg)
			if err != nil {
				return err
			}
			defer cleanup()
			svc := service.NewDeploymentRecoveryService(client)
			issued, err := svc.Issue(cmd.Context(), service.DeploymentRecoveryIssueInput{Username: username})
			if err != nil {
				return err
			}
			if err := writeAuthenticationRecoveryTokenFile(output, issued.Token); err != nil {
				_ = svc.Revoke(cmd.Context(), issued.Token)
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Authentication recovery token written.")
			return err
		},
	}
	cmd.Flags().StringVar(&username, "username", "", "目标活跃管理员用户名")
	cmd.Flags().StringVar(&output, "output", "", "恢复凭据私有文件输出路径；拒绝覆盖")
	cmd.Flags().String("data-dir", "", "覆盖部署数据目录")
	cmd.Flags().Bool("auto-migrate", true, "是否准备数据库 schema")
	_ = cmd.MarkFlagRequired("username")
	_ = cmd.MarkFlagRequired("output")
	return cmd
}

func newAuthenticationRecoveryRestoreCmd(rt *appCtx) *cobra.Command {
	var tokenFile, passwordFile string
	var passwordStdin bool
	cmd := &cobra.Command{
		Use: "restore-local-login", Args: cobra.NoArgs,
		Short: "恢复全站密码登录和用户名定位、关闭本地注册，并重置指定活跃管理员密码",
		RunE: func(cmd *cobra.Command, _ []string) error {
			token, err := readAuthenticationRecoveryTokenFile(tokenFile)
			if err != nil {
				return err
			}
			password, err := readAdministratorPassword(cmd.InOrStdin(), passwordFile, passwordStdin)
			if err != nil {
				return err
			}
			resolved, err := resolveDeployment(cmd, rt, config.ModeServer)
			if err != nil {
				return err
			}
			cfg := resolved.Config
			_, client, cleanup, err := prepareDatabase(cmd.Context(), &cfg)
			if err != nil {
				return err
			}
			defer cleanup()
			svc := service.NewDeploymentRecoveryService(client)
			if err := svc.RestoreLocalLogin(cmd.Context(), service.DeploymentRecoveryRestoreInput{Token: token, Password: password}); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Restored site-wide password login and username login; disabled local signup; reset target administrator password.")
			return err
		},
	}
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "私有恢复凭据文件")
	cmd.Flags().StringVar(&passwordFile, "password-file", "", "从文件读取新密码，移除一个末尾换行")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "从标准输入读取新密码")
	cmd.Flags().String("data-dir", "", "覆盖部署数据目录")
	cmd.Flags().Bool("auto-migrate", true, "是否准备数据库 schema")
	cmd.MarkFlagsMutuallyExclusive("password-file", "password-stdin")
	_ = cmd.MarkFlagRequired("token-file")
	cmd.MarkFlagsOneRequired("password-file", "password-stdin")
	return cmd
}

func newAuthenticationRecoveryRevokeCmd(rt *appCtx) *cobra.Command {
	var tokenFile string
	cmd := &cobra.Command{
		Use: "revoke", Args: cobra.NoArgs,
		Short: "撤销未使用的本地登录恢复凭据",
		RunE: func(cmd *cobra.Command, _ []string) error {
			token, err := readAuthenticationRecoveryTokenFile(tokenFile)
			if err != nil {
				return err
			}
			resolved, err := resolveDeployment(cmd, rt, config.ModeServer)
			if err != nil {
				return err
			}
			cfg := resolved.Config
			_, client, cleanup, err := prepareDatabase(cmd.Context(), &cfg)
			if err != nil {
				return err
			}
			defer cleanup()
			svc := service.NewDeploymentRecoveryService(client)
			if err := svc.Revoke(cmd.Context(), token); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Authentication recovery token revoked.")
			return err
		},
	}
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "私有恢复凭据文件")
	cmd.Flags().String("data-dir", "", "覆盖部署数据目录")
	cmd.Flags().Bool("auto-migrate", true, "是否准备数据库 schema")
	_ = cmd.MarkFlagRequired("token-file")
	return cmd
}

func readAuthenticationRecoveryTokenFile(filename string) ([]byte, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, errors.New("authentication recovery token file could not be read")
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return nil, errors.New("authentication recovery token file is empty")
	}
	token, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(token) != 32 {
		return nil, errors.New("authentication recovery token file is malformed")
	}
	return token, nil
}

func writeAuthenticationRecoveryTokenFile(path string, token []byte) error {
	published, err := credential.PublishPrivateFile(path, []byte(base64.RawURLEncoding.EncodeToString(token)+"\n"))
	if err != nil {
		return fmt.Errorf("publish authentication recovery token file: %w", err)
	}
	if !published {
		return errors.New("authentication recovery token output already exists; choose a new --output path")
	}
	return nil
}
