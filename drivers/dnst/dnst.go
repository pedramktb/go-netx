package dnst

import (
	"fmt"
	"net"

	"github.com/pedramktb/go-netx"
	dnstproto "github.com/pedramktb/go-netx/proto/dnst"
)

func init() {
	netx.Register("dnst", func(params map[string]string, listener bool) (netx.Wrapper, error) {
		domain := params["domain"]
		if domain == "" {
			return netx.Wrapper{}, fmt.Errorf("uri: missing dnst domain parameter")
		}
		if listener {
			return netx.Wrapper{
				Name:     "dnst",
				Params:   params,
				Listener: listener,
				ConnToTagged: func(c net.Conn) (netx.TaggedConn, error) {
					return dnstproto.NewDNSTServerConn(c, domain), nil
				}}, nil
		}
		return netx.Wrapper{
			Name:     "dnst",
			Params:   params,
			Listener: listener,
			ConnToConn: func(c net.Conn) (net.Conn, error) {
				return dnstproto.NewDNSTClientConn(c, domain), nil
			}}, nil
	})
}
