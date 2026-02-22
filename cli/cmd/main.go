package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/pedramktb/go-netx/cli/internal"
	_ "github.com/pedramktb/go-netx/drivers/aesgcm"
	_ "github.com/pedramktb/go-netx/drivers/dnst"
	_ "github.com/pedramktb/go-netx/drivers/dtls"
	_ "github.com/pedramktb/go-netx/drivers/dtlspsk"
	_ "github.com/pedramktb/go-netx/drivers/ssh"
	_ "github.com/pedramktb/go-netx/drivers/tls"
	_ "github.com/pedramktb/go-netx/drivers/tlspsk"
	_ "github.com/pedramktb/go-netx/drivers/utls"
)

func main() {
	os.Exit(internal.Run(signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)))
}
