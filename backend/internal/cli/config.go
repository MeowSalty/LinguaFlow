package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/spf13/cobra"
)

func addDeploymentFlags(cmd *cobra.Command, local bool) {
	cmd.Flags().String("host", "", "覆盖 server.host")
	cmd.Flags().Int("port", 0, "覆盖请求端口（local 可用 0 自动分配）")
	cmd.Flags().String("data-dir", "", "覆盖 server.data_dir")
	cmd.Flags().Bool("auto-migrate", true, "覆盖 server.auto_migrate")
	cmd.Flags().Bool("no-ui", false, "关闭嵌入式 Web UI")
	if local {
		cmd.Flags().Bool("allow-network", false, "允许非回环监听；可连接者拥有本地管理员权限")
	}
}

func resolveDeployment(cmd *cobra.Command, rt *appCtx, mode string) (*config.ResolvedServer, error) {
	in := config.ServerInputs{Mode: mode, Environment: config.Environment(), Overrides: map[string]any{}, OverrideSources: map[string]string{}}
	if cmd.Flags().Changed("config") {
		in.ConfigPath = &rt.configPath
	}
	for _, name := range []string{"host", "data-dir"} {
		if cmd.Flags().Changed(name) {
			v, _ := cmd.Flags().GetString(name)
			key := "server." + name
			if name == "data-dir" {
				key = "server.data_dir"
			}
			in.Overrides[key] = v
		}
	}
	if cmd.Flags().Changed("port") {
		v, _ := cmd.Flags().GetInt("port")
		in.Overrides["server.port"] = v
	}
	if cmd.Flags().Changed("auto-migrate") {
		v, _ := cmd.Flags().GetBool("auto-migrate")
		in.Overrides["server.auto_migrate"] = v
	}
	if cmd.Flags().Changed("no-ui") {
		v, _ := cmd.Flags().GetBool("no-ui")
		in.Overrides["server.serve_ui"] = !v
	}
	if cmd.Flags().Changed("allow-network") {
		in.AllowNetwork, _ = cmd.Flags().GetBool("allow-network")
	}
	if cmd.Flags().Changed("log-level") {
		in.Overrides["log.level"] = rt.logLevel
	} else if cmd.Flags().Changed("verbose") && rt.verbose {
		in.Overrides["log.level"] = "debug"
		in.OverrideSources["log.level"] = "flag: --verbose"
	}
	if cmd.Flags().Changed("log-format") {
		in.Overrides["log.format"] = rt.logFormat
	}
	return config.ResolveServerConfig(in)
}

func newConfigCmd(rt *appCtx) *cobra.Command {
	parent := &cobra.Command{Use: "config", Short: "只读检查和解释部署配置"}
	for _, name := range []string{"check", "explain"} {
		mode := "serve"
		cmd := &cobra.Command{Use: name, Short: "本地解析配置，不创建目录/密钥/数据库或绑定端口", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			if mode != "serve" && mode != "local" {
				return fmt.Errorf("mode must be serve or local")
			}
			if mode == "serve" && cmd.Flags().Changed("allow-network") {
				return fmt.Errorf("allow-network is only available with --mode local")
			}
			r, err := resolveDeployment(cmd, rt, mode)
			if err != nil {
				return err
			}
			if cmd.Name() == "check" {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "Configuration inputs are valid. Startup dependencies and database initialization have not been checked.")
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			if _, err = fmt.Fprintln(w, "FIELD\tVALUE\tSOURCE\tMODES\tEFFECT"); err != nil {
				return err
			}
			for _, f := range r.Fields {
				if _, err = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", f.Key, f.Value, f.Source, f.Modes, f.Effect); err != nil {
					return err
				}
			}
			if err = w.Flush(); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Bootstrap values are initialization inputs, not the current database settings. Bound ports and initialization state are verified only at startup.")
			return err
		}}
		cmd.Flags().StringVar(&mode, "mode", "serve", "部署模式 serve 或 local")
		addDeploymentFlags(cmd, true)
		parent.AddCommand(cmd)
	}
	return parent
}
