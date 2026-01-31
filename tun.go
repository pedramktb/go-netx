package netx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync/atomic"
)

// Tun is an endpoint of a tunnel connection between two net.Conns.
// Conn is the underlying connection of the tunnel and Peer is the client/server communicating with the tunnel.
type Tun struct {
	Logger     Logger
	Conn       net.Conn
	Peer       net.Conn
	BufferSize uint // BufferSize for io.Copy, default 32KB
	closing    atomic.Bool
}

// Relay copies data between the two connections until
func (t *Tun) Relay(ctx context.Context) {
	if t.Logger == nil {
		t.Logger = slog.Default()
	}

	sendErrCh := make(chan error, 1)
	recvErrCh := make(chan error, 1)

	go t.halfCopy(t.Peer, t.Conn, sendErrCh)
	go t.halfCopy(t.Conn, t.Peer, recvErrCh)

	sendErr := <-sendErrCh
	recvErr := <-recvErrCh
	if sendErr != nil {
		t.Logger.ErrorContext(ctx, "error copying data from peer to tun", "error", sendErr.Error())
	}
	if recvErr != nil {
		t.Logger.ErrorContext(ctx, "error copying data from tun to peer", "error", recvErr.Error())
	}
}

func (t *Tun) halfCopy(src io.ReadCloser, dst io.WriteCloser, errCh chan<- error) {
	var buf []byte
	if t.BufferSize != 0 {
		buf = make([]byte, t.BufferSize)
	}
	defer t.Close()
	fmt.Printf("TUN HALFCOPY START: Check Peer/Conn types. Src: %T, Dst: %T\n", src, dst)
	_, err := io.CopyBuffer(dst, src, buf)
	if t.closing.Load() {
		errCh <- nil
		return
	}
	errCh <- err
}

func (t *Tun) Close() error {
	if !t.closing.CompareAndSwap(false, true) {
		return nil
	}
	connErr := t.Conn.Close()
	if errors.Is(connErr, net.ErrClosed) {
		connErr = nil
	}
	peerErr := t.Peer.Close()
	if errors.Is(peerErr, net.ErrClosed) {
		peerErr = nil
	}
	return errors.Join(connErr, peerErr)
}

type TunHandler func(ctx context.Context, conn net.Conn) (matched bool, connCtx context.Context, tunnel Tun)

// TunMaster initially accepts no connections, since there are no known tunnel handlers.
// It's the duty of the caller to add tunnel handlers via SetHandler.
// The generic ID type is used to identify different tunnel handlers, e.g. by a client ID or username.
type TunMaster[ID comparable] struct {
	Server[ID]
}

// SetRoute sets a tunnel handler for a specific ID.
// If a handler already exists for this ID, it will be replaced.
// It does not close any existing tunnels that were created by the previous handler, but new tunnels will use the new handler.
func (m *TunMaster[ID]) SetRoute(id ID, handler TunHandler) {
	m.Server.SetRoute(id, func(connCtx context.Context, conn net.Conn, closed func()) (matched bool, tun io.Closer) {
		matched, connCtx, tunnel := handler(connCtx, conn)
		if !matched {
			return false, conn
		}

		m.Logger.InfoContext(connCtx, "starting new tunnel",
			"tun", tunnel.Conn.RemoteAddr().Network()+"://"+tunnel.Conn.RemoteAddr().String(),
			"peer", tunnel.Peer.RemoteAddr().Network()+"://"+tunnel.Peer.RemoteAddr().String(),
		)

		go func() {
			tunnel.Relay(connCtx)
			closed()
			m.Logger.InfoContext(connCtx, "tunnel closed",
				"tun", tunnel.Conn.RemoteAddr().Network()+"://"+tunnel.Conn.RemoteAddr().String(),
				"peer", tunnel.Peer.RemoteAddr().Network()+"://"+tunnel.Peer.RemoteAddr().String(),
			)
		}()

		return true, &tunnel
	})
}
