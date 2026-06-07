//go:build !darwin

package internal

import "syscall"

// dialControl is a no-op off darwin. On macOS it binds netx's outbound socket
// to the primary physical interface (see tun_darwin.go); on Linux/Windows netx
// runs as its own process, so the socket is not scoped to the tunnel and no
// binding is needed.
func dialControl(network, address string, c syscall.RawConn) error {
	return nil
}
