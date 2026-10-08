// linguaflow-migrate is intentionally built separately from the service.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/MeowSalty/LinguaFlow/backend/internal/migrationcli"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := migrationcli.NewCommand().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "Migration failed:", err)
		os.Exit(1)
	}
}
