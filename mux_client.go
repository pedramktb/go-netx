/*
MuxClient adapts a dial function into a net.Conn by treating every connection obtained
from the dialer as part of a single abstract connection. When a read or write encounters
an error on the current underlying connection, the adapter seamlessly dials a new one and
retries. This provides the illusion of a single persistent connection over a transport
that may use short-lived connections (e.g. UDP associations or individual DNS round-trips).

This is the client-side counterpart to Mux. Where Mux wraps a
net.Listener into a net.Conn by accepting incoming connections on demand, MuxClient
wraps a dial function into a net.Conn by dialing outgoing connections on demand.
*/

package netx

import (
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Dialer is the function signature accepted by NewMuxClient.
// It should return a new net.Conn each time it is called.
type Dialer = func() (net.Conn, error)

type muxClient struct {
	dial   Dialer
	closed atomic.Bool

	rMu sync.Mutex // serialises reads and redial on read path
	wMu sync.Mutex // serialises writes and redial on write path

	connMu  sync.RWMutex // guards current
	current net.Conn

	deadlineMu    sync.Mutex
	readDeadline  time.Time
	writeDeadline time.Time

	localAddr  net.Addr
	remoteAddr net.Addr
}

type MuxClientOption func(*muxClient)

// WithMuxClientLocalAddr sets the address returned by LocalAddr when no
// underlying connection is established yet.
func WithMuxClientLocalAddr(addr net.Addr) MuxClientOption {
	return func(dc *muxClient) {
		dc.localAddr = addr
	}
}

// WithMuxClientRemoteAddr sets the address returned by RemoteAddr when no
// underlying connection is established yet.
func WithMuxClientRemoteAddr(addr net.Addr) MuxClientOption {
	return func(dc *muxClient) {
		dc.remoteAddr = addr
	}
}

// NewMuxClient wraps a dial function as a net.Conn.
// A new connection is obtained by calling dial on the first Read/Write and whenever
// the current connection reaches EOF or encounters an error.
// Closing the returned conn closes the current underlying connection (if any) and
// prevents further dialling.
func NewMuxClient(dial Dialer, opts ...MuxClientOption) net.Conn {
	dc := &muxClient{dial: dial}
	for _, o := range opts {
		o(dc)
	}
	return dc
}

// ensureConn returns the current connection or dials a new one.
// Caller must NOT hold connMu.
func (c *muxClient) ensureConn() (net.Conn, error) {
	c.connMu.RLock()
	conn := c.current
	c.connMu.RUnlock()
	if conn != nil {
		return conn, nil
	}

	c.connMu.Lock()
	defer c.connMu.Unlock()

	// Double-check after acquiring write lock.
	if c.current != nil {
		return c.current, nil
	}

	newConn, err := c.dial()
	if err != nil {
		return nil, err
	}

	c.deadlineMu.Lock()
	rd, wd := c.readDeadline, c.writeDeadline
	c.deadlineMu.Unlock()
	if !rd.IsZero() {
		newConn.SetReadDeadline(rd)
	}
	if !wd.IsZero() {
		newConn.SetWriteDeadline(wd)
	}

	c.current = newConn
	return newConn, nil
}

// replaceCurrent closes the given connection if it is still current and clears
// the slot so the next operation will redial.
func (c *muxClient) replaceCurrent(old net.Conn) {
	c.connMu.Lock()
	if c.current == old {
		_ = c.current.Close()
		c.current = nil
	}
	c.connMu.Unlock()
}

func (c *muxClient) Read(b []byte) (int, error) {
	c.rMu.Lock()
	defer c.rMu.Unlock()

	for {
		if c.closed.Load() {
			return 0, net.ErrClosed
		}

		conn, err := c.ensureConn()
		if err != nil {
			if c.closed.Load() {
				return 0, net.ErrClosed
			}
			return 0, err
		}

		n, err := conn.Read(b)
		if n > 0 {
			if errors.Is(err, io.EOF) {
				c.replaceCurrent(conn)
			}
			return n, nil
		}
		if errors.Is(err, io.EOF) {
			c.replaceCurrent(conn)
			continue // redial on next iteration
		}
		if c.closed.Load() {
			return 0, net.ErrClosed
		}
		return 0, err
	}
}

func (c *muxClient) Write(b []byte) (int, error) {
	c.wMu.Lock()
	defer c.wMu.Unlock()

	if c.closed.Load() {
		return 0, net.ErrClosed
	}

	conn, err := c.ensureConn()
	if err != nil {
		if c.closed.Load() {
			return 0, net.ErrClosed
		}
		return 0, err
	}

	return conn.Write(b)
}

func (c *muxClient) Close() error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}

	c.connMu.Lock()
	defer c.connMu.Unlock()
	if c.current != nil {
		err := c.current.Close()
		c.current = nil
		return err
	}
	return nil
}

func (c *muxClient) LocalAddr() net.Addr {
	c.connMu.RLock()
	defer c.connMu.RUnlock()
	if c.current != nil {
		return c.current.LocalAddr()
	}
	if c.localAddr != nil {
		return c.localAddr
	}
	return undefinedAddr{}
}

func (c *muxClient) RemoteAddr() net.Addr {
	c.connMu.RLock()
	defer c.connMu.RUnlock()
	if c.current != nil {
		return c.current.RemoteAddr()
	}
	if c.remoteAddr != nil {
		return c.remoteAddr
	}
	return undefinedAddr{}
}

func (c *muxClient) SetDeadline(t time.Time) error {
	c.deadlineMu.Lock()
	c.readDeadline = t
	c.writeDeadline = t
	c.deadlineMu.Unlock()

	c.connMu.RLock()
	defer c.connMu.RUnlock()
	if c.current != nil {
		return c.current.SetDeadline(t)
	}
	return nil
}

func (c *muxClient) SetReadDeadline(t time.Time) error {
	c.deadlineMu.Lock()
	c.readDeadline = t
	c.deadlineMu.Unlock()

	c.connMu.RLock()
	defer c.connMu.RUnlock()
	if c.current != nil {
		return c.current.SetReadDeadline(t)
	}
	return nil
}

func (c *muxClient) SetWriteDeadline(t time.Time) error {
	c.deadlineMu.Lock()
	c.writeDeadline = t
	c.deadlineMu.Unlock()

	c.connMu.RLock()
	defer c.connMu.RUnlock()
	if c.current != nil {
		return c.current.SetWriteDeadline(t)
	}
	return nil
}

// undefinedAddr is returned when no underlying connection exists and no
// fallback address was provided via options.
type undefinedAddr struct{}

func (undefinedAddr) Network() string { return "undefined" }
func (undefinedAddr) String() string  { return "<undefined>" }
