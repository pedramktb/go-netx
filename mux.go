/*
Mux adapts a net.Listener into a TaggedConn by treating every connection accepted
from the listener as part of a single abstract tagged connection. Each accepted
underlying connection is serviced concurrently: reads from all active connections
are multiplexed into a shared queue. ReadTagged returns data together with a tag
that identifies the source net.Conn; WriteTagged uses that tag to route the write
back to the exact same underlying connection.

This is useful when a wrapper (e.g. a DNS tunnel server) expects a single TaggedConn
but the transport is connection-oriented (e.g. TCP or UDP/pion), where each incoming
connection carries one or more request-response exchanges and the write path must
return the response on the same connection the request arrived on.
*/

package netx

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

func init() {
	Register("mux", func(params map[string]string, listener bool) (Wrapper, error) {
		var mopts []MuxOption
		for key, value := range params {
			switch key {
			case "rq":
				if !listener {
					return Wrapper{}, fmt.Errorf("mux: readq parameter is only valid for listeners")
				}
				size, err := strconv.ParseUint(value, 10, 16)
				if err != nil {
					return Wrapper{}, fmt.Errorf("mux: invalid readq parameter %q: %w", value, err)
				}
				mopts = append(mopts, WithMuxReadQueue(uint16(size)))
			default:
				return Wrapper{}, fmt.Errorf("uri: unknown mux parameter %q", key)
			}
		}
		if listener {
			return Wrapper{
				Name:     "mux",
				Params:   params,
				Listener: true,
				ListenerToTagged: func(ln net.Listener) (TaggedConn, error) {
					return NewMux(ln, mopts...), nil
				},
			}, nil
		}
		return Wrapper{
			Name:     "mux",
			Params:   params,
			Listener: false,
			DialerToConn: func(d Dialer) (net.Conn, error) {
				return NewMuxClient(d), nil
			},
		}, nil
	})
}

type muxPacket struct {
	data []byte
	conn net.Conn
}

type mux struct {
	listener net.Listener
	closed   atomic.Bool

	doneCh chan struct{}
	rQueue chan muxPacket // per-conn goroutines push accepted reads here

	rMu         sync.Mutex // serializes ReadTagged for pending-buffer management
	pendingData []byte
	pendingConn net.Conn

	connMu sync.Mutex
	conns  map[net.Conn]struct{}

	deadlineMu    sync.Mutex
	readDeadline  time.Time
	writeDeadline time.Time
}

type MuxOption func(*mux)

// WithMuxReadQueue sets the size of the shared read queue for the server-side Mux.
// Default is 128.
func WithMuxReadQueue(size uint16) MuxOption {
	return func(m *mux) {
		m.rQueue = make(chan muxPacket, size)
	}
}

// NewMux wraps a net.Listener as a TaggedConn.
// Each connection accepted from the listener is serviced concurrently by an
// internal goroutine that forwards received data—together with the source
// net.Conn as the tag—into a shared queue.
//
// ReadTagged returns the next available chunk and sets *tag to the net.Conn it
// arrived on. WriteTagged routes the write back to exactly that connection,
// enabling correct request/response pairing across multiple concurrent transports.
//
// Closing the returned TaggedConn closes all active underlying connections and
// the listener.
func NewMux(ln net.Listener, opts ...MuxOption) TaggedConn {
	m := &mux{
		listener: ln,
		doneCh:   make(chan struct{}),
		rQueue:   make(chan muxPacket, 64),
		conns:    make(map[net.Conn]struct{}),
	}
	for _, o := range opts {
		o(m)
	}
	go m.acceptLoop()
	return m
}

