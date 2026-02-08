package dnst

import (
	"fmt"
	"net"

	"github.com/pedramktb/go-netx"
	dnstproto "github.com/pedramktb/go-netx/proto/dnst"
)

func init() {
	netx.Register("dnst", netx.FuncDriver(func(params map[string]string, listener bool) (netx.Wrapper, error) {
		domain := params["domain"]
		if domain == "" {
			return nil, fmt.Errorf("uri: missing dnst domain parameter")
		}
		return func(c net.Conn) (net.Conn, error) {
			if listener {
				return dnstproto.NewDNSTServerConn(c, domain), nil
			}
			return dnstproto.NewDNSTClientConn(c, domain), nil
		}, nil
	}))
}
