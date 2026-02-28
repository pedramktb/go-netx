/*
PollConn provides persistent, bidirectional connection semantics over a
request-response net.Conn.

Many transport protocols operate in a strict request-response model: the client sends a request
and receives exactly one response. PollConn abstracts this into a standard
streaming net.Conn by managing the request-response cycle internally.

PollClientConn: queues user writes and sends them as the payload of the next request.
When idle it polls the server at a configurable interval so that server-initiated data is
delivered promptly. Every write is followed by a read on the underlying connection.

PollServerConn: reads each incoming request, delivers any payload to the caller
via Read, then immediately responds with any data queued by the caller via Write (or an empty
response if none is pending). This ensures the client's Read always receives a reply and the
server can push data on the next incoming poll.

The two halves must be used together: wrapping both sides of a stream connection with
PollConn + PollServerConn gives the illusion of a normal bidirectional net.Conn over a
protocol that is inherently lock-step request → response.
*/

package netx

import (
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"time"
)

func init() {
	Register("poll", func(params map[string]string, listener bool) (Wrapper, error) {
		opts := []PollConnOption{}
		for key, value := range params {
			switch key {
			case "interval":
				if listener {
					return Wrapper{}, fmt.Errorf("poll: interval parameter is only valid for clients")
				}
				dur, err := time.ParseDuration(value)
				if err != nil {
					return Wrapper{}, fmt.Errorf("poll: invalid interval parameter %q: %w", value, err)
				}
				opts = append(opts, WithPollInterval(dur))
			case "sendq":
				size, err := strconv.ParseUint(value, 10, 16)
				if err != nil {
					return Wrapper{}, fmt.Errorf("poll: invalid sendq parameter %q: %w", value, err)
				}
				opts = append(opts, WithPollSendQueue(uint16(size)))
			case "recvq":
				size, err := strconv.ParseUint(value, 10, 16)
				if err != nil {
					return Wrapper{}, fmt.Errorf("poll: invalid recvq parameter %q: %w", value, err)
				}
				opts = append(opts, WithPollRecvQueue(uint16(size)))
			default:
				return Wrapper{}, fmt.Errorf("poll: unknown parameter %q", key)
			}
		}
		clientConnToConn := func(c net.Conn) (net.Conn, error) {
			return NewPollConn(c, opts...), nil
		}
		serverConnToConn := func(c net.Conn) (net.Conn, error) {
			return NewPollServerConn(c, opts...), nil
		}
		return Wrapper{
			Name:   "poll",
			Params: params,
			ListenerToListener: func(l net.Listener) (net.Listener, error) {
				return ConnWrapListener(l, serverConnToConn)
			},
			DialerToDialer: func(f Dialer) (Dialer, error) {
				return ConnWrapDialer(f, clientConnToConn)
			},
			ConnToConn: func(c net.Conn) (net.Conn, error) {
				if listener {
					return serverConnToConn(c)
				}
				return clientConnToConn(c)
			},
		}, nil
	})
}

type pollConnCore struct {
	sendCh   chan []byte // server Write data queued for the next response
	recvCh   chan []byte // received request payloads
	interval time.Duration
}

type PollConnOption func(*pollConnCore)

// WithPollSendQueue sets the capacity of the send queue.
// Write calls block when this queue is full, providing natural backpressure.
// Default is 32.
func WithPollSendQueue(size uint16) PollConnOption {
	return func(c *pollConnCore) {
		c.sendCh = make(chan []byte, size)
	}
}

// WithPollRecvQueue sets the capacity of the receive queue.
// When full, the poll loop blocks until the user reads, providing natural backpressure.
// Default is 32.
func WithPollRecvQueue(size uint16) PollConnOption {
	return func(c *pollConnCore) {
		c.recvCh = make(chan []byte, size)
	}
}

// WithPollInterval sets the polling interval for idle cycles.
// When no user data is queued, PollConn waits this duration before sending an empty
// request to check for server-initiated data.
// Default is 100ms.
func WithPollInterval(d time.Duration) PollConnOption {
	return func(c *pollConnCore) {
		c.interval = d
	}
}

type pollConnServer struct {
	conn net.Conn

	pollConnCore

	mu           sync.Mutex
	unread       []byte
	readDeadline time.Time
	readDlNotify chan struct{}

	wMu           sync.Mutex
	writeDeadline time.Time

	closed    chan struct{}
	closeOnce sync.Once
}

