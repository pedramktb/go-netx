package netx

import (
	"io"
	"net"
	"os"
	"sync"
	"time"
)

type taggedPipeAddr string

func (a taggedPipeAddr) Network() string { return "pipe" }
func (a taggedPipeAddr) String() string  { return string(a) }

type taggedPipeReq struct {
	data []byte
	tag  any
	n    int      // bytes read so far
	done chan int // processed bytes total
}

type taggedPipeConn struct {
	mu           sync.Mutex
	chRead       <-chan *taggedPipeReq
	chWrite      chan<- *taggedPipeReq
	localAddr    net.Addr
	remoteAddr   net.Addr
	closed       chan struct{}
	closeOnce    sync.Once
	req          *taggedPipeReq
	readDeadline time.Time
}

func TaggedPipe() (TaggedConn, TaggedConn) {
	ch1 := make(chan *taggedPipeReq)
	ch2 := make(chan *taggedPipeReq)

	c1 := &taggedPipeConn{
		chRead:     ch1,
		chWrite:    ch2,
		localAddr:  taggedPipeAddr("c1"),
		remoteAddr: taggedPipeAddr("c2"),
		closed:     make(chan struct{}),
	}
	c2 := &taggedPipeConn{
		chRead:     ch2,
		chWrite:    ch1,
		localAddr:  taggedPipeAddr("c2"),
		remoteAddr: taggedPipeAddr("c1"),
		closed:     make(chan struct{}),
	}
	return c1, c2
}

func (c *taggedPipeConn) ReadTagged(b []byte, tag *any) (int, error) {
	c.mu.Lock()
	req := c.req
	c.mu.Unlock()

	if req == nil {
		var timeout <-chan time.Time
		if !c.readDeadline.IsZero() {
			timeout = time.After(time.Until(c.readDeadline))
		}

		select {
		case <-c.closed:
			return 0, io.ErrClosedPipe
		case r, ok := <-c.chRead:
			if !ok {
				return 0, io.EOF
			}
			req = r
		case <-timeout:
			return 0, os.ErrDeadlineExceeded
		}
	}

	// Safety check just in case
	if req.n > len(req.data) {
		return 0, io.EOF
	}

	n := copy(b, req.data[req.n:])
	if tag != nil {
		*tag = req.tag
	}
	req.n += n

	if req.n == len(req.data) {
		// Fully read
		select {
		case <-c.closed:
		case req.done <- req.n:
		default:
			// Should not block ideally but just in case
			go func() { req.done <- req.n }()
		}
		c.mu.Lock()
		c.req = nil
		c.mu.Unlock()
	} else {
		// Partial read
		c.mu.Lock()
		c.req = req
		c.mu.Unlock()
	}
	return n, nil
}

func (c *taggedPipeConn) WriteTagged(b []byte, tag any) (n int, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = io.ErrClosedPipe
		}
	}()

	req := &taggedPipeReq{
		data: b,
		tag:  tag,
		done: make(chan int),
	}

	select {
	case <-c.closed:
		return 0, io.ErrClosedPipe
	case c.chWrite <- req:
	}

	select {
	case <-c.closed:
		return 0, io.ErrClosedPipe
	case n := <-req.done:
		return n, nil
	}
}

func (c *taggedPipeConn) Read(b []byte) (int, error) {
	var tag any
	return c.ReadTagged(b, &tag)
}

func (c *taggedPipeConn) Write(b []byte) (int, error) {
	return c.WriteTagged(b, nil)
}

func (c *taggedPipeConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
		close(c.chWrite)
	})
	return nil
}

func (c *taggedPipeConn) LocalAddr() net.Addr                { return c.localAddr }
func (c *taggedPipeConn) RemoteAddr() net.Addr               { return c.remoteAddr }
func (c *taggedPipeConn) SetDeadline(t time.Time) error      { c.SetReadDeadline(t); return nil } // simplified
func (c *taggedPipeConn) SetWriteDeadline(t time.Time) error { return nil }
func (c *taggedPipeConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.readDeadline = t
	return nil
}
