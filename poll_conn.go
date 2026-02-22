/*
PollConn provides persistent, bidirectional connection semantics over a request-response net.Conn.

Many transport protocols (such as DNS tunneling) operate in a strict request-response model:
the client sends a request and receives exactly one response. PollConn abstracts this into a
standard streaming net.Conn by managing the request-response cycle internally.

When the user writes data, it is queued and sent as the payload of the next request. When idle,
PollConn automatically polls the server at a configurable interval to retrieve any server-initiated
data. Response data is buffered and made available through Read.

Important: The underlying connection must support sending empty (zero-length) writes that still
trigger a round-trip with the server. This is typically achieved by wrapping the connection with
a framing layer (e.g., DemuxClient) that adds a header to every write.

Typical usage with DNS tunneling:

	dnstConn := dnst.NewDNSTClientConn(transport, domain)
	demuxClient, _ := netx.NewDemuxClient(dnstConn, sessionID)()
	persistent := netx.NewPollConn(demuxClient)
	// Use persistent as a regular net.Conn
*/
package netx

import (
	"io"
	"net"
	"os"
	"sync"
	"time"
)

type pollConn struct {
	conn net.Conn

	sendCh chan []byte // user write data queued for sending
	recvCh chan []byte // received response data chunks

	mu           sync.Mutex
	unread       []byte
	readDeadline time.Time
	readDlNotify chan struct{}

	wMu           sync.Mutex
	writeDeadline time.Time

	pollInterval time.Duration
	bufSize      int

	closed    chan struct{}
	closeOnce sync.Once
}

type PollConnOption func(*pollConn)

// WithPollInterval sets the polling interval for idle cycles.
// When no user data is queued, PollConn waits this duration before sending an empty
// request to check for server-initiated data.
// Default is 100ms.
func WithPollInterval(d time.Duration) PollConnOption {
	return func(c *pollConn) {
		c.pollInterval = d
	}
}

// WithPollBufSize sets the read buffer size for reading responses from the underlying connection.
// Default is 4096.
func WithPollBufSize(size uint32) PollConnOption {
	return func(c *pollConn) {
		c.bufSize = int(size)
	}
}

// WithPollSendQueueSize sets the capacity of the send queue.
// Write calls block when this queue is full, providing natural backpressure.
// Default is 32.
func WithPollSendQueueSize(size uint32) PollConnOption {
	return func(c *pollConn) {
		c.sendCh = make(chan []byte, size)
	}
}

// WithPollRecvQueueSize sets the capacity of the receive queue.
// When full, the poll loop blocks until the user reads, providing natural backpressure.
// Default is 32.
func WithPollRecvQueueSize(size uint32) PollConnOption {
	return func(c *pollConn) {
		c.recvCh = make(chan []byte, size)
	}
}

// NewPollConn wraps a request-response net.Conn to provide persistent bidirectional
// connection semantics. The underlying connection operates in lock-step: each write
// (request) is followed by a read (response). PollConn manages this cycle automatically.
func NewPollConn(conn net.Conn, opts ...PollConnOption) net.Conn {
	c := &pollConn{
		conn:         conn,
		sendCh:       make(chan []byte, 32),
		recvCh:       make(chan []byte, 32),
		pollInterval: 100 * time.Millisecond,
		bufSize:      4096,
		closed:       make(chan struct{}),
		readDlNotify: make(chan struct{}),
	}
	for _, opt := range opts {
		opt(c)
	}
	go c.loop()
	return c
}

func (c *pollConn) loop() {
	buf := make([]byte, c.bufSize)
	defer close(c.recvCh)

	for {
		var data []byte

		select {
		case <-c.closed:
			return
		case d := <-c.sendCh:
			data = d
		case <-time.After(c.pollInterval):
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

func (c *pollConn) Read(b []byte) (int, error) {
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

func (c *pollConn) Write(b []byte) (int, error) {
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

func (c *pollConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.closed)
		err = c.conn.Close()
	})
	return err
}

func (c *pollConn) LocalAddr() net.Addr  { return c.conn.LocalAddr() }
func (c *pollConn) RemoteAddr() net.Addr { return c.conn.RemoteAddr() }

func (c *pollConn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}

func (c *pollConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readDeadline = t
	close(c.readDlNotify)
	c.readDlNotify = make(chan struct{})
	return nil
}

func (c *pollConn) SetWriteDeadline(t time.Time) error {
	c.wMu.Lock()
	defer c.wMu.Unlock()
	c.writeDeadline = t
	return nil
}
