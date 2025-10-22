package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/pedramktb/go-netx/internal/cli"
)

func main() {
	os.Exit(cli.Run(signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)))
}
