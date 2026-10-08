package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
)

func newSecretsCmd() *cobra.Command {
	group := &cobra.Command{
		Use: "secrets", Short: "离线生成部署密钥；不读取部署配置或连接数据库",
	}
	group.AddCommand(newSecretGenerateCmd())
	keyring := &cobra.Command{Use: "keyring", Short: "生成或扩展凭据加密 keyring"}
	keyring.AddCommand(newKeyringWriteCmd(false), newKeyringWriteCmd(true))
	group.AddCommand(keyring)
	return group
}

func newSecretGenerateCmd() *cobra.Command {
	var output string
	var stdout bool
	cmd := &cobra.Command{
		Use: "generate", Args: cobra.NoArgs,
		Short: "生成可用于 JWT 或凭据加密的随机 32 字节 Base64 密钥",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Flags().Changed("output") && strings.TrimSpace(output) == "" {
				return errors.New("--output must name a nonempty file path")
			}
			if stdout == (output != "") {
				return errors.New("provide exactly one of --output or --stdout")
			}
			value, err := credential.GenerateMasterKey()
			if err != nil {
				return err
			}
			if stdout {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), value)
				return err
			}
			if err := publishSecretOutput(output, []byte(value+"\n")); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\n", output)
			return err
		},
	}
	cmd.Flags().StringVar(&output, "output", "", "私有文件输出路径；拒绝覆盖")
	cmd.Flags().BoolVar(&stdout, "stdout", false, "仅向标准输出打印密钥；用于管道或手动保存")
	cmd.MarkFlagsMutuallyExclusive("output", "stdout")
	return cmd
}

func newKeyringWriteCmd(rotate bool) *cobra.Command {
	var input, output string
	var fromEnvironment bool
	cmd := &cobra.Command{
		Use: "init", Args: cobra.NoArgs,
		Short: "生成私有 keyring 文件；拒绝覆盖",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(output) == "" {
				return errors.New("--output must name a nonempty file path")
			}
			var old *credential.Keyring
			if rotate {
				if strings.TrimSpace(input) == "" {
					return errors.New("--input must name a nonempty keyring path")
				}
				var err error
				old, err = credential.LoadKeyring(input)
				if err != nil {
					return fmt.Errorf("load input credential keyring: %w", err)
				}
			}
			value, err := keyringMasterKey(fromEnvironment)
			if err != nil {
				return err
			}
			var keys *credential.Keyring
			if rotate {
				keys, err = old.WithMasterKey(value)
			} else {
				keys, err = credential.FromMasterKey(value)
			}
			if err != nil {
				return err
			}
			data, err := credential.EncodeKeyring(keys)
			if err != nil {
				return err
			}
			if err := publishSecretOutput(output, append(data, '\n')); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s (active key ID: %s)\n", output, keys.ActiveKeyID())
			return err
		},
	}
	if rotate {
		cmd.Use = "rotate"
		cmd.Short = "保留所有旧 key，生成并激活新 key 后写入新文件"
		cmd.Flags().StringVar(&input, "input", "", "原有私有 keyring 文件；保持不变")
		_ = cmd.MarkFlagRequired("input")
	}
	cmd.Flags().StringVar(&output, "output", "", "私有 keyring 输出路径；拒绝覆盖")
	cmd.Flags().BoolVar(&fromEnvironment, "from-master-key-env", false, "使用 LINGUAFLOW_CREDENTIALS_MASTER_KEY 或其 _FILE 环境变量中的现有密钥")
	_ = cmd.MarkFlagRequired("output")
	return cmd
}

func keyringMasterKey(fromEnvironment bool) (string, error) {
	if !fromEnvironment {
		return credential.GenerateMasterKey()
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	value, present, err := config.EnvironmentSecret(config.Environment(), "LINGUAFLOW_CREDENTIALS_MASTER_KEY", cwd)
	if err != nil {
		return "", err
	}
	if !present {
		return "", errors.New("--from-master-key-env requires LINGUAFLOW_CREDENTIALS_MASTER_KEY or LINGUAFLOW_CREDENTIALS_MASTER_KEY_FILE")
	}
	return value, nil
}

func publishSecretOutput(path string, data []byte) error {
	published, err := credential.PublishPrivateFile(path, data)
	if err != nil {
		return fmt.Errorf("publish secret output: %w", err)
	}
	if !published {
		return errors.New("secret output already exists; choose a new --output path")
	}
	return nil
}
