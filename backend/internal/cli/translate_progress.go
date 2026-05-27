package cli

import (
	"fmt"
	"os"
	"time"

	"golang.org/x/term"

	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
)

// newReporter 根据 --progress 标志与 stderr 是否 TTY 选择 Reporter。
//   - auto: TTY → bar，非 TTY → log
//   - bar:  强制进度条
//   - log:  强制周期日志（每 5s 或每 10 段，取先到）
//   - none: 静默（仅保留既有 slog 输出）
func newReporter(rt *appCtx) (progress.Reporter, error) {
	mode := rt.progressMode
	if mode == "" {
		mode = "auto"
	}
	if mode == "auto" {
		if term.IsTerminal(int(os.Stderr.Fd())) {
			mode = "bar"
		} else {
			mode = "log"
		}
	}
	switch mode {
	case "bar":
		return progress.NewTerminal(os.Stderr), nil
	case "log":
		return progress.NewLog(rt.logger, 5*time.Second, 10), nil
	case "none":
		return progress.Nop{}, nil
	default:
		return nil, fmt.Errorf("unknown --progress mode %q (want auto|bar|log|none)", mode)
	}
}