// NewPollServerConn wraps a net.Conn to serve as the server side of the poll protocol.
// It must be used together with NewPollConn on the client side: for every request the client
// sends (including empty polls), the server reads it, picks up any pending Write data, and
// sends it back as the response. This ensures the client's Read never blocks indefinitely.
func NewPollServerConn(conn net.Conn, opts ...PollConnOption) net.Conn {
	c := &pollConnServer{
		conn: conn,
		pollConnCore: pollConnCore{
			sendCh: make(chan []byte, 32),
			recvCh: make(chan []byte, 32),
		},
		closed:       make(chan struct{}),
		readDlNotify: make(chan struct{}),
	}
	for _, opt := range opts {
		opt(&c.pollConnCore)
	}
	go c.loop()
	return c
}

func (c *pollConnServer) loop() {
	buf := make([]byte, MaxPacketSize)
	defer close(c.recvCh)

	for {
		// Read the client's request (may be empty).
		n, err := c.conn.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			select {
			case c.recvCh <- chunk:
			case <-c.closed:
				return
			}
		}
		if err != nil {
			return
		}

		// Respond with any queued server data, or an empty response.
		var response []byte
		select {
		case data := <-c.sendCh:
			response = data
		default:
			// no pending data; send empty response so the client's Read returns
		}

		if _, err := c.conn.Write(response); err != nil {
			return
		}
	}
}

// MaxWrite forwards the underlying connection's MaxWrite limit, if any.
func (c *pollConnServer) MaxWrite() uint16 {
	if mw, ok := c.conn.(interface{ MaxWrite() uint16 }); ok {
		return mw.MaxWrite()
	}
	return 0
}

func (c *pollConnServer) Read(b []byte) (int, error) {
	c.mu.Lock()
	if len(c.unread) > 0 {
		n := copy(b, c.unread)
		if n < len(c.unread) {
			c.unread = c.unread[n:]
		} else {
			c.unread = nil
		}
		c.mu.Unlock()
		return n, nil
	}
	c.mu.Unlock()

	for {
		c.mu.Lock()
		deadline := c.readDeadline
		notify := c.readDlNotify
		c.mu.Unlock()

		var timer *time.Timer
		var timeoutCh <-chan time.Time
		if !deadline.IsZero() {
			dur := time.Until(deadline)
			if dur <= 0 {
				return 0, os.ErrDeadlineExceeded
			}
			timer = time.NewTimer(dur)
			timeoutCh = timer.C
		}

		select {
		case data, ok := <-c.recvCh:
			if timer != nil {
				timer.Stop()
			}
			if !ok {
				return 0, io.EOF
			}
			c.mu.Lock()
			n := copy(b, data)
			if n < len(data) {
				c.unread = data[n:]
			}
			c.mu.Unlock()
			return n, nil
		case <-c.closed:
			if timer != nil {
				timer.Stop()
			}
			return 0, net.ErrClosed
		case <-timeoutCh:
			return 0, os.ErrDeadlineExceeded
		case <-notify:
			if timer != nil {
				timer.Stop()
			}
			// Deadline changed, loop to pick up new deadline.
		}
	}
}

func (c *pollConnServer) Write(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}

	data := make([]byte, len(b))
	copy(data, b)

	c.wMu.Lock()
	deadline := c.writeDeadline
	c.wMu.Unlock()

	var timer *time.Timer
	var timeoutCh <-chan time.Time
	if !deadline.IsZero() {
		dur := time.Until(deadline)
		if dur <= 0 {
			return 0, os.ErrDeadlineExceeded
		}
		timer = time.NewTimer(dur)
		timeoutCh = timer.C
		defer timer.Stop()
	}

	select {
	case <-c.closed:
		return 0, net.ErrClosed
	default:
	}

	select {
	case <-c.closed:
		return 0, net.ErrClosed
	case c.sendCh <- data:
		return len(b), nil
	case <-timeoutCh:
		return 0, os.ErrDeadlineExceeded
	}
}

func (c *pollConnServer) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.closed)
		err = c.conn.Close()
	})
	return err
}

func (c *pollConnServer) LocalAddr() net.Addr  { return c.conn.LocalAddr() }
func (c *pollConnServer) RemoteAddr() net.Addr { return c.conn.RemoteAddr() }

func (c *pollConnServer) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}

func (c *pollConnServer) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readDeadline = t
	close(c.readDlNotify)
	c.readDlNotify = make(chan struct{})
	return nil
}

func (c *pollConnServer) SetWriteDeadline(t time.Time) error {
	c.wMu.Lock()
	defer c.wMu.Unlock()
	c.writeDeadline = t
	return nil
}

type pollConnClient struct {
	conn net.Conn

	pollConnCore

	mu           sync.Mutex
	unread       []byte
	readDeadline time.Time
	readDlNotify chan struct{}

	wMu           sync.Mutex
	writeDeadline time.Time

	closed    chan struct{}
	closeOnce sync.Once
}

