package aesgcm

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"

	"github.com/pedramktb/go-netx"
	aesgcmproto "github.com/pedramktb/go-netx/proto/aesgcm"
)

func init() {
	netx.Register("aesgcm", func(params map[string]string, listener bool) (netx.Wrapper, error) {
		aeskey := []byte{}
		for key, value := range params {
			switch key {
			case "key":
				var err error
				aeskey, err = hex.DecodeString(value)
				if err != nil {
					return netx.Wrapper{}, fmt.Errorf("uri: invalid aesgcm key parameter: %w", err)
				}
				if len(aeskey) != 16 && len(aeskey) != 24 && len(aeskey) != 32 {
					return netx.Wrapper{}, fmt.Errorf("uri: invalid aesgcm key size %d", len(aeskey))
				}
			default:
				return netx.Wrapper{}, fmt.Errorf("uri: unknown aesgcm parameter %q", key)
			}
		}
		if len(aeskey) == 0 {
			return netx.Wrapper{}, fmt.Errorf("uri: missing aesgcm key parameter")
		}
		connToConn := func(c net.Conn) (net.Conn, error) {
			return aesgcmproto.NewAESGCMConn(c, aeskey)
		}
		keySum := sha256.Sum256(aeskey)
		return netx.Wrapper{
			Name:     "aesgcm",
			Params:   params,
			Listener: listener,
			SecretParams: []netx.SecretParam{{
				Name:        "key",
				Fingerprint: "sha256=" + colonHex(keySum[:8]),
			}},
			ListenerToListener: func(l net.Listener) (net.Listener, error) {
				return netx.ConnWrapListener(l, connToConn)
			},
			DialerToDialer: func(f netx.Dialer) (netx.Dialer, error) {
				return netx.ConnWrapDialer(f, connToConn)
			},
			ConnToConn: connToConn,
		}, nil
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
