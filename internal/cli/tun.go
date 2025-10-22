package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	netx "github.com/pedramktb/go-netx"
	"github.com/pedramktb/go-netx/uri"
	"github.com/spf13/cobra"
)

const tunExample = `	netx tun \
		--from "tcp+tls[cert=$(cat server.crt | xxd -p),key=$(cat server.key | xxd -p)]://:9000" \
 		--to "udp+aesgcm[key=00112233445566778899aabbccddeeff]://127.0.0.1:5555"
`

func tun(cancel context.CancelFunc) *cobra.Command {
	var from string
	var to string

	if cancel == nil {
		cancel = func() {}
	}

	cmd := &cobra.Command{
		Use:           "tun",
		Short:         "Relay between two endpoints with chainable transforms.",
		Long:          "tun relays between two endpoints with chainable transforms, this can be used for obfuscation tunnels, proxies, reverse proxies, etc.",
		Example:       tunExample,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			err := runTun(ctx, cancel, from, to)
			if err != nil {
				return errors.Join(err, cmd.Help())
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&from, "from", "", "<uri>")
	cmd.Flags().StringVar(&to, "to", "", "<uri>")

	_ = cmd.MarkFlagRequired("from")
	_ = cmd.MarkFlagRequired("to")

	return cmd
}

func runTun(ctx context.Context, cancel context.CancelFunc, from, to string) error {
	var fromURI, toURI uri.URI
	fromURI.Listener = true
	if err := fromURI.UnmarshalText([]byte(from)); err != nil {
		return fmt.Errorf("parse --from: %w", err)
	}
	if err := toURI.UnmarshalText([]byte(to)); err != nil {
		return fmt.Errorf("parse --to: %w", err)
	}

	ln, err := fromURI.Listen(ctx)
	if err != nil {
		return err
	}
	defer ln.Close()

	tm := netx.TunMaster[struct{}]{}

	tm.SetRoute(struct{}{}, func(ctx context.Context, conn net.Conn) (bool, context.Context, netx.Tun) {
		pconn, err := toURI.Dial(ctx)
		if err != nil {
			slog.Error("dial peer", "err", err)
			_ = conn.Close()
			return false, ctx, netx.Tun{}
		}

		return true, ctx, netx.Tun{Conn: conn, Peer: pconn}
	})

	go func() {
		if err := tm.Serve(ctx, ln); err != nil && !errors.Is(err, netx.ErrServerClosed) {
			slog.Error("serve error", "err", err)
			cancel()
		}
	}()

	slog.Info("netx tun started", "listen", ln.Addr().String(), "from", from, "to", to)

	<-ctx.Done()
	shutdownCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	_ = tm.Shutdown(shutdownCtx)

	return nil
}
