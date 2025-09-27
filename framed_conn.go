package netx

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

var ErrFrameTooLarge = errors.New("framedConn: frame too large")

type framedConn struct {
	bc           net.Conn
	maxFrameSize int
	pending      []byte
	rmu, wmu     sync.Mutex
}

type FramedConnOption func(*framedConn)

func WithMaxFrameSize(size int) FramedConnOption {
	return func(c *framedConn) {
		c.maxFrameSize = size
	}
}

// NewFramedConn wraps a net.Conn with a simple length-prefixed framing protocol.
// Each frame is prefixed with a 4-byte big-endian unsigned integer indicating the length of the frame.
// If the frame size exceeds maxFrameSize, Read will return ErrFrameTooLarge.
// The default maxFrameSize is 32KB.
func NewFramedConn(c net.Conn, opts ...FramedConnOption) net.Conn {
	fc := &framedConn{
		bc:           c,
		maxFrameSize: 32 * 1024, // 32KB default max frame size
	}
	for _, opt := range opts {
		opt(fc)
	}
	return fc
}

// Read returns at most one frame's bytes; large frames are delivered across multiple Reads.
func (c *framedConn) Read(p []byte) (int, error) {
	c.rmu.Lock()
	defer c.rmu.Unlock()

	if len(c.pending) > 0 {
		n := copy(p, c.pending)
		c.pending = c.pending[n:]
		return n, nil
	}

	var hdr [4]byte
	if _, err := io.ReadFull(c.bc, hdr[:]); err != nil {
		return 0, err
	}
	n := int(binary.BigEndian.Uint32(hdr[:]))
	if n > c.maxFrameSize {
		return 0, ErrFrameTooLarge
	}
	if n == 0 {
		// Deliver empty frame as a zero-length read to allow keep-alives.
		return 0, nil
	}

	if len(p) >= n {
		_, err := io.ReadFull(c.bc, p[:n])
		return n, err
	}

	buf := make([]byte, n)
	if _, err := io.ReadFull(c.bc, buf); err != nil {
		return 0, err
	}
	w := copy(p, buf)
	c.pending = buf[w:]
	return w, nil
}

// Write sends p as a single frame.
func (c *framedConn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()

	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(p)))
	if _, err := c.bc.Write(hdr[:]); err != nil {
		return 0, err
	}
	if len(p) == 0 {
		return 0, nil
	}
	if _, err := c.bc.Write(p); err != nil {
		return 0, err
	}
	// If the underlying layer is buffered and implements Flush, flush now to coalesce header+payload.
	if fw, ok := c.bc.(BufConn); ok {
		if err := fw.Flush(); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (c *framedConn) Close() error                       { return c.bc.Close() }
func (c *framedConn) LocalAddr() net.Addr                { return c.bc.LocalAddr() }
func (c *framedConn) RemoteAddr() net.Addr               { return c.bc.RemoteAddr() }
func (c *framedConn) SetDeadline(t time.Time) error      { return c.bc.SetDeadline(t) }
func (c *framedConn) SetReadDeadline(t time.Time) error  { return c.bc.SetReadDeadline(t) }
func (c *framedConn) SetWriteDeadline(t time.Time) error { return c.bc.SetWriteDeadline(t) }
