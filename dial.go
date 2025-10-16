package netx

import (
	"context"
	"net"
	"strings"

	pudp "github.com/pion/transport/v3/udp"
)

type listenCfg struct {
	net.ListenConfig
	packet pudp.ListenConfig
}

type ListenOption func(*listenCfg)

func WithListenConfig(cfg net.ListenConfig) ListenOption {
	return func(lc *listenCfg) {
		lc.ListenConfig = cfg
	}
}

func WithPacketListenConfig(cfg pudp.ListenConfig) ListenOption {
	return func(lc *listenCfg) {
		lc.packet = cfg
	}
}

func Listen(ctx context.Context, network, addr string, opts ...ListenOption) (net.Listener, error) {
	cfg := &listenCfg{}
	for _, o := range opts {
		o(cfg)
	}
	switch strings.Split(network, ":")[0] {
	case "udp", "udp4", "udp6":
		uaddr, err := net.ResolveUDPAddr(network, addr)
		if err != nil {
			return nil, err
		}
		return cfg.packet.Listen(network, uaddr)
	// case "ip", "ip4", "ip6":
	// 	iaddr, err := net.ResolveIPAddr(network, addr)
	// 	if err != nil {
	// 		return nil, err
	// 	}
	// 	return (&ip.ListenConfig{
	// 		Backlog:         cfg.packet.Backlog,
	// 		AcceptFilter:    cfg.packet.AcceptFilter,
	// 		ReadBufferSize:  cfg.packet.ReadBufferSize,
	// 		WriteBufferSize: cfg.packet.WriteBufferSize,
	// 		Batch:           cfg.packet.Batch,
	// 	}).Listen(network, iaddr)
	default:
		return cfg.Listen(ctx, network, addr)
	}
}

type dialCfg struct {
	net.Dialer
}

type DialOption func(*dialCfg)

func WithDialConfig(cfg net.Dialer) DialOption {
	return func(dc *dialCfg) {
		dc.Dialer = cfg
	}
}

func Dial(ctx context.Context, network, addr string, opts ...DialOption) (net.Conn, error) {
	cfg := &dialCfg{}
	for _, o := range opts {
		o(cfg)
	}
	return cfg.DialContext(ctx, network, addr)
}
