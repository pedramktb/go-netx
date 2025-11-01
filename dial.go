package netx

import (
	"context"
	"net"

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
	switch network {
	case "udp", "udp4", "udp6":
		uaddr, err := net.ResolveUDPAddr(network, addr)
		if err != nil {
			return nil, err
		}
		return cfg.packet.Listen(network, uaddr)
	case "icmp":
		network = "ip:icmp"
		fallthrough
	case "ip:icmp", "ip4:icmp", "ip6:ipv6-icmp":
		iaddr, err := net.ResolveIPAddr(network, addr)
		if err != nil {
			return nil, err
		}
		return (&icmpListenConfig{
			Backlog:         cfg.packet.Backlog,
			AcceptFilter:    cfg.packet.AcceptFilter,
			ReadBufferSize:  cfg.packet.ReadBufferSize,
			WriteBufferSize: cfg.packet.WriteBufferSize,
			Batch:           cfg.packet.Batch,
		}).Listen(network, iaddr)
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
	switch network {
	case "icmp":
		network = "ip:icmp"
		fallthrough
	case "ip:icmp", "ip4:icmp", "ip6:ipv6-icmp":
		conn, err := cfg.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		var version ipV
		switch network {
		case "ip4:icmp":
			version = 4
		case "ip6:ipv6-icmp":
			version = 6
		default:
			iaddr, _ := conn.LocalAddr().(*net.IPAddr)
			if iaddr.IP.To4() != nil {
				version = 4
			} else {
				version = 6
			}
		}
		return NewICMPClientConn(conn, version)
	default:
		return cfg.DialContext(ctx, network, addr)
	}
}
