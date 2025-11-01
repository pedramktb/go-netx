// This file is adapted from github.com/pion/transport/v3/udp/conn.go to support ICMP PacketConn
// with the origin being subject to the following license:
// SPDX-FileCopyrightText: 2023 The Pion community <https://pion.ly>
// SPDX-License-Identifier: MIT

package netx

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/transport/v3/deadline"
	"github.com/pion/transport/v3/packetio"
	pudp "github.com/pion/transport/v3/udp"
	"golang.org/x/net/ipv4"
)

const (
	receiveMTU           = 8192
	defaultListenBacklog = 128 // same as Linux default
)

// Typed errors.
var (
	ErrClosedListener      = errors.New("icmp: listener closed")
	ErrListenQueueExceeded = errors.New("icmp: listen queue exceeded")
	ErrInvalidBatchConfig  = errors.New("icmp: invalid batch config")
)

// listener augments a connection-oriented Listener over an ICMP PacketConn.
type icmpListener struct {
	ipV

	pConn net.PacketConn

	readBatchSize int

	accepting    atomic.Value // bool
	acceptCh     chan *icmpListenerConn
	doneCh       chan struct{}
	doneOnce     sync.Once
	acceptFilter func([]byte) bool

	connLock sync.Mutex
	conns    map[string]*icmpListenerConn
	connWG   *sync.WaitGroup

	readWG   sync.WaitGroup
	errClose atomic.Value // error

	readDoneCh chan struct{}
	errRead    atomic.Value // error
}

// Accept waits for and returns the next connection to the listener.
func (l *icmpListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.acceptCh:
		l.connWG.Add(1)

		return NewICMPServerConn(c, l.ipV)

	case <-l.readDoneCh:
		err, _ := l.errRead.Load().(error)

		return nil, err

	case <-l.doneCh:
		return nil, ErrClosedListener
	}
}

// Close closes the listener.
// Any blocked Accept operations will be unblocked and return errors.
func (l *icmpListener) Close() error {
	var err error
	l.doneOnce.Do(func() {
		l.accepting.Store(false)
		close(l.doneCh)

		l.connLock.Lock()
		// Close unaccepted connections
	lclose:
		for {
			select {
			case c := <-l.acceptCh:
				close(c.doneCh)
				delete(l.conns, c.rAddr.String())

			default:
				break lclose
			}
		}
		nConns := len(l.conns)
		l.connLock.Unlock()

		l.connWG.Done()

		if nConns == 0 {
			// Wait if this is the final connection
			l.readWG.Wait()
			if errClose, ok := l.errClose.Load().(error); ok {
				err = errClose
			}
		} else {
			err = nil
		}
	})

	return err
}

// Addr returns the listener's network address.
func (l *icmpListener) Addr() net.Addr {
	return l.pConn.LocalAddr()
}

// icmpListenConfig stores options for listening to an address.
type icmpListenConfig struct {
	// Backlog defines the maximum length of the queue of pending
	// connections. It is equivalent of the backlog argument of
	// POSIX listen function.
	// If a connection request arrives when the queue is full,
	// the request will be silently discarded, unlike TCP.
	// Set zero to use default value 128 which is same as Linux default.
	Backlog int

	// AcceptFilter determines whether the new conn should be made for
	// the incoming packet. If not set, any packet creates new conn.
	AcceptFilter func([]byte) bool

	// ReadBufferSize sets the size of the operating system's
	// receive buffer associated with the listener.
	ReadBufferSize int

	// WriteBufferSize sets the size of the operating system's
	// send buffer associated with the connection.
	WriteBufferSize int

	Batch pudp.BatchIOConfig
}

// Listen creates a new listener based on the ListenConfig.
func (lc *icmpListenConfig) Listen(network string, laddr *net.IPAddr) (net.Listener, error) {
	if lc.Backlog == 0 {
		lc.Backlog = defaultListenBacklog
	}

	if lc.Batch.Enable && (lc.Batch.WriteBatchSize <= 0 || lc.Batch.WriteBatchInterval <= 0) {
		return nil, ErrInvalidBatchConfig
	}

	conn, err := net.ListenIP(network, laddr)
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

	if lc.ReadBufferSize > 0 {
		_ = conn.SetReadBuffer(lc.ReadBufferSize)
	}
	if lc.WriteBufferSize > 0 {
		_ = conn.SetWriteBuffer(lc.WriteBufferSize)
	}

	listnerer := &icmpListener{
		ipV:          version,
		pConn:        conn,
		acceptCh:     make(chan *icmpListenerConn, lc.Backlog),
		conns:        make(map[string]*icmpListenerConn),
		doneCh:       make(chan struct{}),
		acceptFilter: lc.AcceptFilter,
		connWG:       &sync.WaitGroup{},
		readDoneCh:   make(chan struct{}),
	}

	if lc.Batch.Enable {
		listnerer.pConn = pudp.NewBatchConn(conn, lc.Batch.WriteBatchSize, lc.Batch.WriteBatchInterval)
		listnerer.readBatchSize = lc.Batch.ReadBatchSize
	}

	listnerer.accepting.Store(true)
	listnerer.connWG.Add(1)
	listnerer.readWG.Add(2) // wait readLoop and Close execution routine

	go listnerer.readLoop()
	go func() {
		listnerer.connWG.Wait()
		if err := listnerer.pConn.Close(); err != nil {
			listnerer.errClose.Store(err)
		}
		listnerer.readWG.Done()
	}()

	return listnerer, nil
}

