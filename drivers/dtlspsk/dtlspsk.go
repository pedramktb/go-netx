package dtlspsk

import (
	"encoding/hex"
	"fmt"
	"net"

	"github.com/pedramktb/go-netx"
	"github.com/pion/dtls/v3"
	dtlsnet "github.com/pion/dtls/v3/pkg/net"
)

func init() {
	netx.Register("dtlspsk", netx.FuncDriver(func(params map[string]string, listener bool) (netx.Wrapper, error) {
		var identity string
		var psk []byte
		for key, value := range params {
			switch key {
			case "key":
				var err error
				psk, err = hex.DecodeString(value)
				if err != nil {
					return nil, fmt.Errorf("uri: invalid dtlspsk key parameter: %w", err)
				}
			case "identity":
				identity = value
			default:
				return nil, fmt.Errorf("uri: unknown dtlspsk parameter %q", key)
			}
		}
		if len(psk) == 0 {
			return nil, fmt.Errorf("uri: missing dtlspsk key parameter")
		}
		if !listener && identity == "" {
			return nil, fmt.Errorf("uri: dtlspsk client requires identity parameter")
		}
		cfg := &dtls.Config{
			PSK: func(hint []byte) ([]byte, error) {
				return psk, nil
			},
			PSKIdentityHint:    []byte(identity),
			CipherSuites:       []dtls.CipherSuiteID{dtls.TLS_PSK_WITH_AES_128_GCM_SHA256},
			InsecureSkipVerify: true,
		}
		if listener {
			return func(c net.Conn) (net.Conn, error) {
				return dtls.Server(dtlsnet.PacketConnFromConn(c), c.RemoteAddr(), cfg)
			}, nil
		} else {
			return func(c net.Conn) (net.Conn, error) {
				return dtls.Client(dtlsnet.PacketConnFromConn(c), c.RemoteAddr(), cfg)
			}, nil
		}
	}))
}
