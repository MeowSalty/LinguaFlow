package cli

import (
	"context"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/spf13/cobra"
	"net"
	"os/exec"
	"runtime"
	"strconv"
)

func newLocalCmd(rt *appCtx) *cobra.Command {
	var noBrowser bool
	cmd := &cobra.Command{
		Use: "local", Short: "以单用户本地模式启动 LinguaFlow", Args: cobra.NoArgs,
		Long:    "默认监听回环地址，自动使用本地管理员身份。\n非回环监听必须显式提供 --allow-network：能够连接的客户端将拥有本地管理员权限。\n该许可不启用认证，也不改变 host。数据库固定为 data_dir 下的 SQLite。",
		Example: "  linguaflow local\n  linguaflow local --port 0 --no-browser\n  linguaflow local --host 0.0.0.0 --allow-network",
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolved, err := resolveDeployment(cmd, rt, config.ModeLocal)
			if err != nil {
				return err
			}
			return runLocal(cmd.Context(), resolved, noBrowser)
		},
	}
	addDeploymentFlags(cmd, true)
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "不自动打开浏览器")
	return cmd
}
func runLocal(ctx context.Context, resolved *config.ResolvedServer, noBrowser bool) error {
	server, ln, cleanup, err := bootstrapServer(ctx, BootOptions{Resolved: resolved})
	if err != nil {
		return err
	}
	defer func() { _ = cleanup() }()
	if !noBrowser {
		go openBrowser(browserURL(ln.Addr().(*net.TCPAddr)))
	}
	return server.Run(ctx, ln)
}
func browserURL(addr *net.TCPAddr) string {
	host := addr.IP.String()
	if addr.IP.IsUnspecified() {
		if addr.IP.To4() != nil {
			host = "127.0.0.1"
		} else {
			host = "::1"
		}
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(addr.Port))
}
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Run()
}
