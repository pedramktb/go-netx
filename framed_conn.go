/*
FramedConn is a network layer that adds a length-prefixed framing protocol inside a stream-oriented
connection (like TCP). This allows wrapping packet-based connections inside stream ones (e.g.
UDP over TCP+TLS), preserving message boundaries. Each frame consists of a 4-byte big-endian
length header followed by the payload.

Since FramedConn performs two writes per frame (one for the header and one for the payload),
it is highly recommended to wrap the underlying connection in a BufferedConn. This coalesces
the writes into a single system call, significantly improving performance.
*/

package netx

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
)

var ErrFrameTooLarge = errors.New("framedConn: frame too large")

type framedConn struct {
	net.Conn
	maxFrameSize int
	pending      []byte
	rmu, wmu     sync.Mutex
}

type FramedConnOption func(*framedConn)

func WithMaxFrameSize(size uint32) FramedConnOption {
	return func(c *framedConn) {
		c.maxFrameSize = int(size)
	}
}

// NewFramedConn wraps a net.Conn with a simple length-prefixed framing protocol.
// Each frame is prefixed with a 4-byte big-endian unsigned integer indicating the length of the frame.
// If the frame size exceeds maxFrameSize, Read will return ErrFrameTooLarge.
// The default maxFrameSize is 32KB.
func NewFramedConn(c net.Conn, opts ...FramedConnOption) net.Conn {
	fc := &framedConn{
		Conn:         c,
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
	if _, err := io.ReadFull(c.Conn, hdr[:]); err != nil {
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
		_, err := io.ReadFull(c.Conn, p[:n])
		return n, err
	}

	buf := make([]byte, n)
	if _, err := io.ReadFull(c.Conn, buf); err != nil {
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
	if _, err := c.Conn.Write(hdr[:]); err != nil {
		return 0, err
	}
	if len(p) == 0 {
		return 0, nil
	}
	if _, err := c.Conn.Write(p); err != nil {
		return 0, err
	}
	// If the underlying layer is buffered and implements Flush, flush now to coalesce header+payload.
	if fw, ok := c.Conn.(BufConn); ok {
		if err := fw.Flush(); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}
