package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

type localOptions struct {
	host      string
	port      int
	dataDir   string
	noBrowser bool
	jwtSecret string
}

func newLocalCmd(rt *appCtx) *cobra.Command {
	opts := localOptions{}
	cmd := &cobra.Command{
		Use:   "local",
		Short: "以单用户本地模式启动 LinguaFlow",
		Example: `  linguaflow local
  linguaflow local --port 19000 --no-browser`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLocal(cmd.Context(), rt, opts)
		},
	}

	cmd.Flags().StringVar(&opts.host, "host", "127.0.0.1", "监听地址")
	cmd.Flags().IntVar(&opts.port, "port", 18080, "监听端口（0=随机空闲端口）")
	cmd.Flags().StringVar(&opts.dataDir, "data-dir", "", "数据目录（默认 UserConfigDir/LinguaFlow）")
	cmd.Flags().BoolVar(&opts.noBrowser, "no-browser", false, "不自动打开浏览器")
	cmd.Flags().StringVar(&opts.jwtSecret, "jwt-secret", "", "覆盖 LINGUAFLOW_JWT_SECRET")
	return cmd
}

func runLocal(ctx context.Context, rt *appCtx, opts localOptions) error {
	dataDir := opts.dataDir
	if dataDir == "" {
		ucd, err := os.UserConfigDir()
		if err != nil {
			return fmt.Errorf("get user config dir: %w", err)
		}
		dataDir = ucd + "/LinguaFlow"
	}

	port := resolvePort(opts.host, opts.port)

	server, ln, cleanup, err := bootstrapServer(ctx, BootOptions{
		Logger: rt.logger,
		Mode:   config.ModeLocal,
		Overrides: func(cfg *config.ServerConfig) {
			cfg.Host = opts.host
			cfg.Port = port
			cfg.DataDir = dataDir
			if opts.jwtSecret != "" {
				cfg.JWTSecret = opts.jwtSecret
			}
		},
	})
	if err != nil {
		return err
	}
	defer func() { _ = cleanup() }()

	if !opts.noBrowser {
		go openBrowser("http://" + ln.Addr().String())
	}

	return server.Run(ctx, ln)
}

// resolvePort 查找可用端口。如果请求端口为 0，返回 0（由 OS 分配）。
// 如果端口被占用，尝试递增端口号最多 10 次。
func resolvePort(host string, port int) int {
	if port == 0 {
		return 0
	}
	for i := 0; i < 10; i++ {
		addr := fmt.Sprintf("%s:%d", host, port+i)
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			_ = ln.Close()
			return port + i
		}
	}
	return port
}

// openBrowser 使用默认浏览器打开指定 URL。
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Run()
}
