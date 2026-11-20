package main

import (
	"context"
	"fmt"
	"github.com/Emyrk/chronicle-mcp/internal/cli"
	"os"
	"os/signal"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer stop()
	if err := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "chronicle-mcp:", err)
		os.Exit(1)
	}
}