// readLoop has to tasks:
//  1. Dispatching incoming packets to the correct Conn.
//     It can therefore not be ended until all Conns are closed.
//  2. Creating a new Conn when receiving from a new remote.
func (l *icmpListener) readLoop() {
	defer l.readWG.Done()
	defer close(l.readDoneCh)

	if br, ok := l.pConn.(pudp.BatchReader); ok && l.readBatchSize > 1 {
		l.readBatch(br)
	} else {
		l.read()
	}
}

func (l *icmpListener) readBatch(br pudp.BatchReader) {
	msgs := make([]ipv4.Message, l.readBatchSize)
	for i := range msgs {
		msg := &msgs[i]
		msg.Buffers = [][]byte{make([]byte, receiveMTU)}
		msg.OOB = make([]byte, 40)
	}
	for {
		n, err := br.ReadBatch(msgs, 0)
		if err != nil {
			l.errRead.Store(err)

			return
		}
		for i := range n {
			l.dispatchMsg(msgs[i].Addr, msgs[i].Buffers[0][:msgs[i].N])
		}
	}
}

func (l *icmpListener) read() {
	buf := make([]byte, receiveMTU)
	for {
		n, raddr, err := l.pConn.ReadFrom(buf)
		if err != nil {
			l.errRead.Store(err)

			return
		}
		l.dispatchMsg(raddr, buf[:n])
	}
}

func (l *icmpListener) dispatchMsg(addr net.Addr, buf []byte) {
	conn, ok, err := l.getConn(addr, buf)
	if err != nil {
		return
	}
	if ok {
		_, _ = conn.buffer.Write(buf)
	}
}

func (l *icmpListener) getConn(raddr net.Addr, buf []byte) (*icmpListenerConn, bool, error) {
	l.connLock.Lock()
	defer l.connLock.Unlock()
	conn, ok := l.conns[raddr.String()]
	if !ok {
		if isAccepting, ok := l.accepting.Load().(bool); !isAccepting || !ok {
			return nil, false, ErrClosedListener
		}
		if l.acceptFilter != nil {
			if !l.acceptFilter(buf) {
				return nil, false, nil
			}
		}
		conn = l.newConn(raddr)
		select {
		case l.acceptCh <- conn:
			l.conns[raddr.String()] = conn
		default:
			return nil, false, ErrListenQueueExceeded
		}
	}

	return conn, true, nil
}

// icmpListenerConn augments a connection-oriented connection over an ICMP PacketConn.
type icmpListenerConn struct {
	listener *icmpListener

	rAddr net.Addr

	buffer *packetio.Buffer

	doneCh   chan struct{}
	doneOnce sync.Once

	writeDeadline *deadline.Deadline
}

func (l *icmpListener) newConn(rAddr net.Addr) *icmpListenerConn {
	return &icmpListenerConn{
		listener:      l,
		rAddr:         rAddr,
		buffer:        packetio.NewBuffer(),
		doneCh:        make(chan struct{}),
		writeDeadline: deadline.New(),
	}
}

// Read reads from c into p.
func (c *icmpListenerConn) Read(p []byte) (int, error) {
	return c.buffer.Read(p)
}

// Write writes len(p) bytes from p to the ICMP connection.
func (c *icmpListenerConn) Write(p []byte) (n int, err error) {
	select {
	case <-c.writeDeadline.Done():
		return 0, context.DeadlineExceeded
	default:
	}

	return c.listener.pConn.WriteTo(p, c.rAddr)
}

// Close closes the conn and releases any Read calls.
func (c *icmpListenerConn) Close() error {
	var err error
	c.doneOnce.Do(func() {
		c.listener.connWG.Done()
		close(c.doneCh)
		c.listener.connLock.Lock()
		delete(c.listener.conns, c.rAddr.String())
		nConns := len(c.listener.conns)
		c.listener.connLock.Unlock()

		if isAccepting, ok := c.listener.accepting.Load().(bool); nConns == 0 && !isAccepting && ok {
			// Wait if this is the final connection
			c.listener.readWG.Wait()
			if errClose, ok := c.listener.errClose.Load().(error); ok {
				err = errClose
			}
		} else {
			err = nil
		}

		if errBuf := c.buffer.Close(); errBuf != nil && err == nil {
			err = errBuf
		}
	})

	return err
}

// LocalAddr implements net.Conn.LocalAddr.
func (c *icmpListenerConn) LocalAddr() net.Addr {
	return c.listener.pConn.LocalAddr()
}

// RemoteAddr implements net.Conn.RemoteAddr.
func (c *icmpListenerConn) RemoteAddr() net.Addr {
	return c.rAddr
}

// SetDeadline implements net.Conn.SetDeadline.
func (c *icmpListenerConn) SetDeadline(t time.Time) error {
	c.writeDeadline.Set(t)

	return c.SetReadDeadline(t)
}

// SetReadDeadline implements net.Conn.SetDeadline.
func (c *icmpListenerConn) SetReadDeadline(t time.Time) error {
	return c.buffer.SetReadDeadline(t)
}

// SetWriteDeadline implements net.Conn.SetDeadline.
func (c *icmpListenerConn) SetWriteDeadline(t time.Time) error {
	c.writeDeadline.Set(t)
	// Write deadline of underlying connection should not be changed
	// since the connection can be shared.
	return nil
}
