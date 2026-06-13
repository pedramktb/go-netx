package dtlspsk

import (
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/pedramktb/go-netx"
	"github.com/pion/dtls/v3"
	dtlsnet "github.com/pion/dtls/v3/pkg/net"
)

func init() {
	netx.Register("dtlspsk", func(params map[string]string, listener bool) (netx.Wrapper, error) {
		var identity string
		var psk []byte
		var (
			mtu            int
			flightInterval time.Duration
			noBackoff      bool
			skipCookie     bool
			resume         bool
		)
		for key, value := range params {
			switch key {
			case "key":
				var err error
				psk, err = hex.DecodeString(value)
				if err != nil {
					return netx.Wrapper{}, fmt.Errorf("uri: invalid dtlspsk key parameter: %w", err)
				}
			case "identity":
				identity = value
			case "mtu":
				// Fragment handshake flights to fit the path MTU so DTLS records
				// never IP-fragment on censored paths. pion default 1200.
				n, err := strconv.ParseUint(value, 10, 32)
				if err != nil || n < 576 || n > 1500 {
					return netx.Wrapper{}, fmt.Errorf("uri: invalid dtlspsk mtu parameter %q (want 576..1500)", value)
				}
				mtu = int(n)
			case "flightinterval":
				// Initial handshake-retransmit interval (pion default 1s); set a
				// bit above the path RTT to recover lost flights faster on loss.
				d, err := time.ParseDuration(value)
				if err != nil || d <= 0 {
					return netx.Wrapper{}, fmt.Errorf("uri: invalid dtlspsk flightinterval parameter %q: %w", value, err)
				}
				flightInterval = d
			case "nobackoff":
				b, err := strconv.ParseBool(value)
				if err != nil {
					return netx.Wrapper{}, fmt.Errorf("uri: invalid dtlspsk nobackoff parameter %q: %w", value, err)
				}
				noBackoff = b
			case "skipcookie":
				// Server-only: skip the HelloVerifyRequest cookie round-trip. The
				// PSK already gates abuse (a handshake can't complete without the
				// shared key), so the dropped anti-spoof cookie is bounded.
				if !listener {
					return netx.Wrapper{}, fmt.Errorf("uri: dtlspsk skipcookie parameter is only valid for servers")
				}
				b, err := strconv.ParseBool(value)
				if err != nil {
					return netx.Wrapper{}, fmt.Errorf("uri: invalid dtlspsk skipcookie parameter %q: %w", value, err)
				}
				skipCookie = b
			case "resume":
				// Abbreviated-handshake resumption (process-global bounded store).
				b, err := strconv.ParseBool(value)
				if err != nil {
					return netx.Wrapper{}, fmt.Errorf("uri: invalid dtlspsk resume parameter %q: %w", value, err)
				}
				resume = b
			default:
				return netx.Wrapper{}, fmt.Errorf("uri: unknown dtlspsk parameter %q", key)
			}
		}
		if len(psk) == 0 {
			return netx.Wrapper{}, fmt.Errorf("uri: missing dtlspsk key parameter")
		}
		if !listener && identity == "" {
			return netx.Wrapper{}, fmt.Errorf("uri: dtlspsk client requires identity parameter")
		}
		cfg := &dtls.Config{
			PSK: func(hint []byte) ([]byte, error) {
				return psk, nil
			},
			PSKIdentityHint:    []byte(identity),
			CipherSuites:       []dtls.CipherSuiteID{dtls.TLS_PSK_WITH_AES_128_GCM_SHA256},
			InsecureSkipVerify: true,
		}
		if mtu != 0 {
			cfg.MTU = mtu
		}
		if flightInterval != 0 {
			cfg.FlightInterval = flightInterval
		}
		cfg.DisableRetransmitBackoff = noBackoff
		cfg.InsecureSkipVerifyHello = skipCookie
		if resume {
			cfg.SessionStore = sharedSessionStore()
		}
		if listener {
			return netx.Wrapper{
				Name:     "dtlspsk",
				Params:   params,
				Listener: listener,
				ListenerToListener: func(l net.Listener) (net.Listener, error) {
					return dtls.NewListener(dtlsnet.PacketListenerFromListener(l), cfg)
				},
				ConnToConn: func(c net.Conn) (net.Conn, error) {
					return dtls.Server(dtlsnet.PacketConnFromConn(c), c.RemoteAddr(), cfg)
				}}, nil
		} else {
			return netx.Wrapper{
				Name:     "dtlspsk",
				Params:   params,
				Listener: listener,
				DialerToDialer: func(f netx.Dialer) (netx.Dialer, error) {
					return netx.ConnWrapDialer(f, func(c net.Conn) (net.Conn, error) {
						return dtls.Client(dtlsnet.PacketConnFromConn(c), c.RemoteAddr(), cfg)
					})
				},
				ConnToConn: func(c net.Conn) (net.Conn, error) {
					return dtls.Client(dtlsnet.PacketConnFromConn(c), c.RemoteAddr(), cfg)
				}}, nil
		}
	})
}
