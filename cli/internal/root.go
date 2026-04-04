package internal

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

type cfg struct {
	args []string
	out  io.Writer
	err  io.Writer
}

type Option func(*cfg)

func WithArgs(args []string) Option {
	return func(c *cfg) {
		c.args = args
	}
}
func WithOut(w io.Writer) Option {
	return func(c *cfg) {
		c.out = w
	}
}

func WithErr(w io.Writer) Option {
	return func(c *cfg) {
		c.err = w
	}
}

func Run(ctx context.Context, cancel context.CancelFunc, opts ...Option) (exitCode int) {
	cfg := cfg{
		args: os.Args[1:],
		out:  os.Stdout,
		err:  os.Stderr,
	}

	for _, o := range opts {
		o(&cfg)
	}

	var logLevel string

	cmd := &cobra.Command{
		Use:           "netx [command]",
		Short:         "Small networking toolbox",
		Long:          "netx is a small networking toolbox.",
		Version:       "dev",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			lvl, err := parseLogLevel(logLevel)
			if err != nil {
				return err
			}
			slog.SetDefault(slog.New(slog.NewTextHandler(cfg.out, &slog.HandlerOptions{Level: lvl})))
			return nil
		},
	}

	cmd.SetArgs(cfg.args)
	cmd.SetOut(cfg.out)
	cmd.SetErr(cfg.err)

	defaultHelp := cmd.HelpFunc()
	cmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		defaultHelp(cmd, args)
		fmt.Fprintln(cmd.OutOrStdout())
		fmt.Fprint(cmd.OutOrStdout(), uriFormat)
	})

	cmd.PersistentFlags().StringVar(&logLevel, "log", "info", "log level: debug|info|warn|error")

	cmd.AddCommand(tun(cancel))

	if err := cmd.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(cfg.err, err)
		return 1
	}

	return 0
}

func parseLogLevel(level string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log level %q", level)
	}
}
