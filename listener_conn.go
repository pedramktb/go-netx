/*
ListenerConn adapts a net.Listener into a net.Conn by treating every connection accepted
from the listener as part of a single abstract connection. When a read encounters EOF on
the current underlying connection, the adapter seamlessly accepts the next one and continues
reading. Writes are forwarded to the connection that was most recently accepted.

This is useful when a wrapper (e.g. a DNS tunnel server) expects a single net.Conn but the
transport is connection-oriented (e.g. TCP), where each incoming connection carries one or
more request-response exchanges.
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

type listenerConn struct {
	listener net.Listener
	closed   atomic.Bool

	rMu sync.Mutex // serialises reads and accept transitions
	wMu sync.Mutex // serialises writes

	connMu  sync.RWMutex // guards current
	current net.Conn

	deadlineMu    sync.Mutex
	readDeadline  time.Time
	writeDeadline time.Time
}

// NewListenerConn wraps a net.Listener as a net.Conn.
// Reads accept connections from the listener on demand and transition to the next connection
// transparently when the current one reaches EOF.
// Writes are sent to the most recently accepted connection.
// Closing the returned conn closes both the current connection (if any) and the listener.
func NewListenerConn(ln net.Listener) net.Conn {
	return &listenerConn{listener: ln}
}

func (c *listenerConn) Read(b []byte) (int, error) {
	c.rMu.Lock()
	defer c.rMu.Unlock()

	for {
		if c.closed.Load() {
			return 0, net.ErrClosed
		}

		c.connMu.RLock()
		conn := c.current
		c.connMu.RUnlock()

		if conn == nil {
			newConn, err := c.listener.Accept()
			if err != nil {
				if c.closed.Load() {
					return 0, net.ErrClosed
				}
				return 0, err
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

			c.connMu.Lock()
			c.current = newConn
			c.connMu.Unlock()
			conn = newConn
		}

		n, err := conn.Read(b)
		if n > 0 {
			if errors.Is(err, io.EOF) {
				c.connMu.Lock()
				if c.current == conn {
					_ = c.current.Close()
					c.current = nil
				}
				c.connMu.Unlock()
			}
			return n, nil
		}
		if errors.Is(err, io.EOF) {
			c.connMu.Lock()
			if c.current == conn {
				_ = c.current.Close()
				c.current = nil
			}
			c.connMu.Unlock()
			continue // accept next connection
		}
		if c.closed.Load() {
			return 0, net.ErrClosed
		}
		return 0, err
	}
}

func (c *listenerConn) Write(b []byte) (int, error) {
	c.wMu.Lock()
	defer c.wMu.Unlock()

	if c.closed.Load() {
		return 0, net.ErrClosed
	}

	c.connMu.RLock()
	conn := c.current
	c.connMu.RUnlock()

	if conn == nil {
		return 0, io.ErrClosedPipe
	}

	return conn.Write(b)
}

func (c *listenerConn) Close() error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}

	var errs []error

	c.connMu.Lock()
	if c.current != nil {
		errs = append(errs, c.current.Close())
		c.current = nil
	}
	c.connMu.Unlock()

	errs = append(errs, c.listener.Close())
	return errors.Join(errs...)
}

func (c *listenerConn) LocalAddr() net.Addr {
	return c.listener.Addr()
}

func (c *listenerConn) RemoteAddr() net.Addr {
	c.connMu.RLock()
	defer c.connMu.RUnlock()
	if c.current != nil {
		return c.current.RemoteAddr()
	}
	return c.listener.Addr()
}

func (c *listenerConn) SetDeadline(t time.Time) error {
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

func (c *listenerConn) SetReadDeadline(t time.Time) error {
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

func (c *listenerConn) SetWriteDeadline(t time.Time) error {
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
