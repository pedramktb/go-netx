package dnst

import (
	"fmt"
	"net"
	"strconv"

	"github.com/pedramktb/go-netx"
	dnstproto "github.com/pedramktb/go-netx/proto/dnst"
)

func init() {
	netx.Register("dnst", func(params map[string]string, listener bool) (netx.Wrapper, error) {
		var domain string
		opts := []dnstproto.ServerOption{}
		for key, value := range params {
			switch key {
			case "domain":
				domain = value
			case "maxw":
				if !listener {
					return netx.Wrapper{}, fmt.Errorf("dnst: max write parameter is only valid for listeners")
				}
				size, err := strconv.ParseUint(value, 10, 16)
				if err != nil {
					return netx.Wrapper{}, fmt.Errorf("dnst: invalid max write parameter %q: %w", value, err)
				}
				opts = append(opts, dnstproto.WithMaxWrite(uint16(size)))
			default:
				return netx.Wrapper{}, fmt.Errorf("dnst: unknown parameter %q", key)
			}
		}
		if domain == "" {
			return netx.Wrapper{}, fmt.Errorf("dnst: missing domain parameter")
		}
		if listener {
			return netx.Wrapper{
				Name:     "dnst",
				Params:   params,
				Listener: listener,
				ConnToTagged: func(c net.Conn) (netx.TaggedConn, error) {
					return dnstproto.NewServerConn(c, domain, opts...), nil
				},
				TaggedToTagged: func(c netx.TaggedConn) (netx.TaggedConn, error) {
					return dnstproto.NewTaggedServerConn(c, domain, opts...), nil
				}}, nil
		}
		return netx.Wrapper{
			Name:     "dnst",
			Params:   params,
			Listener: listener,
			ConnToConn: func(c net.Conn) (net.Conn, error) {
				return dnstproto.NewClientConn(c, domain), nil
			}}, nil
	})
}
