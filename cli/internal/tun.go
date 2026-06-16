package internal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	netx "github.com/pedramktb/go-netx"
	"github.com/spf13/cobra"
)

const tunExample = `	netx tun \
		--from "tcp+tls{cert=$(cat server.crt | xxd -p),key=$(cat server.key | xxd -p)}://:9000" \
 		--to "udp+aesgcm{key=00112233445566778899aabbccddeeff}://127.0.0.1:5555"
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

// handshaker is any dialed conn whose security handshake is DEFERRED to the first
// I/O (pion *dtls.Conn, stdlib *tls.Conn). pion's lazy Handshake() runs under an
// uncancellable context.Background() at first Read/Write, and pion's UDP listener
// keys conns by 5-tuple — so WireGuard's handshake-init retransmits from the same
// source port hit the SAME (stalled) conn and never re-enter the dial handler. A
// first handshake that stalls/fails is then permanent until a full client
// reconnect ("first connect dark, 2nd attempt works"). Driving the handshake
// eagerly under a bounded, cancellable ctx (below) turns that silent stall into a
// fast, retryable dial error. AES-GCM/frame/plain conns don't implement this and
// have no blocking handshake, so the assertion simply no-ops for them.
type handshaker interface {
	HandshakeContext(ctx context.Context) error
}

const (
	// 1 initial + 2 retries. Per-attempt 4s comfortably covers a DTLS Certificate
	// flight on a lossy/censored path; worst case (4+0.25+4+0.25+4 ≈ 12.5s) stays
	// under the Dart-side netx/wg start timeout (15s) so a genuinely dead path
	// returns a clean transient error to the establishment-retry layer, not a hang.
	dialHandshakeAttempts = 3
	dialHandshakeTimeout  = 4 * time.Second
	dialHandshakeBackoff  = 250 * time.Millisecond
)

func runTun(ctx context.Context, cancel context.CancelFunc, from, to string) error {
	var fromURI netx.ListenerURI
	var toURI netx.DialerURI
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
		var pconn net.Conn
		var lastErr error
		for attempt := 1; attempt <= dialHandshakeAttempts; attempt++ {
			// NetxInterrupt (ctx cancel) aborts immediately, even between attempts.
			if err := ctx.Err(); err != nil {
				lastErr = err
				break
			}
			// dialControl is a no-op except on darwin, where it binds netx's outbound
			// socket to the primary physical interface (IP_BOUND_IF) so the obfuscation
			// dial does not loop back into the NEPacketTunnelProvider's tunnel (rx=0).
			// Re-applied on EVERY attempt so the physical-iface bind survives retries.
			c, err := toURI.Dial(ctx, netx.WithDialConfig(net.Dialer{Control: dialControl}))
			if err != nil {
				lastErr = err
				slog.Warn("dial tun", "attempt", attempt, "err", err)
			} else if hs, ok := c.(handshaker); ok {
				// Drive the deferred DTLS/TLS handshake eagerly under a bounded,
				// cancellable ctx instead of letting it run later under
				// context.Background() at first I/O (see handshaker above).
				hctx, cancelHS := context.WithTimeout(ctx, dialHandshakeTimeout)
				err = hs.HandshakeContext(hctx)
				cancelHS()
				if err != nil {
					_ = c.Close()
					lastErr = err
					slog.Warn("dial tun handshake", "attempt", attempt, "err", err)
				} else {
					pconn = c
					break
				}
			} else {
				pconn = c // no deferred handshake (aes/frame/plain) — done
				break
			}
			if attempt < dialHandshakeAttempts {
				select {
				case <-ctx.Done():
				case <-time.After(dialHandshakeBackoff):
				}
			}
		}
		if pconn == nil {
			slog.Error("dial tun", "err", lastErr)
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

	// Embedders (the c-shared library forwards info-level slog output to a C
	// callback) latch on this line to know the relay is bound and serving.
	// Redacted() masks secret param values with driver-supplied protocol-standard
	// fingerprints, so the protocol chain stays visible without leaking key
	// material.
	slog.Info("netx tun started", "listen", ln.Addr().String(), "from", fromURI.Redacted(), "to", toURI.Redacted())

	<-ctx.Done()
	shutdownCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	_ = tm.Shutdown(shutdownCtx)

	return nil
}
