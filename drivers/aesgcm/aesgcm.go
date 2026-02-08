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
	netx.Register("aesgcm", netx.FuncDriver(func(params map[string]string, listener bool) (netx.Wrapper, error) {
		aeskey := []byte{}
		opts := []aesgcmproto.AESGCMOption{}
		for key, value := range params {
			switch key {
			case "key":
				var err error
				aeskey, err = hex.DecodeString(value)
				if err != nil {
					return nil, fmt.Errorf("uri: invalid aesgcm key parameter: %w", err)
				}
				if len(aeskey) != 16 && len(aeskey) != 24 && len(aeskey) != 32 {
					return nil, fmt.Errorf("uri: invalid aesgcm key size %d", len(aeskey))
				}
			case "maxpacket":
				maxPacket, err := strconv.ParseUint(value, 10, 31)
				if err != nil {
					return nil, fmt.Errorf("uri: invalid aesgcm maxpacket parameter %q: %w", value, err)
				}
				opts = append(opts, aesgcmproto.WithAESGCMMaxPacket(uint32(maxPacket)))
			default:
				return nil, fmt.Errorf("uri: unknown aesgcm parameter %q", key)
			}
		}
		if len(aeskey) == 0 {
			return nil, fmt.Errorf("uri: missing aesgcm key parameter")
		}
		return func(c net.Conn) (net.Conn, error) {
			return aesgcmproto.NewAESGCMConn(c, aeskey, opts...)
		}, nil
	}))
}
