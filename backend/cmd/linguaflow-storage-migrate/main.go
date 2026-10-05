package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/MeowSalty/LinguaFlow/backend/internal/storagemigrate"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := storagemigrate.NewCommand().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "Storage migration failed:", err)
		os.Exit(1)
	}
}