// acceptLoop accepts new connections and spawns a per-connection read goroutine.
// It runs until the listener is closed (triggered by Close).
func (c *mux) acceptLoop() {
	defer func() {
		c.connMu.Lock()
		for conn := range c.conns {
			_ = conn.Close()
		}
		c.conns = nil
		c.connMu.Unlock()
		close(c.rQueue)
	}()

	for {
		conn, err := c.listener.Accept()
		if err != nil {
			return
		}

		c.deadlineMu.Lock()
		rd, wd := c.readDeadline, c.writeDeadline
		c.deadlineMu.Unlock()
		if !rd.IsZero() {
			_ = conn.SetReadDeadline(rd)
		}
		if !wd.IsZero() {
			_ = conn.SetWriteDeadline(wd)
		}

		c.connMu.Lock()
		if c.conns == nil { // already closed
			c.connMu.Unlock()
			_ = conn.Close()
			return
		}
		c.conns[conn] = struct{}{}
		c.connMu.Unlock()

		go c.readConn(conn)
	}
}

// readConn reads from a single underlying connection and forwards packets to
// the shared readQueue. It exits on EOF, any error, or mux close.
func (c *mux) readConn(conn net.Conn) {
	defer func() {
		_ = conn.Close()
		c.connMu.Lock()
		if c.conns != nil {
			delete(c.conns, conn)
		}
		c.connMu.Unlock()
	}()

	buf := make([]byte, 65536)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			select {
			case c.rQueue <- muxPacket{data: data, conn: conn}:
			case <-c.doneCh:
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (c *mux) ReadTagged(b []byte, tag *any) (int, error) {
	c.rMu.Lock()
	defer c.rMu.Unlock()

	// Drain a pending partial packet first.
	if len(c.pendingData) > 0 {
		n := copy(b, c.pendingData)
		if tag != nil {
			*tag = c.pendingConn
		}
		c.pendingData = c.pendingData[n:]
		if len(c.pendingData) == 0 {
			c.pendingConn = nil
		}
		return n, nil
	}

	if c.closed.Load() {
		return 0, net.ErrClosed
	}

	var timeout <-chan time.Time
	c.deadlineMu.Lock()
	rd := c.readDeadline
	c.deadlineMu.Unlock()
	if !rd.IsZero() {
		d := time.Until(rd)
		if d <= 0 {
			return 0, os.ErrDeadlineExceeded
		}
		t := time.NewTimer(d)
		defer t.Stop()
		timeout = t.C
	}

	select {
	case pkt, ok := <-c.rQueue:
		if !ok {
			return 0, net.ErrClosed
		}
		n := copy(b, pkt.data)
		if tag != nil {
			*tag = pkt.conn
		}
		if n < len(pkt.data) {
			c.pendingData = pkt.data[n:]
			c.pendingConn = pkt.conn
		}
		return n, nil
	case <-timeout:
		return 0, os.ErrDeadlineExceeded
	case <-c.doneCh:
		return 0, net.ErrClosed
	}
}

func (c *mux) WriteTagged(b []byte, tag any) (int, error) {
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	conn, ok := tag.(net.Conn)
	if !ok || conn == nil {
		return 0, fmt.Errorf("mux: WriteTagged: invalid tag type %T", tag)
	}
	return conn.Write(b)
}

func (c *mux) Close() error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	close(c.doneCh)
	return c.listener.Close()
}

func (c *mux) LocalAddr() net.Addr {
	return c.listener.Addr()
}

func (c *mux) RemoteAddr() net.Addr {
	return &muxVirtualAddr{}
}

func (c *mux) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}

func (c *mux) SetReadDeadline(t time.Time) error {
	c.deadlineMu.Lock()
	c.readDeadline = t
	c.deadlineMu.Unlock()

	c.connMu.Lock()
	defer c.connMu.Unlock()
	var errs []error
	for conn := range c.conns {
		if err := conn.SetReadDeadline(t); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c *mux) SetWriteDeadline(t time.Time) error {
	c.deadlineMu.Lock()
	c.writeDeadline = t
	c.deadlineMu.Unlock()

	c.connMu.Lock()
	defer c.connMu.Unlock()
	var errs []error
	for conn := range c.conns {
		if err := conn.SetWriteDeadline(t); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type muxVirtualAddr struct{}

func (a *muxVirtualAddr) Network() string {
	return "virtual"
}

func (a *muxVirtualAddr) String() string {
	return "mux"
}
