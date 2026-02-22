package tlspsk

import (
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net"

	"github.com/pedramktb/go-netx"
	tlswithpks "github.com/raff/tls-ext"
	tlspks "github.com/raff/tls-psk"
)

func init() {
	netx.Register("tlspsk", func(params map[string]string, listener bool) (netx.Wrapper, error) {
		var identity string
		var psk []byte
		for key, value := range params {
			switch key {
			case "key":
				var err error
				psk, err = hex.DecodeString(value)
				if err != nil {
					return netx.Wrapper{}, fmt.Errorf("uri: invalid tlspsk key parameter: %w", err)
				}
			case "identity":
				identity = value
			default:
				return netx.Wrapper{}, fmt.Errorf("uri: unknown tlspsk parameter %q", key)
			}
		}
		if len(psk) == 0 {
			return netx.Wrapper{}, fmt.Errorf("uri: missing tlspsk key parameter")
		}
		if !listener && identity == "" {
			return netx.Wrapper{}, fmt.Errorf("uri: tlspsk client requires identity parameter")
		}
		cfg := &tlswithpks.Config{
			MinVersion: tls.VersionTLS12,
			MaxVersion: tls.VersionTLS12,
			Extra: tlspks.PSKConfig{
				GetIdentity: func() string { return identity },
				GetKey:      func(identity string) ([]byte, error) { return psk, nil },
			},
			CipherSuites:       []uint16{tlspks.TLS_PSK_WITH_AES_256_CBC_SHA},
			InsecureSkipVerify: true,
		}
		if listener {
			// Provide dummy Certificates to make tlspsk happy on server side
			cfg.Certificates = dummyCert()
			return netx.Wrapper{
				Name:     "tlspsk",
				Params:   params,
				Listener: listener,
				ListenerToListener: func(l net.Listener) (net.Listener, error) {
					return netx.ConnWrapListener(l, func(c net.Conn) (net.Conn, error) {
						return tlswithpks.Server(c, cfg), nil
					})
				},
				ConnToConn: func(c net.Conn) (net.Conn, error) {
					return tlswithpks.Server(c, cfg), nil
				}}, nil
		} else {
			return netx.Wrapper{
				Name:     "tlspsk",
				Params:   params,
				Listener: listener,
				DialerToDialer: func(f netx.Dialer) (netx.Dialer, error) {
					return netx.ConnWrapDialer(f, func(c net.Conn) (net.Conn, error) {
						return tlswithpks.Client(c, cfg), nil
					})
				},
				ConnToConn: func(c net.Conn) (net.Conn, error) {
					return tlswithpks.Client(c, cfg), nil
				}}, nil
		}
	})
}

// dummyCert returns a self-signed certificate for use in tls-psk server mode. (ed25519)
func dummyCert() []tlswithpks.Certificate {
	// Generated with:
	// openssl req -x509 -newkey ed25519 -keyout key.pem -out cert.pem -days 100000 -nodes -subj "/CN=dummy"
	certPEM := `-----BEGIN CERTIFICATE-----
MIIBNjCB6aADAgECAhRX020iAjrT4wTjwRdAJ+PPjpe33DAFBgMrZXAwEDEOMAwG
A1UEAwwFZHVtbXkwIBcNMjUwOTIxMTUxNzMwWhgPMjI5OTA3MDcxNTE3MzBaMBAx
DjAMBgNVBAMMBWR1bW15MCowBQYDK2VwAyEA/8RGhnpLT8uPAm8Ah0vEYWCskGrk
R3lqdOjspIidVmKjUzBRMB0GA1UdDgQWBBRMUX8P7I1KV1UxMjcJlIT42a72ozAf
BgNVHSMEGDAWgBRMUX8P7I1KV1UxMjcJlIT42a72ozAPBgNVHRMBAf8EBTADAQH/
MAUGAytlcANBAEFf17f1XhfLek4D203mGz8BihBfXfeL6kADMMV+G2qpkqZPcnTI
NXPuT9B/6+hM7nD/vh7JKXTfSAEFo22rzwA=
-----END CERTIFICATE-----
`
	keyPEM := `-----BEGIN PRIVATE KEY-----
MC4CAQAwBQYDK2VwBCIEIEsb9X3HHGBFSe5jKvqNmua6ZFplNaiBROtJ7ZZAJlRz
-----END PRIVATE KEY-----
`
	cert, _ := tlswithpks.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	return []tlswithpks.Certificate{cert}
}
