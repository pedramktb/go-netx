package dtls

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/pedramktb/go-netx"
	"github.com/pion/dtls/v3"
	dtlsnet "github.com/pion/dtls/v3/pkg/net"
)

func init() {
	netx.Register("dtls", func(params map[string]string, listener bool) (netx.Wrapper, error) {
		var certKey, cert []byte
		cfg := &dtls.Config{}
		for key, value := range params {
			switch key {
			case "key":
				var err error
				certKey, err = hex.DecodeString(value)
				if err != nil {
					return netx.Wrapper{}, fmt.Errorf("uri: invalid dtls key parameter: %w", err)
				}
			case "cert":
				var err error
				cert, err = hex.DecodeString(value)
				if err != nil {
					return netx.Wrapper{}, fmt.Errorf("uri: invalid dtls cert parameter: %w", err)
				}
			case "servername":
				cfg.ServerName = value
			case "mtu":
				// Fragment handshake flights to fit the path MTU so DTLS records
				// never IP-fragment (fragmented records are disproportionately
				// dropped by middleboxes on censored paths). pion default 1200.
				n, err := strconv.ParseUint(value, 10, 32)
				if err != nil || n < 576 || n > 1500 {
					return netx.Wrapper{}, fmt.Errorf("uri: invalid dtls mtu parameter %q (want 576..1500)", value)
				}
				cfg.MTU = int(n)
			case "flightinterval":
				// Initial handshake-retransmit interval (pion default 1s). A value
				// a bit above the path RTT recovers a lost flight far faster on a
				// lossy link. Must be positive.
				d, err := time.ParseDuration(value)
				if err != nil || d <= 0 {
					return netx.Wrapper{}, fmt.Errorf("uri: invalid dtls flightinterval parameter %q: %w", value, err)
				}
				cfg.FlightInterval = d
			case "nobackoff":
				b, err := strconv.ParseBool(value)
				if err != nil {
					return netx.Wrapper{}, fmt.Errorf("uri: invalid dtls nobackoff parameter %q: %w", value, err)
				}
				cfg.DisableRetransmitBackoff = b
			case "skipcookie":
				// Server-only: skip the HelloVerifyRequest cookie exchange,
				// removing one round-trip from every fresh handshake. The cookie
				// is anti-spoofing DoS protection; for an SPKI-pinned endpoint the
				// handshake cannot complete without the pinned key, so abuse is
				// bounded — rate-limit upstream if needed.
				if !listener {
					return netx.Wrapper{}, fmt.Errorf("uri: dtls skipcookie parameter is only valid for servers")
				}
				b, err := strconv.ParseBool(value)
				if err != nil {
					return netx.Wrapper{}, fmt.Errorf("uri: invalid dtls skipcookie parameter %q: %w", value, err)
				}
				cfg.InsecureSkipVerifyHello = b
			case "resume":
				// Enable abbreviated-handshake session resumption (skips the
				// Certificate flight for a returning peer). Backed by a bounded
				// process-global store shared across listener/dialer instances.
				//
				// CAVEAT: with the certificate-based dtls driver, pion/dtls v3.1.2
				// fails the *full* handshake when SessionStore is set on both ends
				// with separate stores (the production client/server split). Prefer
				// resumption on the dtlspsk driver, where it works. The param is
				// still accepted here for completeness/forward-compat.
				b, err := strconv.ParseBool(value)
				if err != nil {
					return netx.Wrapper{}, fmt.Errorf("uri: invalid dtls resume parameter %q: %w", value, err)
				}
				if b {
					cfg.SessionStore = sharedSessionStore()
				}
			default:
				return netx.Wrapper{}, fmt.Errorf("uri: unknown dtls parameter %q", key)
			}
		}
		if listener {
			if cert == nil || certKey == nil {
				return netx.Wrapper{}, fmt.Errorf("uri: dtls server requires cert and key parameters")
			}
			certificate, err := tls.X509KeyPair(cert, certKey)
			if err != nil {
				return netx.Wrapper{}, fmt.Errorf("uri: invalid dtls certificate: %w", err)
			}
			cfg.Certificates = []tls.Certificate{certificate}
			return netx.Wrapper{
				Name:     "dtls",
				Params:   params,
				Listener: listener,
				ListenerToListener: func(l net.Listener) (net.Listener, error) {
					return dtls.NewListener(dtlsnet.PacketListenerFromListener(l), cfg)
				},
				ConnToConn: func(c net.Conn) (net.Conn, error) {
					return dtls.Server(dtlsnet.PacketConnFromConn(c), c.RemoteAddr(), cfg)
				}}, nil
		} else {
			if certKey != nil {
				return netx.Wrapper{}, fmt.Errorf("uri: dtls client does not support key parameter")
			}
			if cert != nil {
				var err error
				cfg.InsecureSkipVerify = true
				cfg.VerifyPeerCertificate, err = spkiVerifier(cert)
				if err != nil {
					return netx.Wrapper{}, fmt.Errorf("uri: invalid dtls cert parameter: %w", err)
				}
			}
			if cfg.ServerName == "" && cert == nil {
				return netx.Wrapper{}, fmt.Errorf("uri: dtls client requires servername or cert parameter")
			}
			return netx.Wrapper{
				Name:     "dtls",
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

func spkiVerifier(certPEM []byte) (func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("uri: invalid PEM certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("uri: parse x509 certificate: %w", err)
	}
	spkiHash := sha256.New().Sum(cert.RawSubjectPublicKeyInfo)
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		for _, rawCert := range rawCerts {
			c, err := x509.ParseCertificate(rawCert)
			if err != nil {
				return fmt.Errorf("parse peer cert: %w", err)
			}
			if bytes.Equal(sha256.New().Sum(c.RawSubjectPublicKeyInfo), spkiHash) {
				return nil
			}
		}
		return fmt.Errorf("no matching SPKI found")
	}, nil
}
