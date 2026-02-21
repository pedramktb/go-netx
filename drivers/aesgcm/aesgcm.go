package aesgcm

import (
	"encoding/hex"
	"fmt"
	"net"
	"strconv"

	"github.com/pedramktb/go-netx"
	aesgcmproto "github.com/pedramktb/go-netx/proto/aesgcm"
)

func init() {
	netx.Register("aesgcm", func(params map[string]string, listener bool) (netx.Wrapper, error) {
		aeskey := []byte{}
		opts := []aesgcmproto.AESGCMOption{}
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
			case "maxpacket":
				maxPacket, err := strconv.ParseUint(value, 10, 31)
				if err != nil {
					return netx.Wrapper{}, fmt.Errorf("uri: invalid aesgcm maxpacket parameter %q: %w", value, err)
				}
				opts = append(opts, aesgcmproto.WithAESGCMMaxPacket(uint32(maxPacket)))
			default:
				return netx.Wrapper{}, fmt.Errorf("uri: unknown aesgcm parameter %q", key)
			}
		}
		if len(aeskey) == 0 {
			return netx.Wrapper{}, fmt.Errorf("uri: missing aesgcm key parameter")
		}
		connToConn := func(c net.Conn) (net.Conn, error) {
			return aesgcmproto.NewAESGCMConn(c, aeskey, opts...)
		}
		return netx.Wrapper{
			Name:     "aesgcm",
			Params:   params,
			Listener: listener,
			ListenerToListener: func(l net.Listener) (net.Listener, error) {
				return netx.ConnWrapListener(l, connToConn)
			},
			DialerToDialer: func(f func() (net.Conn, error)) (func() (net.Conn, error), error) {
				return netx.ConnWrapDialer(f, connToConn)
			},
			ConnToConn: connToConn,
		}, nil
	})
}
