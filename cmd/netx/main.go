package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	tuncmd "github.com/pedramktb/go-netx/cmd/netx/tun"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "tun":
		tuncmd.Run(ctx, cancel, os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `netx - small networking toolbox

Subcommands:
  tun    Relay between two endpoints with chainable transforms.

Run 'netx <subcommand> --help' for details.
`)
}
