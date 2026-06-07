//go:build darwin

package internal

import (
	"log/slog"
	"net"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// dialControl binds netx's outbound socket to the primary physical interface.
//
// On macOS, netx runs INSIDE the NEPacketTunnelProvider, whose own sockets are
// scoped to the tunnel it serves. Without an explicit binding, netx's UDP
// socket to the obfuscation server loops back into the tunnel and the WireGuard
// handshake never completes (rx=0). wireguard-go binds its socket to the
// physical interface via IP_BOUND_IF; this does the same for netx. Fail-open:
// if no usable interface is found or the bind fails, the dial proceeds unbound
// rather than erroring.
func dialControl(network, address string, c syscall.RawConn) error {
	idx, ok := primaryPhysicalIfaceIndex()
	if !ok {
		return nil
	}
	_ = c.Control(func(fd uintptr) {
		// IP_BOUND_IF / IPV6_BOUND_IF take an interface index. Best-effort for
		// both families; setting the v6 option on a v4 socket simply no-ops.
		_ = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_BOUND_IF, idx)
		_ = unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_BOUND_IF, idx)
	})
	slog.Info("netx: bound dial socket to physical interface", "network", network, "ifindex", idx)
	return nil
}

// primaryPhysicalIfaceIndex returns the index of the lowest-index, up,
// non-loopback en* interface that carries a usable (non-link-local) IPv4
// address — i.e. the Mac's primary physical / Wi-Fi interface.
func primaryPhysicalIfaceIndex() (int, bool) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return 0, false
	}
	best := -1
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		if !strings.HasPrefix(ifi.Name, "en") {
			continue
		}
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		hasV4 := false
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			if ip.To4() != nil {
				hasV4 = true
				break
			}
		}
		if !hasV4 {
			continue
		}
		if best == -1 || ifi.Index < best {
			best = ifi.Index
		}
	}
	if best == -1 {
		return 0, false
	}
	return best, true
}
