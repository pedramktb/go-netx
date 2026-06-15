//go:build darwin

package internal

import (
	"log/slog"
	"net"
	"runtime"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// dialControl binds netx's outbound socket to a physical interface — but ONLY on
// iOS. There, netx runs INSIDE the NEPacketTunnelProvider, whose sockets are
// scoped to the tunnel; without the bind, netx's obfuscation-server socket loops
// back into the tunnel and the WireGuard handshake never completes (rx=0).
//
// On macOS, netx runs in the root HELPER DAEMON, OUTSIDE any tunnel, so its
// egress already follows the kernel routing table (the daemon excludes the
// server /32 from the utun). Binding here is not just unnecessary — it is
// HARMFUL: primaryPhysicalIfaceIndex's "lowest-index up interface" guess can land
// on a STALE or secondary interface after a network switch (a USB-ethernet /
// Thunderbolt dock / lingering old Wi-Fi), pinning the socket to an interface
// whose subnet no longer matches the server route. The kernel then drops every
// packet — tx climbs, rx stays ~0, the handshake never completes (the macOS
// post-network-switch dead-tunnel bug). So macOS lets the route table decide,
// exactly like Linux/Windows where dialControl is already a no-op (tun_other.go).
func dialControl(network, address string, c syscall.RawConn) error {
	if runtime.GOOS != "ios" {
		return nil
	}
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
// non-loopback, NON-TUNNEL interface that carries a usable (non-link-local) IPv4
// address — the device's active physical uplink. Used on iOS only (see
// dialControl). It must include cellular (pdp_ipN), not just Wi-Fi (enN): a
// Wi-Fi->cellular switch leaves only pdp_ip0 up, and an en-only match would find
// nothing -> the dial proceeds unbound -> netx loops into the tunnel (rx=0).
// Tunnel interfaces (utun/ipsec/ppp/tap) are excluded so we never bind to our own
// tunnel (whose utun carries a usable 10.x address).
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
		n := ifi.Name
		if strings.HasPrefix(n, "utun") || strings.HasPrefix(n, "ipsec") ||
			strings.HasPrefix(n, "ppp") || strings.HasPrefix(n, "tap") {
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
