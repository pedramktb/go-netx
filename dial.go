package netx

import (
	"context"
	"net"

	pudp "github.com/pion/transport/v3/udp"
)

// defaultUDPSocketBuffer is the OS receive/send buffer applied to UDP sockets (the
// loopback listener and the upstream dialer) unless the caller overrides it.
// Without it the platform default applies, and Windows' (~64 KB) is small enough
// that a burst — a download flight from the server, or a userspace WireGuard
// adapter flushing across loopback — overflows the socket faster than the relay
// goroutine drains it. The dropped datagrams force inner-TCP retransmits and
// collapse throughput; a few MiB of headroom absorbs the bursts. Linux's far
// larger default rmem masks this, which is why the regression was Windows-only.
const defaultUDPSocketBuffer = 4 << 20 // 4 MiB

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
		// Default the OS socket buffers so a bursty peer can't overflow them
		// (see defaultUDPSocketBuffer). Only fills unset values — an explicit
		// WithPacketListenConfig still wins. pion applies these via
		// SetReadBuffer/SetWriteBuffer on the underlying conn.
		if cfg.packet.ReadBufferSize == 0 {
			cfg.packet.ReadBufferSize = defaultUDPSocketBuffer
		}
		if cfg.packet.WriteBufferSize == 0 {
			cfg.packet.WriteBufferSize = defaultUDPSocketBuffer
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
		conn, err := cfg.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		// Enlarge the OS socket buffers on UDP so a fast download burst from the
		// server is absorbed by the kernel instead of overflowing the small
		// platform default while the relay goroutine is momentarily descheduled
		// (see defaultUDPSocketBuffer). TCP autotunes its own window, so this is
		// scoped to *net.UDPConn.
		if uc, ok := conn.(*net.UDPConn); ok {
			_ = uc.SetReadBuffer(defaultUDPSocketBuffer)
			_ = uc.SetWriteBuffer(defaultUDPSocketBuffer)
		}
		return conn, nil
	}
}
