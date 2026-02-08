package netx

import (
	"context"
	"fmt"
	"net"
	"strings"
)

type ListenerURI struct {
	URI
}

type listenerWrapper struct {
	net.Listener
	uri *ListenerURI
}

func (l *listenerWrapper) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return l.uri.Wrap(c)
}

func (u ListenerURI) Listen(ctx context.Context, opts ...ListenOption) (net.Listener, error) {
	l, err := Listen(ctx, u.Scheme.Transport.String(), u.Addr, opts...)
	if err != nil {
		return nil, fmt.Errorf("error listening on %s://%s: %w", u.Scheme.Transport.String(), u.Addr, err)
	}
	l, err = u.Scheme.WrapListener(l)
	if err != nil {
		//return nil, fmt.Errorf("error multiplexing listener on %s://%s with %q: %w", u.Scheme.Transport.String(), u.Addr, u.Scheme.TransportLayers.String(), err)
	}
	return &listenerWrapper{l, &u}, nil
}

func (u *ListenerURI) UnmarshalText(text []byte) error {
	return u.URI.UnmarshalText(text, true)
}

type DialerURI struct {
	URI
}

func (u DialerURI) Dial(ctx context.Context, opts ...DialOption) (net.Conn, error) {
	dial := func() (net.Conn, error) {
		return Dial(ctx, u.Scheme.Transport.String(), u.Addr, opts...)
	}
	dial, err := u.Scheme.WrapDialer(dial)
	if err != nil {
		//return nil, fmt.Errorf("error multiplexing dialer to %s://%s with %q: %w", u.Scheme.Transport.String(), u.Addr, u.Scheme.TransportLayers.String(), err)
	}
	c, err := dial()
	if err != nil {
		return nil, fmt.Errorf("error dialing %s://%s: %w", u.Scheme.Transport.String(), u.Addr, err)
	}
	return u.Wrap(c)
}

func (u *DialerURI) UnmarshalText(text []byte) error {
	return u.URI.UnmarshalText(text, false)
}

type URI struct {
	Scheme `json:"scheme"`
	Addr   string `json:"addr"`
}

func (u URI) String() string {
	return u.Scheme.String() + "://" + u.Addr
}

func (u URI) MarshalText() ([]byte, error) {
	return []byte(u.String()), nil
}

func (u *URI) UnmarshalText(text []byte, server bool) error {
	str := string(text)
	parts := strings.SplitN(str, "://", 2)
	if len(parts) < 2 {
		return fmt.Errorf("uri: missing scheme delimiter in %q", str)
	}

	u.Addr = strings.TrimSpace(parts[1])
	if u.Addr == "" {
		return fmt.Errorf("uri: empty address in %q", str)
	}

	return u.Scheme.UnmarshalText([]byte(parts[0]), server)
}
