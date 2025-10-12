package uri

import (
	"context"
	"fmt"
	"net"

	"github.com/pedramktb/go-netx"
)

type listener struct {
	net.Listener
	uri *URI
}

func (l *listener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return l.uri.Layers.Wrap(c)
}

func (u URI) Listen(ctx context.Context, opts ...netx.ListenOption) (net.Listener, error) {
	if !u.Listener {
		return nil, fmt.Errorf("uri: cannot listen on a non-listener URI")
	}
	l, err := netx.Listen(ctx, u.Scheme.Transport.String(), u.Addr, opts...)
	if err != nil {
		return nil, fmt.Errorf("error listening on %s://%s: %w", u.Scheme.Transport.String(), u.Addr, err)
	}
	return &listener{l, &u}, nil
}

func (u URI) Dial(ctx context.Context, opts ...netx.DialOption) (net.Conn, error) {
	if u.Listener {
		return nil, fmt.Errorf("uri: cannot dial on a listener URI")
	}
	c, err := netx.Dial(ctx, u.Scheme.Transport.String(), u.Addr, opts...)
	if err != nil {
		return nil, fmt.Errorf("error dialing %s://%s: %w", u.Scheme.Transport.String(), u.Addr, err)
	}
	return u.Layers.Wrap(c)
}
