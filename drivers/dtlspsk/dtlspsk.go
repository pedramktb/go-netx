package dtlspsk

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"

	"github.com/pedramktb/go-netx"
	"github.com/pion/dtls/v3"
	dtlsnet "github.com/pion/dtls/v3/pkg/net"
)

func init() {
	netx.Register("dtlspsk", func(params map[string]string, listener bool) (netx.Wrapper, error) {
		var identity string
		var psk []byte
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
		pskSum := sha256.Sum256(psk)
		secretParams := []netx.SecretParam{{
			Name:        "key",
			Fingerprint: "sha256=" + colonHex(pskSum[:8]),
		}}
		if listener {
			return netx.Wrapper{
				Name:         "dtlspsk",
				Params:       params,
				Listener:     listener,
				SecretParams: secretParams,
				ListenerToListener: func(l net.Listener) (net.Listener, error) {
					return dtls.NewListener(dtlsnet.PacketListenerFromListener(l), cfg)
				},
				ConnToConn: func(c net.Conn) (net.Conn, error) {
					return dtls.Server(dtlsnet.PacketConnFromConn(c), c.RemoteAddr(), cfg)
				}}, nil
		} else {
			return netx.Wrapper{
				Name:         "dtlspsk",
				Params:       params,
				Listener:     listener,
				SecretParams: secretParams,
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

// colonHex formats b as colon-separated hex pairs (e.g. "ab:cd:ef").
func colonHex(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*3-1)
	for i, x := range b {
		if i > 0 {
			out = append(out, ':')
		}
		out = append(out, hexdigits[x>>4], hexdigits[x&0x0f])
	}
	return string(out)
}