// NewPollConn wraps a request-response net.Conn to provide persistent bidirectional
// connection semantics. The underlying connection operates in lock-step: each write
// (request) is followed by a read (response). PollConn manages this cycle automatically.
func NewPollConn(conn net.Conn, opts ...PollConnOption) net.Conn {
	c := &pollConnClient{
		conn: conn,
		pollConnCore: pollConnCore{
			sendCh:   make(chan []byte, 32),
			recvCh:   make(chan []byte, 32),
			interval: 100 * time.Millisecond,
		},
		closed:       make(chan struct{}),
		readDlNotify: make(chan struct{}),
	}
	for _, opt := range opts {
		opt(&c.pollConnCore)
	}
	go c.loop()
	return c
}

func (c *pollConnClient) loop() {
	buf := make([]byte, MaxPacketSize)
	defer close(c.recvCh)

	for {
		var data []byte

		select {
		case <-c.closed:
			return
		case d := <-c.sendCh:
			data = d
		case <-time.After(c.interval):
			// poll with nil data
		}

		// Write request to underlying connection
		if _, err := c.conn.Write(data); err != nil {
			return
		}

		// Read response from underlying connection
		n, err := c.conn.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			select {
			case c.recvCh <- chunk:
			case <-c.closed:
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// MaxWrite forwards the underlying connection's MaxWrite limit, if any.
// This allows layers above (e.g. SplitConn) to respect the transport's packet-size constraint.
func (c *pollConnClient) MaxWrite() uint16 {
	if mw, ok := c.conn.(interface{ MaxWrite() uint16 }); ok {
		return mw.MaxWrite()
	}
	return 0
}

func (c *pollConnClient) Read(b []byte) (int, error) {
	c.mu.Lock()
	if len(c.unread) > 0 {
		n := copy(b, c.unread)
		if n < len(c.unread) {
			c.unread = c.unread[n:]
		} else {
			c.unread = nil
		}
		c.mu.Unlock()
		return n, nil
	}
	c.mu.Unlock()

	for {
		c.mu.Lock()
		deadline := c.readDeadline
		notify := c.readDlNotify
		c.mu.Unlock()

		var timer *time.Timer
		var timeoutCh <-chan time.Time

		if !deadline.IsZero() {
			dur := time.Until(deadline)
			if dur <= 0 {
				return 0, os.ErrDeadlineExceeded
			}
			timer = time.NewTimer(dur)
			timeoutCh = timer.C
		}

		select {
		case data, ok := <-c.recvCh:
			if timer != nil {
				timer.Stop()
			}
			if !ok {
				return 0, io.EOF
			}
			c.mu.Lock()
			n := copy(b, data)
			if n < len(data) {
				c.unread = data[n:]
			}
			c.mu.Unlock()
			return n, nil
		case <-c.closed:
			if timer != nil {
				timer.Stop()
			}
			return 0, net.ErrClosed
		case <-timeoutCh:
			return 0, os.ErrDeadlineExceeded
		case <-notify:
			if timer != nil {
				timer.Stop()
			}
			// Deadline changed, loop to pick up new deadline
		}
	}
}

func (c *pollConnClient) Write(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}

	data := make([]byte, len(b))
	copy(data, b)

	c.wMu.Lock()
	deadline := c.writeDeadline
	c.wMu.Unlock()

	var timer *time.Timer
	var timeoutCh <-chan time.Time
	if !deadline.IsZero() {
		dur := time.Until(deadline)
		if dur <= 0 {
			return 0, os.ErrDeadlineExceeded
		}
		timer = time.NewTimer(dur)
		timeoutCh = timer.C
		defer timer.Stop()
	}

	select {
	case <-c.closed:
		return 0, net.ErrClosed
	default:
	}

	select {
	case <-c.closed:
		return 0, net.ErrClosed
	case c.sendCh <- data:
		return len(b), nil
	case <-timeoutCh:
		return 0, os.ErrDeadlineExceeded
	}
}

func (c *pollConnClient) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.closed)
		err = c.conn.Close()
	})
	return err
}

func (c *pollConnClient) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}

func (c *pollConnClient) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readDeadline = t
	close(c.readDlNotify)
	c.readDlNotify = make(chan struct{})
	return nil
}

func (c *pollConnClient) SetWriteDeadline(t time.Time) error {
	c.wMu.Lock()
	defer c.wMu.Unlock()
	c.writeDeadline = t
	return nil
}

func (c *pollConnClient) LocalAddr() net.Addr  { return c.conn.LocalAddr() }
func (c *pollConnClient) RemoteAddr() net.Addr { return c.conn.RemoteAddr() }
