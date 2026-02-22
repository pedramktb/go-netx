package netx

import (
	"context"
	"fmt"
	"net"
)

const (
	TransportICMP = "icmp" // ip:1
	TransportTCP  = "tcp"  // ip:6
	TransportUDP  = "udp"  // ip:17
)

type Transport string

type ListenerTransport struct {
	Transport
}

func (t *ListenerTransport) Listen(ctx context.Context, addr string, opts ...ListenOption) (net.Listener, error) {
	return Listen(ctx, t.String(), addr, opts...)
}

func (t *ListenerTransport) UnmarshalText(text []byte) error {
	return t.Transport.UnmarshalText(text, true)
}

type DialerTransport struct {
	Transport
}

func (t *DialerTransport) Dial(ctx context.Context, addr string, opts ...DialOption) (net.Conn, error) {
	return Dial(ctx, t.String(), addr, opts...)
}

func (t *DialerTransport) UnmarshalText(text []byte) error {
	return t.Transport.UnmarshalText(text, false)
}

func (t Transport) String() string {
	switch t {
	case TransportICMP, TransportTCP, TransportUDP:
		return string(t)
	default:
		return ""
	}
}

func (t Transport) MarshalText() ([]byte, error) {
	return []byte(t.String()), nil
}

func (t *Transport) UnmarshalText(text []byte, listener bool) error {
	switch string(text) {
	case TransportICMP, TransportTCP, TransportUDP:
		*t = Transport(string(text))
		return nil
	default:
		return fmt.Errorf("uri: unknown transport %q", string(text))
	}

}
