package uri

import (
	"context"
	"fmt"
	"net"

	"github.com/pedramktb/go-netx"
)

type listener struct {
	net.Listener
	uri *ServerURI
}

func (l *listener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return l.uri.WrapConn(c)
}

func (u ServerURI) Listen(ctx context.Context, opts ...netx.ListenOption) (net.Listener, error) {
	l, err := netx.Listen(ctx, u.Scheme.Transport.String(), u.Addr, opts...)
	if err != nil {
		return nil, fmt.Errorf("error listening on %s://%s: %w", u.Scheme.Transport.String(), u.Addr, err)
	}
	l, err = u.Scheme.WrapListener(l)
	if err != nil {
		return nil, fmt.Errorf("error multiplexing listener on %s://%s with %q: %w", u.Scheme.Transport.String(), u.Addr, u.Scheme.TransportLayers.String(), err)
	}
	return &listener{l, &u}, nil
}

func (u ClientURI) Dial(ctx context.Context, opts ...netx.DialOption) (net.Conn, error) {
	dial := func() (net.Conn, error) {
		return netx.Dial(ctx, u.Scheme.Transport.String(), u.Addr, opts...)
	}
	dial, err := u.Scheme.WrapDialer(dial)
	if err != nil {
		return nil, fmt.Errorf("error multiplexing dialer to %s://%s with %q: %w", u.Scheme.Transport.String(), u.Addr, u.Scheme.TransportLayers.String(), err)
	}
	c, err := dial()
	if err != nil {
		return nil, fmt.Errorf("error dialing %s://%s: %w", u.Scheme.Transport.String(), u.Addr, err)
	}
	return u.WrapConn(c)
}
